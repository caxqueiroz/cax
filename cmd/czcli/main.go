// Command czcli runs the assistant as a Bubble Tea TUI: it loads config, wires
// the memory store, multi-provider model, and dive agent, then launches the CLI
// channel which renders the dashboard and streams replies live.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/caxqueiroz/czcli/internal/agent"
	"github.com/caxqueiroz/czcli/internal/channel"
	"github.com/caxqueiroz/czcli/internal/channel/cli"
	"github.com/caxqueiroz/czcli/internal/config"
	"github.com/caxqueiroz/czcli/internal/lsp"
	"github.com/caxqueiroz/czcli/internal/mcp"
	"github.com/caxqueiroz/czcli/internal/memory"
	"github.com/caxqueiroz/czcli/internal/plugins"
	"github.com/caxqueiroz/czcli/internal/scheduler"
	"github.com/caxqueiroz/czcli/internal/skills"
	"github.com/deepnoodle-ai/dive"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "czcli: %v\n", err)
		os.Exit(1)
	}
}

// run loads config, wires dependencies, and launches the TUI channel.
func run() error {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	path := os.Getenv("CZCLI_CONFIG")
	if path == "" {
		path = "config.yaml"
	}
	cfg, err := config.Load(path)
	if err != nil {
		return fmt.Errorf("load config from %q (set CZCLI_CONFIG to override): %w", path, err)
	}

	embedder, err := memory.NewEmbedder(cfg.Embeddings)
	if err != nil {
		return fmt.Errorf("build embedder: %w", err)
	}

	store, err := memory.Open(cfg.Memory, embedder)
	if err != nil {
		return fmt.Errorf("open memory store: %w", err)
	}
	defer func() {
		if cerr := store.Close(); cerr != nil {
			slog.Warn("close store", "err", cerr)
		}
	}()

	model, err := agent.BuildModel(cfg)
	if err != nil {
		return fmt.Errorf("build model: %w", err)
	}

	// Plugins: discover Claude Code-compatible plugin bundles. Drop-folder
	// roots come from cfg.Plugins.Dirs (defaults: ~/.czcli/plugins,
	// .czcli/plugins). State file lives at ~/.czcli/plugins.json. Per-plugin
	// parse errors are logged + skipped; a broken plugin never blocks startup.
	pluginsMgr := plugins.New(cfg.Plugins, pluginsStatePath(), gitClone)
	contrib, _, err := pluginsMgr.Load(ctx)
	if err != nil {
		slog.Warn("plugins: initial load", "error", err)
	}

	// Load skills (best-effort), pulling extra dirs from plugin contributions.
	skillRes, err := skills.Load(cfg.Skills, contrib.SkillDirs)
	if err != nil {
		slog.Warn("skills: load failed; continuing without skills", "err", err)
		skillRes = nil
	}

	// Connect MCP servers (best-effort: per-server errors land in ServerInfo),
	// merging user config and plugin contributions.
	mcpServers := mergeMCPServers(cfg.MCP.Servers, contrib.MCPServers)
	mcpTools, mcpInfos, err := mcp.Connect(ctx, mcpServers, mcpTokenPath())
	if err != nil {
		slog.Warn("mcp: connect failed; continuing without MCP tools", "err", err)
	}

	// LSP: spawn user-configured servers (plus plugin-contributed ones merged
	// in by mergeLSPServers). Per-server errors are recorded in ServerInfo;
	// initialization never aborts startup. Hot-reload (pluginAdapter.Rebuild)
	// rotates the manager via lspHolder so child processes from prior
	// generations are reaped.
	holder := &lspHolder{}
	holder.swap(buildLSP(ctx, cfg, contrib))
	defer holder.closeCurrent()
	lspTools, lspInfos := holder.current()

	assistant, err := agent.BuildWithMCPInfos(ctx, cfg, store, model, skillRes, mcpTools, mcpInfos, lspTools, lspInfos)
	if err != nil {
		return fmt.Errorf("build assistant: %w", err)
	}

	// Seed config-defined schedules into the store (idempotent) so they
	// participate in the scheduler's Load/Reload alongside CLI-added ones.
	for _, sc := range cfg.Schedules {
		if err := store.UpsertSchedule(ctx, sc); err != nil {
			slog.Warn("scheduler: failed to seed schedule from config", "schedule", sc.Name, "error", err)
		}
	}

	sched := scheduler.New(store, schedulerRunFunc(assistant))
	if err := sched.Load(ctx); err != nil {
		slog.Error("scheduler: initial load failed", "error", err)
		// Non-fatal: continue without schedules rather than aborting startup.
	} else {
		sched.Start()
		slog.Info("scheduler started")
	}
	defer sched.Stop()

	pluginsAdp := pluginAdapter{mgr: pluginsMgr, cfg: cfg, assistant: assistant, lspHolder: holder}
	ch := cli.New(
		cli.WithSessionID("cli"),
		cli.WithScheduler(scheduleAdapter{store: store, sched: sched}),
		cli.WithPlugins(pluginsAdp),
	)
	statusFn := func(ctx context.Context) (channel.Status, error) {
		st, err := assistant.Status(ctx)
		if err != nil {
			return st, err
		}
		st.PluginCount, st.PluginNames = pluginsAdp.Snapshot(ctx)
		return st, nil
	}
	if err := ch.Start(ctx, assistant.Handle, statusFn); err != nil {
		return fmt.Errorf("run cli channel: %w", err)
	}
	return nil
}

