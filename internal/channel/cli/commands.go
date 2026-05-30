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
func (m model) handleCommand(line string) (string, bool) {
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
	default:
		return fmt.Sprintf("unknown command /%s — try /stats /tools /agents /schedule /model /skills /mcp", name), false
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
	fmt.Fprintf(&b, "context: ctx %s/%s (%d%%)\n", humanizeTokensTenths(s.ContextTokens), humanizeTokensTenths(s.ContextBudget), pctOf(s.ContextTokens, s.ContextBudget))
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
