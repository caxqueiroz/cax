// Command cax runs the assistant as a Bubble Tea TUI: it loads config, wires
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
	"sync"
	"syscall"

	"github.com/caxqueiroz/cax/internal/agent"
	"github.com/caxqueiroz/cax/internal/channel"
	"github.com/caxqueiroz/cax/internal/channel/cli"
	"github.com/caxqueiroz/cax/internal/config"
	"github.com/caxqueiroz/cax/internal/creator"
	"github.com/caxqueiroz/cax/internal/hooks"
	"github.com/caxqueiroz/cax/internal/lsp"
	"github.com/caxqueiroz/cax/internal/mcp"
	"github.com/caxqueiroz/cax/internal/memory"
	"github.com/caxqueiroz/cax/internal/plugins"
	"github.com/caxqueiroz/cax/internal/scheduler"
	"github.com/caxqueiroz/cax/internal/skills"
	"github.com/caxqueiroz/cax/internal/theme"
	"github.com/caxqueiroz/cax/internal/usercmds"
	"github.com/deepnoodle-ai/dive"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "cax: %v\n", err)
		os.Exit(1)
	}
}

// run loads config, wires dependencies, and launches the TUI channel.
func run() error {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	path, isDefault := resolveConfigPath()
	created, err := ensureDefaultConfig(path, isDefault)
	if err != nil {
		return err
	}
	if created {
		fmt.Fprintf(os.Stderr,
			"cax: wrote a default config to %s\nedit it to set provider model IDs and API keys, then run cax again\n",
			path)
		return nil
	}
	cfg, err := config.Load(path)
	if err != nil {
		return fmt.Errorf("load config from %q (override with $CAX_CONFIG or place one at ./.cax/config.yaml): %w", path, err)
	}

	// Themes: load embedded built-ins, then user themes from ~/.cax/themes,
	// then resolve the active one. Order: state.json > config.cli.theme >
	// terminal-adapted default-{dark,light}.
	theme.LoadBuiltins()
	if themesDir := userThemesDir(); themesDir != "" {
		theme.LoadUserDir(themesDir)
	}
	if active := theme.Resolve(theme.StateFile(), cfg.CLI.Theme); active != nil {
		slog.Info("theme: active", "name", active.Name)
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
	// roots come from cfg.Plugins.Dirs (defaults: ~/.cax/plugins,
	// .cax/plugins). State file lives at ~/.cax/plugins.json. Per-plugin
	// parse errors are logged + skipped; a broken plugin never blocks startup.
	pluginsMgr := plugins.New(cfg.Plugins, pluginsStatePath(), gitClone)
	contrib, _, err := pluginsMgr.Load(ctx)
	if err != nil {
		slog.Warn("plugins: initial load", "error", err)
	}
	// Merge user-level slash commands from cfg.Commands.Dirs into the plugin
	// Contributions. User commands take Source = "user:<dir-basename>" so the
	// dispatcher can tell them apart from plugin-contributed ones.
	contrib.Commands = append(contrib.Commands, usercmds.Load(cfg.Commands.Dirs)...)

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

	// Build the hooks dispatcher from plugin-contributed entries. Empty list
	// when no plugin ships hooks; the dispatcher itself is still constructed
	// (cheap) so /hooks and Status.HookCount report a stable 0.
	hookEntries := contribsToHookEntries(contrib.Hooks, slog.Default())
	hooksDisp := hooks.Load(hookEntries, slog.Default())

	// Creator: writer materializes new skills/agents/commands under
	// ~/.cax/{skills,agents,commands}; the reloader shim captures every
	// Assistant.Rebuild dependency so the create_* FuncTools can hot-reload
	// the live agent via the single-method creator.Reloader contract without
	// the creator package importing internal/agent.
	skillsDir, agentsDir, commandsDir := creatorPaths()
	writer := creator.Writer{
		SkillsDir:   skillsDir,
		AgentsDir:   agentsDir,
		CommandsDir: commandsDir,
	}
	reloader := &assistantReloader{
		cfg:       cfg,
		skillRes:  skillRes,
		mcpTools:  mcpTools,
		mcpInfos:  mcpInfos,
		lspTools:  lspTools,
		lspInfos:  lspInfos,
		hooksDisp: hooksDisp,
	}
	creatorTools := creator.Tools(writer, reloader)
	reloader.creatorTools = creatorTools

	assistant, err := agent.BuildWithMCPInfos(ctx, cfg, store, model, skillRes, mcpTools, mcpInfos, lspTools, lspInfos, hooksDisp, creatorTools)
	if err != nil {
		return fmt.Errorf("build assistant: %w", err)
	}
	reloader.assistant = assistant

	// Install the TUI permission modal. The dialog reads cfg.Tools.RequireConfirm
	// at construction; bypassing permissions is just `tools.require_confirm: false`
	// in config (the same knob as Claude Code's --dangerously-skip-permissions).
	permDialog := cli.NewPermDialog(cfg.Tools.RequireConfirm)
	assistant.SetDialog(permDialog)

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

	pluginsAdp := pluginAdapter{
		mgr:          pluginsMgr,
		cfg:          cfg,
		assistant:    assistant,
		lspHolder:    holder,
		creatorTools: creatorTools,
		reloader:     reloader,
	}
	ch := cli.New(
		cli.WithSessionID("cli"),
		cli.WithScheduler(scheduleAdapter{store: store, sched: sched}),
		cli.WithPlugins(pluginsAdp),
		cli.WithCreator(creatorAdapter{writer: writer, reloader: reloader}),
		cli.WithHookEntries(hookEntries),
		cli.WithUserCommands(contrib.Commands),
		cli.WithThemeStateFile(themeStatePath()),
		cli.WithPermDialog(permDialog),
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
		return filepath.Join(os.TempDir(), "cax-mcp-tokens.json")
	}
	return filepath.Join(home, ".cax", "mcp-tokens.json")
}

// userThemesDir returns ~/.cax/themes or "" if home cannot be resolved.
// The directory does not need to exist; LoadUserDir silently no-ops on
// missing dirs.
func userThemesDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".cax", "themes")
}