// schedulerRunFunc builds a scheduler.RunFunc that runs a stored prompt through
// the agent under a synthetic per-channel session and routes the reply to the
// named channel. MVP: replies are logged/printed; a channel registry can route
// to Telegram/Discord/etc. later without changing this signature.
func schedulerRunFunc(assistant *agent.Assistant) scheduler.RunFunc {
	return func(ctx context.Context, prompt, ch string) error {
		msg := channel.Message{
			SessionID: "scheduler:" + ch, // distinct session per scheduled channel
			Text:      prompt,
		}
		// Drain stream events to a no-op sink for MVP; a real channel would render.
		emit := func(channel.StreamEvent) {}

		reply, err := assistant.Handle(ctx, msg, emit)
		if err != nil {
			return fmt.Errorf("scheduled run (channel=%s): %w", ch, err)
		}

		// MVP routing: log + print. Replace with a channel registry lookup later.
		slog.Info("scheduled run completed", "channel", ch, "reply_len", len(reply.Text))
		fmt.Printf("[scheduled:%s] %s\n", ch, reply.Text)
		return nil
	}
}

// scheduleAdapter satisfies the CLI's /schedule CRUD backend over the memory
// store and the live scheduler. List/Upsert hit the store; Reload re-registers
// cron entries so changes take effect immediately.
type scheduleAdapter struct {
	store *memory.Store
	sched *scheduler.Scheduler
}

func (a scheduleAdapter) List(ctx context.Context) ([]config.ScheduleConfig, error) {
	return a.store.ListSchedules(ctx)
}

func (a scheduleAdapter) Upsert(ctx context.Context, sc config.ScheduleConfig) error {
	return a.store.UpsertSchedule(ctx, sc)
}

func (a scheduleAdapter) Reload(ctx context.Context) error {
	return a.sched.Reload(ctx)
}

// mcpTokenPath returns the default OAuth token-store path under the user's
// home dir, falling back to a process-local file when home is unresolvable.
func mcpTokenPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(os.TempDir(), "czcli-mcp-tokens.json")
	}
	return filepath.Join(home, ".czcli", "mcp-tokens.json")
}

// pluginsStatePath returns the default plugin state file under the user's home
// dir, falling back to a process-local file when home is unresolvable.
func pluginsStatePath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(os.TempDir(), "czcli-plugins.json")
	}
	return filepath.Join(home, ".czcli", "plugins.json")
}

// gitClone is the production CloneFunc: shallow git clone of gitURL into dest.
// We pin --depth=1 since runtime never needs history. Any non-zero exit is
// surfaced verbatim through the returned error.
func gitClone(ctx context.Context, gitURL, dest string) error {
	cmd := exec.CommandContext(ctx, "git", "clone", "--depth=1", gitURL, dest)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git clone %s: %w (output: %s)", gitURL, err, string(out))
	}
	return nil
}

