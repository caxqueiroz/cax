package cli

import (
	"fmt"
	"strings"
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
	default:
		return fmt.Sprintf("unknown command /%s — try /stats /tools /agents /schedule /model", name), false
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

// cmdSchedule is a read-only seam. Schedule CRUD is owned by Plan 5 (the
// scheduler plus store-backed persistence); the TUI prints guidance here so the
// command exists without faking scheduling. Plan 5 replaces this body with a
// store-backed lister/CRUD handler.
func (m model) cmdSchedule(args string) string {
	if args == "" {
		return "schedule: not yet available in the TUI — configure schedules in config.yaml; live CRUD lands with Plan 5 (try /schedule list)"
	}
	return fmt.Sprintf("schedule %q: not yet wired in the TUI (see Plan 5)", args)
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
