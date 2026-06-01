package cli

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/caxqueiroz/cax/internal/config"
	"github.com/caxqueiroz/cax/internal/creator"
	"github.com/caxqueiroz/cax/internal/theme"
)

// parseCommand splits "/name args..." into ("name", "args"). The leading slash
// is stripped; surrounding whitespace in args is trimmed.
func parseCommand(line string) (name, args string) {
	line = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "/"))
	if line == "" {
		return "", ""
	}
	if i := strings.IndexFunc(line, func(r rune) bool { return r == ' ' || r == '\t' }); i >= 0 {
		return line[:i], strings.TrimSpace(line[i+1:])
	}
	return line, ""
}

// handleCommand dispatches a slash command and returns display output, a
// quit flag, and an optional wizard to install on the model. Only /new sets
// the wizard pointer; every other command returns nil for it. The wizard
// is read by submit() in model.go to route subsequent text through the /new
// state machine. Pointer receiver: the /theme handler mutates the active
// theme registry and needs to call m.refreshViewport().
func (m *model) handleCommand(line string) (string, bool, *creator.Wizard) {
	name, args := parseCommand(line)
	switch name {
	case "quit", "exit":
		return "", true, nil
	case "cancel":
		// /cancel clears an active wizard. When no wizard is active we fall
		// through to a hint so users discover the command's purpose.
		if m.wizard != nil {
			m.wizard = nil
			return "/new: cancelled", false, nil
		}
		return "nothing to cancel", false, nil
	case "stats":
		return m.cmdStats(), false, nil
	case "tools":
		return m.cmdTools(), false, nil
	case "agents":
		return m.cmdAgents(), false, nil
	case "schedule":
		return m.cmdSchedule(args), false, nil
	case "model":
		return m.cmdModel(), false, nil
	case "skills":
		return m.cmdSkills(), false, nil
	case "mcp":
		return m.cmdMCP(), false, nil
	case "lsp":
		return m.cmdLSP(), false, nil
	case "plugin":
		return m.cmdPlugin(args), false, nil
	case "hooks":
		return m.cmdHooks(), false, nil
	case "theme":
		return m.cmdTheme(args), false, nil
	case "reload":
		return m.cmdReload(), false, nil
	case "new":
		return m.cmdNew(args)
	case "about":
		return m.cmdAbout(), false, nil
	case "permissions":
		return m.cmdPermissions(args), false, nil
	case "code":
		return m.cmdCode(args), false, nil
	case "facts":
		return m.cmdFacts(args), false, nil
	case "cwd":
		return m.cmdCwd(args), false, nil
	case "workspace", "ws":
		return m.cmdWorkspace(args), false, nil
	default:
		return fmt.Sprintf("unknown command /%s — try /stats /tools /agents /schedule /model /skills /mcp /lsp /plugin /hooks /theme /reload /new /about /permissions /code /facts /cwd /workspace", name), false, nil
	}
}

// cmdCode opens code from the most recent assistant reply in the user's pager
// (or $EDITOR) so it can be selected, copied, or edited with the terminal's
// native mouse — bubbletea's mouse-cell-motion blocks drag-to-select inside
// the TUI. Usage:
//
//	/code           - opens the last fenced code block, or all of them when
//	                  the reply has multiple
//	/code <N>       - opens the N-th code block (1-based)
//	/code all       - opens every code block in one file
func (m *model) cmdCode(args string) string {
	last := m.lastBotEntry()
	if last == nil {
		return "code: no assistant reply yet"
	}
	sub := strings.ToLower(strings.TrimSpace(args))
	// "reply" opens the entire bot reply — useful when the model returned
	// unfenced output (a file listing, ls output, error message etc.) that
	// users still want to copy with the terminal's native mouse selection.
	if sub == "reply" || sub == "last" {
		path, err := writeScratchFile(last.text, "md")
		if err != nil {
			return fmt.Sprintf("code: %v", err)
		}
		bin, opts := resolveCodeViewer()
		if bin == "" {
			return fmt.Sprintf("code: wrote %s — set $PAGER or $EDITOR to open it automatically", path)
		}
		m.pendingExec = teaExecProcess(bin, append(opts, path), "full reply", path)
		return ""
	}
	blocks := extractCodeBlocks(last.text)
	if len(blocks) == 0 {
		return "code: no fenced code blocks in the last reply (try /code reply to open the whole text)"
	}
	var payload string
	var lang string
	var label string
	switch sub {
	case "", "all":
		// Multiple blocks → concatenate with a header per block; single
		// block → just the body. Tag with the first block's language for
		// the file extension.
		if len(blocks) == 1 {
			payload = blocks[0].Code
			lang = blocks[0].Lang
			label = "code block"
		} else {
			var sb strings.Builder
			for i, b := range blocks {
				if i > 0 {
					sb.WriteString("\n\n")
				}
				fmt.Fprintf(&sb, "// --- block %d", i+1)
				if b.Lang != "" {
					fmt.Fprintf(&sb, " (%s)", b.Lang)
				}
				sb.WriteString(" ---\n")
				sb.WriteString(b.Code)
			}
			payload = sb.String()
			lang = blocks[0].Lang
			label = fmt.Sprintf("%d code blocks", len(blocks))
		}
	default:
		var n int
		if _, err := fmt.Sscanf(sub, "%d", &n); err != nil || n < 1 || n > len(blocks) {
			return fmt.Sprintf("code: bad index %q; usage: /code [N|all] (%d block(s) available)", sub, len(blocks))
		}
		payload = blocks[n-1].Code
		lang = blocks[n-1].Lang
		label = fmt.Sprintf("code block %d", n)
	}
	path, err := writeScratchFile(payload, lang)
	if err != nil {
		return fmt.Sprintf("code: %v", err)
	}
	bin, opts := resolveCodeViewer()
	if bin == "" {
		return fmt.Sprintf("code: wrote %s — set $PAGER or $EDITOR to open it automatically", path)
	}
	m.pendingExec = teaExecProcess(bin, append(opts, path), label, path)
	return ""
}