// themeStatePath returns the path Ctrl+T persists the active theme to.
// Falls back to a process-local file when HOME is unresolvable. The CLI
// channel writes only the "theme" field, preserving any other keys already
// present (mirrors theme.StateFile()'s schema).
func themeStatePath() string {
	if p := theme.StateFile(); p != "" {
		return p
	}
	return filepath.Join(os.TempDir(), "cax-state.json")
}

// pluginsStatePath returns the default plugin state file under the user's home
// dir, falling back to a process-local file when home is unresolvable.
func pluginsStatePath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(os.TempDir(), "cax-plugins.json")
	}
	return filepath.Join(home, ".cax", "plugins.json")
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
	// creatorTools is the slice of create_* FuncTools (skill/agent/command)
	// re-passed through assistant.Rebuild on every hot-reload so creator-
	// authored tool requests stay available after plugin mutations.
	creatorTools []dive.Tool
	// reloader is the assistantReloader shared with the create_* tools. Its
	// captured args are refreshed inside Rebuild BEFORE the swap so a
	// creator-triggered Rebuild always sees the latest plugin contributions.
	reloader *assistantReloader
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
	// Merge user-level slash commands so /reload + /plugin mutations hot-pick
	// changes to ~/.cax/commands/*.md alongside plugin contributions.
	contrib.Commands = append(contrib.Commands, usercmds.Load(a.cfg.Commands.Dirs)...)
	// Re-load any user themes so a fresh ~/.cax/themes/*.yaml is registered
	// before the next render. Built-ins were already embedded at startup; new
	// user themes will become Cycle-able after this call.
	if themesDir := userThemesDir(); themesDir != "" {
		theme.LoadUserDir(themesDir)
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
	// Rebuild the hooks dispatcher from the fresh plugin contributions so
	// hot-reload swaps the entry set atomically alongside skills/MCP/LSP.
	hookEntries := contribsToHookEntries(contrib.Hooks, slog.Default())
	hooksDisp := hooks.Load(hookEntries, slog.Default())

	// Refresh the reloader's captured args BEFORE the rebuild so any in-flight
	// create-tool call that runs during the swap sees the latest contributions.
	if a.reloader != nil {
		a.reloader.update(skillRes, mcpTools, mcpInfos, lspTools, lspInfos, hooksDisp)
	}
	if err := a.assistant.Rebuild(ctx, a.cfg, skillRes, mcpTools, mcpInfos, lspTools, lspInfos, hooksDisp, a.creatorTools); err != nil {
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

// contribsToHookEntries converts plugins.HookEntry (string Event + map matcher,
// used in the JSON manifests) into the typed hooks.Entry the dispatcher
// consumes. Unknown event names are skipped + logged so a malformed manifest
// can't take the whole startup down. Lives in main.go so internal/hooks does
// not import internal/plugins (the dispatcher only consumes typed Entries).
func contribsToHookEntries(in []plugins.HookEntry, logger *slog.Logger) []hooks.Entry {
	out := make([]hooks.Entry, 0, len(in))
	for _, h := range in {
		ev := hooks.Event(h.Event)
		switch ev {
		case hooks.EventUserPromptSubmit, hooks.EventPreToolUse,
			hooks.EventPostToolUse, hooks.EventStop:
			// ok
		default:
			logger.Warn("hooks: unknown event in plugin manifest, skipping",
				"event", h.Event, "source", h.Source)
			continue
		}
		out = append(out, hooks.Entry{
			Event:          ev,
			Matcher:        hooks.Matcher{Tool: h.Matcher["tool"], Command: h.Matcher["command"]},
			Command:        h.Command,
			TimeoutSeconds: h.TimeoutSeconds,
			Source:         h.Source,
		})
	}
	return out
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

// resolveConfigPath determines which config file to load. Order:
//  1. $CAX_CONFIG (literal path)
//  2. ./.cax/config.yaml (project-local)
//  3. ~/.cax/config.yaml (user default)
//
// The second return is true when the chosen path is the user default, which is
// the only path eligible for first-run auto-create by ensureDefaultConfig.
func resolveConfigPath() (path string, isDefault bool) {
	if p := os.Getenv("CAX_CONFIG"); p != "" {
		return p, false
	}
	local := filepath.Join(".cax", "config.yaml")
	if _, err := os.Stat(local); err == nil {
		return local, false
	}
	home, err := os.UserHomeDir()
	if err != nil {
		// No HOME — fall back to the project-local path so the error message is sensible.
		return local, false
	}
	return filepath.Join(home, ".cax", "config.yaml"), true
}

// assistantReloader satisfies creator.Reloader by capturing the seven
// non-creator dependencies *agent.Assistant.Rebuild needs (cfg, skillRes,
// mcpTools, mcpInfos, lspTools, lspInfos, hooksDisp) plus the creator tool
// slice itself (so Rebuild re-supplies the same set the live agent holds).
// pluginAdapter.Rebuild calls update() after every reload so the captured
// args track the latest plugin contributions.
type assistantReloader struct {
	mu           sync.Mutex
	assistant    *agent.Assistant
	cfg          *config.Config
	skillRes     *skills.LoadResult
	mcpTools     []dive.Tool
	mcpInfos     []mcp.ServerInfo
	lspTools     []dive.Tool
	lspInfos     []lsp.ServerInfo
	hooksDisp    *hooks.Dispatcher
	creatorTools []dive.Tool
}

// Rebuild satisfies creator.Reloader. It shallow-copies the captured args
// under the lock and calls assistant.Rebuild unlocked so a concurrent
// pluginAdapter.Rebuild that may itself want to update() doesn't deadlock
// against the in-flight reload.
func (r *assistantReloader) Rebuild(ctx context.Context) error {
	r.mu.Lock()
	a := r.assistant
	cfg := r.cfg
	skillRes := r.skillRes
	mcpTools := r.mcpTools
	mcpInfos := r.mcpInfos
	lspTools := r.lspTools
	lspInfos := r.lspInfos
	hooksDisp := r.hooksDisp
	creatorTools := r.creatorTools
	r.mu.Unlock()
	if a == nil {
		return fmt.Errorf("assistantReloader: not initialized")
	}
	return a.Rebuild(ctx, cfg, skillRes, mcpTools, mcpInfos, lspTools, lspInfos, hooksDisp, creatorTools)
}

// update refreshes the captured arg snapshot. Called by pluginAdapter.Rebuild
// (and the initial wiring in run) so a creator-triggered Rebuild always sees
// the latest plugin contributions.
func (r *assistantReloader) update(
	skillRes *skills.LoadResult,
	mcpTools []dive.Tool, mcpInfos []mcp.ServerInfo,
	lspTools []dive.Tool, lspInfos []lsp.ServerInfo,
	hooksDisp *hooks.Dispatcher,
) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.skillRes = skillRes
	r.mcpTools = mcpTools
	r.mcpInfos = mcpInfos
	r.lspTools = lspTools
	r.lspInfos = lspInfos
	r.hooksDisp = hooksDisp
}

// creatorAdapter satisfies cli.creatorBackend by delegating to the shared
// Writer + Reloader the create_* FuncTools use. Both /new wizard finalizes
// and natural-language create_* calls produce identical files this way and
// share a single reload path. Overwrite is wired to false — the wizard
// surfaces errors back to the user via the chat history.
type creatorAdapter struct {
	writer   creator.Writer
	reloader creator.Reloader
}

func (a creatorAdapter) CreateSkill(ctx context.Context, name, desc, body string) (string, error) {
	path, err := a.writer.WriteSkill(name, desc, body, false)
	if err != nil {
		return "", err
	}
	if err := a.reloader.Rebuild(ctx); err != nil {
		return path, fmt.Errorf("wrote %s but reload failed: %w", path, err)
	}
	return path, nil
}

func (a creatorAdapter) CreateAgent(ctx context.Context, name, desc string, tools []string, body string) (string, error) {
	path, err := a.writer.WriteAgent(name, desc, tools, nil, body, false)
	if err != nil {
		return "", err
	}
	if err := a.reloader.Rebuild(ctx); err != nil {
		return path, fmt.Errorf("wrote %s but reload failed: %w", path, err)
	}
	return path, nil
}

func (a creatorAdapter) CreateCommand(ctx context.Context, name, desc, hint, body string) (string, error) {
	path, err := a.writer.WriteCommand(name, desc, hint, body, false)
	if err != nil {
		return "", err
	}
	if err := a.reloader.Rebuild(ctx); err != nil {
		return path, fmt.Errorf("wrote %s but reload failed: %w", path, err)
	}
	return path, nil
}

// creatorPaths resolves the three target directories under the user's HOME.
// Falls back to a process-local tmp dir when HOME is unresolvable so cax
// still starts on broken environments (the create tools will still work; the
// files just land in a temp dir).
func creatorPaths() (skillsDir, agentsDir, commandsDir string) {
	home, err := os.UserHomeDir()
	if err != nil {
		slog.Warn("creator: cannot resolve home dir; using temp dir", "err", err)
		home = os.TempDir()
	}
	base := filepath.Join(home, ".cax")
	return filepath.Join(base, "skills"),
		filepath.Join(base, "agents"),
		filepath.Join(base, "commands")
}

// ensureDefaultConfig writes the embedded default config to path on first run.
// It only fires for the user-default path (~/.cax/config.yaml) and only when
// the file doesn't already exist. Returns true if it created the file so the
// caller can print a setup message and exit cleanly.
func ensureDefaultConfig(path string, isDefault bool) (created bool, err error) {
	if !isDefault {
		return false, nil
	}
	if _, err := os.Stat(path); err == nil {
		return false, nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return false, fmt.Errorf("create config dir: %w", err)
	}
	if err := os.WriteFile(path, config.ExampleYAML, 0o600); err != nil {
		return false, fmt.Errorf("write default config: %w", err)
	}
	return true, nil
}
