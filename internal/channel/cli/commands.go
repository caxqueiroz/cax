package cli

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/caxqueiroz/czcli/internal/config"
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

// handleCommand dispatches a slash command and returns display output plus a
// quit flag. It reads only the latest status snapshot already in the model.
// Pointer receiver: the /theme handler mutates the active theme registry
// and needs to call m.refreshViewport() so the new look applies immediately.
func (m *model) handleCommand(line string) (string, bool) {
	name, args := parseCommand(line)
	switch name {
	case "quit", "exit":
		return "", true
	case "stats":
		return m.cmdStats(), false
	case "tools":
		return m.cmdTools(), false
	case "agents":
		return m.cmdAgents(), false
	case "schedule":
		return m.cmdSchedule(args), false
	case "model":
		return m.cmdModel(), false
	case "skills":
		return m.cmdSkills(), false
	case "mcp":
		return m.cmdMCP(), false
	case "lsp":
		return m.cmdLSP(), false
	case "plugin":
		return m.cmdPlugin(args), false
	case "hooks":
		return m.cmdHooks(), false
	default:
		return fmt.Sprintf("unknown command /%s — try /stats /tools /agents /schedule /model /skills /mcp /lsp /plugin /hooks", name), false
	}
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
	fmt.Fprintf(&b, "model:   %s:%s\n", s.Provider, s.Model)
	fmt.Fprintf(&b, "history: hist %s/%s (%d%%) — in-memory turn budget; summarized above this\n", humanizeTokensTenths(s.ContextTokens), humanizeTokensTenths(s.ContextBudget), pctOf(s.ContextTokens, s.ContextBudget))
	fmt.Fprintf(&b, "tokens:  1d %s · 1w %s · 1m %s\n", humanizeTokens(day), humanizeTokens(week), humanizeTokens(month))
	fmt.Fprintf(&b, "memory:  mem %s · %d messages · %d vectors\n", humanizeBytes(s.MemSizeBytes), s.MessageCount, s.MemoryCount)
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