// cmdPermissions toggles or reports the runtime permission-confirm flag.
// Usage: /permissions [on|off|status]. Empty arg = status.
// Bypass at startup via cfg.tools.require_confirm: false in config.yaml.
func (m model) cmdPermissions(args string) string {
	if m.permDialog == nil {
		return "permissions: TUI permission modal not wired (set cfg.tools.require_confirm in config.yaml)"
	}
	sub := strings.ToLower(strings.TrimSpace(args))
	switch sub {
	case "", "status":
		state := "on (tools prompt for confirmation)"
		if !m.permDialog.RequireConfirm() {
			state = "off (all tool calls auto-approved — DANGEROUS)"
		}
		return "permissions: " + state + "\nusage: /permissions on|off|status"
	case "on":
		m.permDialog.SetRequireConfirm(true)
		return "permissions: ON — tools will prompt for confirmation"
	case "off":
		m.permDialog.SetRequireConfirm(false)
		return "permissions: OFF — all tool calls auto-approved (DANGEROUS; equivalent to claude-code --dangerously-skip-permissions)"
	default:
		return "permissions: unknown subcommand " + sub + "; try /permissions on|off|status"
	}
}

// cmdAbout returns the brand mark + version + active theme name.
func (m model) cmdAbout() string {
	return welcomeArt + "\n\ncax v" + Version + "\ntheme: " + theme.Active().Name
}

const newUsage = "usage: /new skill|agent|command [name]"

// cmdNew activates the /new wizard for one of skill|agent|command. Returns
// the prompt text for the first wizard step plus a fresh *creator.Wizard the
// model installs to route subsequent text input through Advance.
func (m *model) cmdNew(args string) (string, bool, *creator.Wizard) {
	fields := tokenizeArgs(args)
	if len(fields) == 0 {
		return newUsage, false, nil
	}
	kind := strings.ToLower(fields[0])
	var wk creator.WizardKind
	switch kind {
	case "skill":
		wk = creator.WizardKindSkill
	case "agent":
		wk = creator.WizardKindAgent
	case "command":
		wk = creator.WizardKindCommand
	default:
		return fmt.Sprintf("unknown kind %q; %s", kind, newUsage), false, nil
	}
	name := ""
	if len(fields) >= 2 {
		name = fields[1]
	}
	w := &creator.Wizard{Kind: wk, Name: name}
	if name == "" {
		w.Step = creator.WizardStepName
	} else {
		w.Step = creator.WizardStepDescription
	}
	prompt := w.Prompt()
	return fmt.Sprintf("/new %s: %s (use /cancel to abort)", kind, prompt), false, w
}

// cmdReload triggers the wired pluginBackend's Rebuild (which re-runs the
// full plugins → skills → mcp → lsp → hooks → assistant.Rebuild chain in
// cmd/cax/main.go). Without a wired backend, returns a usage hint.
func (m model) cmdReload() string {
	if m.plugins == nil {
		return "reload: not available (plugins backend not wired); set plugins.enabled: true in config.yaml"
	}
	if err := m.plugins.Rebuild(context.Background()); err != nil {
		return fmt.Sprintf("reload failed: %v", err)
	}
	return "reloaded: plugins, skills, mcp, lsp, hooks, user commands"
}