// buildLSP merges user-config and plugin-contributed LSP servers, then
// instantiates the manager + tools when LSP is enabled and at least one
// server is configured. Returns (nil, nil, nil) when LSP is disabled or no
// servers were configured. Errors are logged + swallowed; per-server failures
// already surface via ServerInfo.LastError.
func buildLSP(ctx context.Context, cfg *config.Config, contrib plugins.Contributions) (*lsp.Manager, []dive.Tool, []lsp.ServerInfo) {
	if !cfg.LSP.Enabled {
		return nil, nil, nil
	}
	servers := mergeLSPServers(cfg.LSP.Servers, contrib.LSPServers)
	if len(servers) == 0 {
		return nil, nil, nil
	}
	rootDir, err := os.Getwd()
	if err != nil {
		slog.Warn("lsp: resolve workdir", "err", err)
		rootDir = "."
	}
	mgr, infos, err := lsp.New(ctx, servers, rootDir)
	if err != nil {
		slog.Warn("lsp: manager init failed; continuing without LSP", "err", err)
		return nil, nil, nil
	}
	return mgr, mgr.Tools(), infos
}

// mergeLSPServers concatenates user-config and plugin-contributed LSP servers,
// dropping duplicates by Name (user config wins). Mirrors mergeMCPServers.
func mergeLSPServers(user, fromPlugins []config.LSPServerConfig) []config.LSPServerConfig {
	if len(fromPlugins) == 0 {
		return user
	}
	seen := make(map[string]bool, len(user)+len(fromPlugins))
	out := make([]config.LSPServerConfig, 0, len(user)+len(fromPlugins))
	for _, s := range user {
		if seen[s.Name] {
			continue
		}
		seen[s.Name] = true
		out = append(out, s)
	}
	for _, s := range fromPlugins {
		if seen[s.Name] {
			slog.Warn("plugins: lsp server name collides with user config; keeping user", "name", s.Name)
			continue
		}
		seen[s.Name] = true
		out = append(out, s)
	}
	return out
}

// lspHolder owns the current generation of the LSP manager + its tools/infos
// so plugin hot-reload can rotate the manager without leaking child
// processes. swap closes the previous generation; closeCurrent runs on
// process shutdown.
type lspHolder struct {
	mgr   *lsp.Manager
	tools []dive.Tool
	infos []lsp.ServerInfo
}

func (h *lspHolder) swap(mgr *lsp.Manager, tools []dive.Tool, infos []lsp.ServerInfo) {
	if h.mgr != nil {
		if err := h.mgr.Close(); err != nil {
			slog.Warn("lsp: close previous manager", "error", err)
		}
	}
	h.mgr, h.tools, h.infos = mgr, tools, infos
}

func (h *lspHolder) current() ([]dive.Tool, []lsp.ServerInfo) {
	return h.tools, h.infos
}

func (h *lspHolder) closeCurrent() {
	if h.mgr != nil {
		if err := h.mgr.Close(); err != nil {
			slog.Warn("lsp: close manager", "error", err)
		}
		h.mgr = nil
	}
}

// mergeMCPServers concatenates user-config and plugin-contributed MCP servers,
// dropping duplicates by Name (user config wins). Deterministic order: user
// entries first, then plugin entries in the order Manager.Load returned them.
func mergeMCPServers(user, fromPlugins []config.MCPServerConfig) []config.MCPServerConfig {
	if len(fromPlugins) == 0 {
		return user
	}
	seen := make(map[string]bool, len(user)+len(fromPlugins))
	out := make([]config.MCPServerConfig, 0, len(user)+len(fromPlugins))
	for _, s := range user {
		if seen[s.Name] {
			continue
		}
		seen[s.Name] = true
		out = append(out, s)
	}
	for _, s := range fromPlugins {
		if seen[s.Name] {
			slog.Warn("plugins: mcp server name collides with user config; keeping user", "name", s.Name)
			continue
		}
		seen[s.Name] = true
		out = append(out, s)
	}
	return out
}

// pluginAdapter satisfies cli.pluginBackend over the Manager + Assistant. It
// flattens plugins.PluginInfo into cli.PluginListItem for List, and on every
// mutation re-runs Manager.Load + skills.Load + mcp.Connect with the merged
// config + contributions and asks the Assistant to Rebuild so the agent picks
// up new skills/MCP/LSP/hooks/commands on the next turn.
type pluginAdapter struct {
	mgr       *plugins.Manager
	cfg       *config.Config
	assistant *agent.Assistant
	lspHolder *lspHolder
}

func (a pluginAdapter) List(ctx context.Context) ([]cli.PluginListItem, error) {
	_, infos, err := a.mgr.Load(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]cli.PluginListItem, 0, len(infos))
	for _, p := range infos {
		out = append(out, cli.PluginListItem{
			Name:       p.Name,
			Version:    p.Version,
			Source:     p.Source,
			Enabled:    p.Enabled,
			SkillCount: p.Counts.Skills,
			MCPCount:   p.Counts.MCP,
			LSPCount:   p.Counts.LSP,
			HookCount:  p.Counts.Hooks,
			CmdCount:   p.Counts.Commands,
			AgentCount: p.Counts.Agents,
		})
	}
	return out, nil
}

func (a pluginAdapter) Install(ctx context.Context, gitURL, name string) error {
	_, err := a.mgr.Install(ctx, gitURL, name)
	return err
}

func (a pluginAdapter) Enable(_ context.Context, name string) error  { return a.mgr.Enable(name) }
func (a pluginAdapter) Disable(_ context.Context, name string) error { return a.mgr.Disable(name) }
func (a pluginAdapter) Remove(_ context.Context, name string) error  { return a.mgr.Remove(name) }

// Rebuild re-runs Manager.Load, merges plugin contributions with the user
// config, reloads skills and reconnects MCP, then asks the agent to swap its
// inner *dive.Agent atomically. In-flight turns finish under the old agent;
// the next turn picks up the new one.
func (a pluginAdapter) Rebuild(ctx context.Context) error {
	contrib, _, err := a.mgr.Load(ctx)
	if err != nil {
		return fmt.Errorf("plugins: reload: %w", err)
	}
	skillRes, err := skills.Load(a.cfg.Skills, contrib.SkillDirs)
	if err != nil {
		slog.Warn("plugins: rebuild: skills.Load failed; continuing without skills", "err", err)
		skillRes = nil
	}
	mcpServers := mergeMCPServers(a.cfg.MCP.Servers, contrib.MCPServers)
	mcpTools, mcpInfos, err := mcp.Connect(ctx, mcpServers, mcpTokenPath())
	if err != nil {
		slog.Warn("plugins: rebuild: mcp.Connect failed; continuing without MCP tools", "err", err)
	}
	// Re-spawn LSP servers off the merged config + plugin contributions;
	// holder.swap closes the previous generation's children.
	if a.lspHolder != nil {
		a.lspHolder.swap(buildLSP(ctx, a.cfg, contrib))
	}
	var lspTools []dive.Tool
	var lspInfos []lsp.ServerInfo
	if a.lspHolder != nil {
		lspTools, lspInfos = a.lspHolder.current()
	}
	if err := a.assistant.Rebuild(ctx, a.cfg, skillRes, mcpTools, mcpInfos, lspTools, lspInfos); err != nil {
		return fmt.Errorf("plugins: agent rebuild: %w", err)
	}
	slog.Info("plugins: hot-reload complete",
		"skills_dirs", len(contrib.SkillDirs),
		"mcp_servers", len(contrib.MCPServers),
		"lsp_servers", len(contrib.LSPServers),
		"hooks", len(contrib.Hooks),
		"commands", len(contrib.Commands),
	)
	return nil
}

// Snapshot returns the last-known enabled-plugin count + names for the
// dashboard. Cheap: re-runs Load against the local filesystem only.
func (a pluginAdapter) Snapshot(ctx context.Context) (int, []string) {
	_, infos, err := a.mgr.Load(ctx)
	if err != nil {
		return 0, nil
	}
	names := make([]string, 0, len(infos))
	for _, p := range infos {
		if p.Enabled {
			names = append(names, p.Name)
		}
	}
	return len(names), names
}