// renderHelpOverlay returns the multi-line help text for the Ctrl+/ overlay.
// Lists keybindings and the slash commands the model currently knows about
// (built-in + user-level + plugin-level merged via m.userCommands).
func (m model) renderHelpOverlay() string {
	var b strings.Builder
	b.WriteString("keybindings:\n")
	b.WriteString("  Enter            send\n")
	b.WriteString("  Alt+Enter        newline\n")
	b.WriteString("  Ctrl+L           /model picker\n")
	b.WriteString("  Ctrl+R           /reload\n")
	b.WriteString("  Ctrl+T           cycle theme\n")
	b.WriteString("  Ctrl+/           toggle this overlay\n")
	b.WriteString("  Ctrl+C / Esc     quit\n")
	b.WriteString("  PgUp / PgDn      scroll viewport\n")
	b.WriteString("\nbuilt-in commands:\n")
	b.WriteString("  /stats /tools /agents /schedule /model /skills /mcp /lsp /plugin /hooks /theme /reload /facts /code /quit\n")
	b.WriteString("  /about                             show the brand mark + active theme\n")
	b.WriteString("  /new skill|agent|command [name]   start the creator wizard\n")
	b.WriteString("  /cancel                            cancel the active wizard\n")
	b.WriteString("\nask in natural language; the agent calls these tools:\n")
	b.WriteString("  create_skill   create_agent   create_command\n")
	if len(m.userCommands) > 0 {
		b.WriteString("\nuser + plugin commands:\n")
		for _, c := range m.userCommands {
			label := "/" + c.Name
			if c.ArgumentHint != "" {
				label += " " + c.ArgumentHint
			}
			if c.Description != "" {
				fmt.Fprintf(&b, "  %-32s  %s  [%s]\n", label, c.Description, c.Source)
			} else {
				fmt.Fprintf(&b, "  %-32s  [%s]\n", label, c.Source)
			}
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

func (m model) cmdStats() string {
	if !m.hasStatus {
		return "stats unavailable (no status yet)"
	}
	s := m.status
	day := s.Usage.Day.InputTokens + s.Usage.Day.OutputTokens
	week := s.Usage.Week.InputTokens + s.Usage.Week.OutputTokens
	month := s.Usage.Month.InputTokens + s.Usage.Month.OutputTokens
	var b strings.Builder
	fmt.Fprintf(&b, "model:    %s:%s\n", s.Provider, s.Model)
	fmt.Fprintf(&b, "ctx:      %s/%s (%d%%) — prompt window used; older turns summarized once this fills\n", humanizeTokensTenths(s.ContextTokens), humanizeTokensTenths(s.ContextBudget), pctOf(s.ContextTokens, s.ContextBudget))
	fmt.Fprintf(&b, "tokens:   1d %s · 1w %s · 1m %s\n", humanizeTokens(day), humanizeTokens(week), humanizeTokens(month))
	fmt.Fprintf(&b, "memories: %d vectors · %d messages · db %s on disk\n", s.MemoryCount, s.MessageCount, humanizeBytes(s.MemSizeBytes))
	fmt.Fprintf(&b, "tools:   %d · subagents %d", len(s.ToolNames), len(s.SubagentNames))
	return b.String()
}

func (m model) cmdTools() string {
	if !m.hasStatus || len(m.status.ToolNames) == 0 {
		return "no tools registered"
	}
	return fmt.Sprintf("tools (%d): %s", len(m.status.ToolNames), strings.Join(m.status.ToolNames, ", "))
}

func (m model) cmdAgents() string {
	if !m.hasStatus {
		return "no subagent info yet"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "personas (%d): %s\n", len(m.status.SubagentNames), strings.Join(m.status.SubagentNames, ", "))
	running := m.running
	if len(running) == 0 {
		running = m.status.RunningSubagents
	}
	if len(running) == 0 {
		b.WriteString("running: none")
	} else {
		fmt.Fprintf(&b, "running: %s", strings.Join(running, ", "))
	}
	return b.String()
}

const scheduleUsage = "usage: /schedule <list | add <name> \"<cron>\" \"<prompt>\" [channel] | enable <name> | disable <name> | remove <name>>"

// cmdSchedule is store-backed CRUD over the schedules table. Mutating
// subcommands persist via the injected backend then call Reload so changes take
// effect on the running scheduler. "remove" is a soft-disable (no DeleteSchedule
// in the store contract).
func (m model) cmdSchedule(args string) string {
	if m.sched == nil {
		return "schedule: not available (scheduler not wired); configure schedules in config.yaml"
	}

	fields := tokenizeArgs(args)
	if len(fields) == 0 {
		return scheduleUsage
	}

	ctx := context.Background()
	sub := fields[0]
	rest := fields[1:]

	switch sub {
	case "list", "ls":
		return m.scheduleList(ctx)
	case "add":
		return m.scheduleAdd(ctx, rest)
	case "enable":
		return m.scheduleSetEnabled(ctx, rest, true)
	case "disable":
		return m.scheduleSetEnabled(ctx, rest, false)
	case "remove", "rm", "delete":
		return m.scheduleRemove(ctx, rest)
	default:
		return scheduleUsage
	}
}

func (m model) scheduleList(ctx context.Context) string {
	scheds, err := m.sched.List(ctx)
	if err != nil {
		return fmt.Sprintf("schedule list failed: %v", err)
	}
	if len(scheds) == 0 {
		return "no schedules configured (try /schedule add)"
	}
	sort.Slice(scheds, func(i, j int) bool { return scheds[i].Name < scheds[j].Name })
	var b strings.Builder
	fmt.Fprintf(&b, "schedules (%d):", len(scheds))
	for _, sc := range scheds {
		state := "off"
		if sc.Enabled {
			state = "on"
		}
		ch := sc.Channel
		if ch == "" {
			ch = "cli"
		}
		fmt.Fprintf(&b, "\n  %-12s [%s] %q -> %s  %q", sc.Name, state, sc.Cron, ch, sc.Prompt)
	}
	return b.String()
}

func (m model) scheduleAdd(ctx context.Context, args []string) string {
	if len(args) < 3 {
		return scheduleUsage
	}
	sc := config.ScheduleConfig{
		Name:    args[0],
		Cron:    args[1],
		Prompt:  args[2],
		Channel: "cli",
		Enabled: true,
	}
	if len(args) >= 4 && args[3] != "" {
		sc.Channel = args[3]
	}
	if err := m.sched.Upsert(ctx, sc); err != nil {
		return fmt.Sprintf("schedule add failed: %v", err)
	}
	if err := m.sched.Reload(ctx); err != nil {
		return fmt.Sprintf("schedule %q added but reload failed: %v", sc.Name, err)
	}
	return fmt.Sprintf("schedule %q added (%s -> %s)", sc.Name, sc.Cron, sc.Channel)
}

func (m model) scheduleSetEnabled(ctx context.Context, args []string, enabled bool) string {
	if len(args) < 1 {
		return scheduleUsage
	}
	name := args[0]
	sc, ok, err := m.findSchedule(ctx, name)
	if err != nil {
		return fmt.Sprintf("schedule lookup failed: %v", err)
	}
	if !ok {
		return fmt.Sprintf("no schedule named %q", name)
	}
	sc.Enabled = enabled
	if err := m.sched.Upsert(ctx, sc); err != nil {
		return fmt.Sprintf("schedule update failed: %v", err)
	}
	if err := m.sched.Reload(ctx); err != nil {
		return fmt.Sprintf("schedule %q updated but reload failed: %v", name, err)
	}
	verb := "enabled"
	if !enabled {
		verb = "disabled"
	}
	return fmt.Sprintf("schedule %q %s", name, verb)
}

func (m model) scheduleRemove(ctx context.Context, args []string) string {
	if len(args) < 1 {
		return scheduleUsage
	}
	name := args[0]
	sc, ok, err := m.findSchedule(ctx, name)
	if err != nil {
		return fmt.Sprintf("schedule lookup failed: %v", err)
	}
	if !ok {
		return fmt.Sprintf("no schedule named %q", name)
	}
	sc.Enabled = false
	if err := m.sched.Upsert(ctx, sc); err != nil {
		return fmt.Sprintf("schedule remove failed: %v", err)
	}
	if err := m.sched.Reload(ctx); err != nil {
		return fmt.Sprintf("schedule %q removed but reload failed: %v", name, err)
	}
	return fmt.Sprintf("schedule %q removed (soft-disabled)", name)
}

func (m model) findSchedule(ctx context.Context, name string) (config.ScheduleConfig, bool, error) {
	scheds, err := m.sched.List(ctx)
	if err != nil {
		return config.ScheduleConfig{}, false, err
	}
	for _, sc := range scheds {
		if sc.Name == name {
			return sc, true, nil
		}
	}
	return config.ScheduleConfig{}, false, nil
}

// tokenizeArgs splits a command argument string into fields, honoring
// double-quoted segments so cron expressions and prompts can contain spaces.
func tokenizeArgs(s string) []string {
	var fields []string
	var cur strings.Builder
	inQuote := false
	have := false // whether cur represents a (possibly empty) token

	flush := func() {
		if have {
			fields = append(fields, cur.String())
			cur.Reset()
			have = false
		}
	}

	for _, r := range s {
		switch {
		case r == '"':
			inQuote = !inQuote
			have = true
		case (r == ' ' || r == '\t') && !inQuote:
			flush()
		default:
			cur.WriteRune(r)
			have = true
		}
	}
	flush()
	return fields
}

func (m model) cmdModel() string {
	if !m.hasStatus {
		return "model unavailable (no status yet)"
	}
	s := m.status
	if s.OnFallback {
		return fmt.Sprintf("active: %s:%s (⚠ fallback #%d)", s.Provider, s.Model, s.FallbackIndex)
	}
	return fmt.Sprintf("active: %s:%s (✓ primary)", s.Provider, s.Model)
}

// cmdSkills renders the loaded skill catalog. Plan 7 will let plugins
// contribute extra dirs; that flows in transparently through Status.
func (m model) cmdSkills() string {
	if !m.hasStatus {
		return "skills unavailable (no status yet)"
	}
	s := m.status
	if s.SkillCount == 0 {
		return "no skills loaded (configure skills.dirs in config.yaml)"
	}
	return fmt.Sprintf("skills (%d): %s", s.SkillCount, strings.Join(s.SkillNames, ", "))
}

// cmdLSP renders the configured LSP servers, the union of languages they
// serve, and the per-server state + last error (if any).
func (m model) cmdLSP() string {
	if !m.hasStatus {
		return "lsp unavailable (no status yet)"
	}
	s := m.status
	if s.LSPServerCount == 0 {
		return "no LSP servers configured (add entries under lsp.servers in config.yaml)"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "lsp servers (%d) · languages: %s",
		s.LSPServerCount, strings.Join(s.LSPLanguages, ","))
	for _, srv := range s.LSPServers {
		state := "running"
		if !srv.Running {
			state = "stopped"
		}
		fmt.Fprintf(&b, "\n  %-16s [%s] langs=%s",
			srv.Name, state, strings.Join(srv.Languages, ","))
		if srv.LastError != "" {
			fmt.Fprintf(&b, "  error=%q", srv.LastError)
		}
	}
	return b.String()
}

// cmdMCP renders the configured MCP servers and their connection state.
func (m model) cmdMCP() string {
	if !m.hasStatus {
		return "mcp unavailable (no status yet)"
	}
	s := m.status
	if s.MCPServerCount == 0 {
		return "no mcp servers configured (add entries under mcp.servers in config.yaml)"
	}
	return fmt.Sprintf("mcp servers (%d): %s", s.MCPServerCount, strings.Join(s.MCPServerNames, ", "))
}

func pctOf(n, d int) int {
	if d <= 0 {
		return 0
	}
	p := int(float64(n) / float64(d) * 100)
	if p > 100 {
		return 100
	}
	return p
}

const pluginUsage = "usage: /plugin <list | install <git-url> [name] | enable <name> | disable <name> | remove <name>>"

// cmdPlugin drives the injected pluginBackend. Mutations (install/enable/
// disable/remove) trigger Rebuild so the running agent picks up the new
// Contributions on the next turn. List is a pure query.
func (m model) cmdPlugin(args string) string {
	if m.plugins == nil {
		return "plugin: not available (plugins backend not wired); set plugins.enabled: true and configure plugins.dirs in config.yaml"
	}
	fields := tokenizeArgs(args)
	if len(fields) == 0 {
		return pluginUsage
	}
	ctx := context.Background()
	sub, rest := fields[0], fields[1:]

	switch sub {
	case "list", "ls":
		return m.pluginList(ctx)
	case "install", "add":
		return m.pluginInstall(ctx, rest)
	case "enable":
		return m.pluginSetEnabled(ctx, rest, true)
	case "disable":
		return m.pluginSetEnabled(ctx, rest, false)
	case "remove", "rm", "uninstall":
		return m.pluginRemove(ctx, rest)
	default:
		return pluginUsage
	}
}

func (m model) pluginList(ctx context.Context) string {
	items, err := m.plugins.List(ctx)
	if err != nil {
		return fmt.Sprintf("plugin list failed: %v", err)
	}
	if len(items) == 0 {
		return "no plugins installed (try /plugin install <git-url> [name])"
	}
	sort.Slice(items, func(i, j int) bool { return items[i].Name < items[j].Name })
	var b strings.Builder
	fmt.Fprintf(&b, "plugins (%d):", len(items))
	for _, p := range items {
		state := "off"
		if p.Enabled {
			state = "on"
		}
		ver := p.Version
		if ver == "" {
			ver = "?"
		}
		src := p.Source
		if src == "" {
			src = "local"
		}
		fmt.Fprintf(&b, "\n  %-16s %-8s [%s] %s  (skills %d · mcp %d · lsp %d · hooks %d · cmds %d)",
			p.Name, ver, state, src, p.SkillCount, p.MCPCount, p.LSPCount, p.HookCount, p.CmdCount)
	}
	return b.String()
}

func (m model) pluginInstall(ctx context.Context, args []string) string {
	if len(args) < 1 {
		return pluginUsage
	}
	gitURL := args[0]
	name := ""
	if len(args) >= 2 {
		name = args[1]
	} else {
		name = inferPluginName(gitURL)
	}
	if err := m.plugins.Install(ctx, gitURL, name); err != nil {
		return fmt.Sprintf("plugin install failed: %v", err)
	}
	if err := m.plugins.Rebuild(ctx); err != nil {
		return fmt.Sprintf("plugin %q installed but rebuild failed: %v", name, err)
	}
	return fmt.Sprintf("plugin %q installed from %s", name, gitURL)
}

func (m model) pluginSetEnabled(ctx context.Context, args []string, enabled bool) string {
	if len(args) < 1 {
		return pluginUsage
	}
	name := args[0]
	var err error
	if enabled {
		err = m.plugins.Enable(ctx, name)
	} else {
		err = m.plugins.Disable(ctx, name)
	}
	if err != nil {
		return fmt.Sprintf("plugin update failed: %v", err)
	}
	if err := m.plugins.Rebuild(ctx); err != nil {
		return fmt.Sprintf("plugin %q updated but rebuild failed: %v", name, err)
	}
	verb := "enabled"
	if !enabled {
		verb = "disabled"
	}
	return fmt.Sprintf("plugin %q %s", name, verb)
}

func (m model) pluginRemove(ctx context.Context, args []string) string {
	if len(args) < 1 {
		return pluginUsage
	}
	name := args[0]
	if err := m.plugins.Remove(ctx, name); err != nil {
		return fmt.Sprintf("plugin remove failed: %v", err)
	}
	if err := m.plugins.Rebuild(ctx); err != nil {
		return fmt.Sprintf("plugin %q removed but rebuild failed: %v", name, err)
	}
	return fmt.Sprintf("plugin %q removed", name)
}

// cmdHooks renders the active plugin-declared hook entries: event, matcher
// (tool / command substring), source plugin, and per-entry timeout. Falls
// back to a hint pointing at .claude-plugin/hooks.json when nothing is wired.
func (m model) cmdHooks() string {
	if !m.hasStatus || m.status.HookCount == 0 || len(m.hookEntries) == 0 {
		return "no hooks registered (configure via plugin .claude-plugin/hooks.json)"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "hooks (%d):", len(m.hookEntries))
	for _, e := range m.hookEntries {
		matcher := "*"
		switch {
		case e.Matcher.Tool != "" && e.Matcher.Command != "":
			matcher = fmt.Sprintf("tool=%s command~=%q", e.Matcher.Tool, e.Matcher.Command)
		case e.Matcher.Tool != "":
			matcher = fmt.Sprintf("tool=%s", e.Matcher.Tool)
		case e.Matcher.Command != "":
			matcher = fmt.Sprintf("command~=%q", e.Matcher.Command)
		}
		timeout := e.TimeoutSeconds
		if timeout <= 0 {
			timeout = 5
		}
		source := e.Source
		if source == "" {
			source = "user"
		}
		fmt.Fprintf(&b, "\n  %-16s %-32s [%s] %ds", string(e.Event), matcher, source, timeout)
	}
	return b.String()
}

// cmdTheme handles "/theme list" and "/theme <name>". Setting persists the
// choice to ~/.cax/state.json and the next refreshViewport call applies
// the new look.
func (m *model) cmdTheme(args string) string {
	args = strings.TrimSpace(args)
	if args == "" || args == "list" || args == "ls" {
		names := theme.List()
		if len(names) == 0 {
			return "no themes registered"
		}
		active := ""
		if a := theme.Active(); a != nil {
			active = a.Name
		}
		var b strings.Builder
		fmt.Fprintf(&b, "themes (%d):", len(names))
		for _, n := range names {
			marker := "  "
			if n == active {
				marker = "* "
			}
			fmt.Fprintf(&b, "\n  %s%s", marker, n)
		}
		return b.String()
	}
	t, err := theme.Get(args)
	if err != nil {
		return fmt.Sprintf("theme %q not found (try /theme list)", args)
	}
	theme.Set(t)
	if path := theme.StateFile(); path != "" {
		if err := theme.WriteActive(path); err != nil {
			slog.Warn("theme: persist state", "err", err)
		}
	}
	m.refreshViewport()
	return fmt.Sprintf("theme set to %q", t.Name)
}

// inferPluginName extracts a sensible default from a git URL: the last path
// segment minus a trailing ".git".
func inferPluginName(gitURL string) string {
	s := gitURL
	if i := strings.LastIndex(s, "/"); i >= 0 {
		s = s[i+1:]
	}
	s = strings.TrimSuffix(s, ".git")
	if s == "" {
		return "plugin"
	}
	return s
}

// cmdFacts inspects the mem0-style fact store. Subcommands:
//
//	/facts            — list all live facts for the current session (newest first)
//	/facts <query>    — semantic search; returns top-K closest facts
//	/facts clear      — soft-delete every fact for the session
//
// Requires factsBackend (wired by main.go over memory.Store). When unset
// (e.g. memory.mode left at snippets and the backend was opted out), the
// command reports that and exits cleanly.
func (m model) cmdFacts(args string) string {
	if m.facts == nil {
		return "facts: not wired — set memory.mode: facts (or both) in ~/.cax/config.yaml"
	}
	ctx, cancel := contextWithTimeout(3 * time.Second)
	defer cancel()
	sid := m.sessionID()
	args = strings.TrimSpace(args)

	switch {
	case args == "":
		hits, err := m.facts.List(ctx, sid, 50)
		if err != nil {
			return fmt.Sprintf("facts: list failed: %s", err.Error())
		}
		return renderFactList(hits, "all facts for this session")
	case args == "clear":
		n, err := m.facts.Clear(ctx, sid)
		if err != nil {
			return fmt.Sprintf("facts: clear failed: %s", err.Error())
		}
		return fmt.Sprintf("facts: cleared %d facts", n)
	default:
		hits, err := m.facts.Search(ctx, sid, args, 10)
		if err != nil {
			return fmt.Sprintf("facts: search failed: %s", err.Error())
		}
		return renderFactList(hits, fmt.Sprintf("facts matching %q", args))
	}
}

func renderFactList(hits []FactDisplay, header string) string {
	if len(hits) == 0 {
		return header + " — none"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s (%d):\n", header, len(hits))
	for _, h := range hits {
		kind := h.Kind
		if kind == "" {
			kind = "·"
		}
		fmt.Fprintf(&b, "  [%d] %s  (%s, %s)\n", h.ID, h.Text, kind, h.UpdatedAt.Format("2006-01-02"))
	}
	return strings.TrimRight(b.String(), "\n")
}

// sessionID is the CLI's outbound session id — currently the same constant
// the CLI hands to every channel.Message. Kept as a method so future
// per-tab / per-window scoping can land in one place.
func (m model) sessionID() string { return "cli" }

// contextWithTimeout is a tiny helper so cmd handlers don't need to repeat
// the boilerplate. Always pair with defer cancel().
func contextWithTimeout(d time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), d)
}

// cmdCwd manages the project-root override read by the agent on every turn.
// Subcommands:
//
//	/cwd            — show the current resolved project root (override or auto)
//	/cwd <path>     — pin the active project root (sticky until cleared)
//	/cwd clear      — drop the override; auto-detection resumes
//
// The agent uses this for code_search and any other hook that needs to know
// "what project is the user currently working in?". Auto-detection (walk-up
// from launch CWD + path-in-query inference) covers most cases; /cwd is the
// explicit escape hatch.
func (m model) cmdCwd(args string) string {
	if m.projectRoot == nil {
		return "cwd: project-root resolver not wired"
	}
	args = strings.TrimSpace(args)
	switch args {
	case "":
		// Show current state: override (if any) + auto-resolved fallback.
		override := m.projectRoot.Override()
		auto := m.projectRoot.For("") // no query → walk-up only
		var b strings.Builder
		if override != "" {
			fmt.Fprintf(&b, "cwd: override → %s\n", override)
		} else {
			b.WriteString("cwd: no override set\n")
		}
		if auto != "" {
			fmt.Fprintf(&b, "auto-detected from launch CWD → %s", auto)
		} else {
			b.WriteString("auto-detection found nothing")
		}
		return b.String()
	case "clear", "off", "unset":
		m.projectRoot.ClearOverride()
		return "cwd: override cleared; auto-detection resumed"
	default:
		clean, err := m.projectRoot.SetOverride(args)
		if err != nil {
			return fmt.Sprintf("cwd: %s", err.Error())
		}
		return "cwd: pinned to " + clean
	}
}

// cmdWorkspace manages the project workspace the agent uses for code_search
// fan-out. Subcommands:
//
//	/workspace                — list current entries
//	/workspace discover       — preview candidate sibling projects under the .git root
//	/workspace add <path>     — add a project root (path required)
//	/workspace remove <name>  — drop a project by name or path
//
// The list flows directly to the agent's PreGeneration hook: every entry
// here gets searched in parallel on each turn, with results merged and
// re-ranked by score. Use /workspace discover after cax launches inside a
// monorepo to bulk-import its services.
func (m model) cmdWorkspace(args string) string {
	if m.workspace == nil {
		return "workspace: not wired"
	}
	fields := tokenizeArgs(args)
	sub := ""
	if len(fields) > 0 {
		sub = strings.ToLower(fields[0])
	}
	switch sub {
	case "", "list", "ls":
		entries := m.workspace.List()
		if len(entries) == 0 {
			return "workspace: no entries (try /workspace discover or /workspace add <path>)"
		}
		var b strings.Builder
		fmt.Fprintf(&b, "workspace (%d):\n", len(entries))
		nameLen := 0
		for _, e := range entries {
			if len(e.Name) > nameLen {
				nameLen = len(e.Name)
			}
		}
		for _, e := range entries {
			fmt.Fprintf(&b, "  %-*s  %s\n", nameLen, e.Name, e.Path)
		}
		return strings.TrimRight(b.String(), "\n")
	case "discover":
		root, children, err := m.workspace.Discover("")
		if err != nil {
			return fmt.Sprintf("workspace: discover failed: %s", err.Error())
		}
		if root == "" {
			cwd, _ := os.Getwd()
			return "workspace: no parent directory with 2+ projects found above " + cwd + "\n" +
				"if this is a single-project repo, use /workspace add " + cwd + "\n" +
				"otherwise relaunch cax from inside the monorepo / multi-repo parent"
		}
		if len(children) == 0 {
			return "workspace: " + root + " has no child projects (looked for .git/go.mod/package.json/etc)"
		}
		var b strings.Builder
		fmt.Fprintf(&b, "discovered %d candidates under %s:\n", len(children), root)
		for _, c := range children {
			fmt.Fprintf(&b, "  %s  %s\n", c.Name, c.Path)
		}
		b.WriteString("run /workspace add <path> per project, or /workspace add-all to import them all")
		return b.String()
	case "add-all":
		root, children, err := m.workspace.Discover("")
		if err != nil || root == "" {
			return "workspace: discover failed or no .git ancestor"
		}
		added, skipped := 0, 0
		var errs []string
		for _, c := range children {
			if _, err := m.workspace.Add(c.Name, c.Path); err != nil {
				skipped++
				if len(errs) < 3 {
					errs = append(errs, err.Error())
				}
			} else {
				added++
			}
		}
		msg := fmt.Sprintf("workspace: added %d, skipped %d", added, skipped)
		if len(errs) > 0 {
			msg += " (" + strings.Join(errs, "; ") + ")"
		}
		return msg
	case "add":
		if len(fields) < 2 {
			return "usage: /workspace add <path> [name]"
		}
		name := ""
		if len(fields) >= 3 {
			name = fields[2]
		}
		e, err := m.workspace.Add(name, fields[1])
		if err != nil {
			return fmt.Sprintf("workspace: %s", err.Error())
		}
		return fmt.Sprintf("workspace: added %s → %s", e.Name, e.Path)
	case "remove", "rm":
		if len(fields) < 2 {
			return "usage: /workspace remove <name|path>"
		}
		e, err := m.workspace.Remove(fields[1])
		if err != nil {
			return fmt.Sprintf("workspace: %s", err.Error())
		}
		return fmt.Sprintf("workspace: removed %s (%s)", e.Name, e.Path)
	default:
		return "workspace: unknown subcommand " + sub + "; try /workspace [list|discover|add|add-all|remove]"
	}
}
