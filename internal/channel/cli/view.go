package cli

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

const (
	gaugeAmberPct = 0.75
	gaugeRedPct   = 0.90
	gaugeCells    = 8
)

var (
	barStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("250"))
	okStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("42"))  // green
	amberStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("214")) // amber
	redStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("196")) // red
	dimStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("244"))
	youStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("39")).Bold(true)
	botStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("42")).Bold(true)
	sysStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("244")).Italic(true)
	sepStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("240"))
)

// renderTopBar: "claude-opus ✓ │ ctx 6.1k/8k ▓▓▓░ 76% ⚠".
func (m model) renderTopBar() string {
	if !m.hasStatus {
		return barStyle.Width(m.width).Render("connecting…")
	}
	s := m.status

	var modelPart string
	if s.OnFallback {
		modelPart = amberStyle.Render(fmt.Sprintf("%s ⚠ fallback #%d", s.Model, s.FallbackIndex))
	} else {
		modelPart = okStyle.Render(s.Model + " ✓")
	}

	gauge := m.renderGauge(s.ContextTokens, s.ContextBudget)
	line := modelPart + sepStyle.Render(" │ ") + gauge
	return barStyle.Width(m.width).Render(line)
}

// renderGauge: "ctx 6.1k/8k ▓▓▓░ 76% [⚠]" with threshold coloring.
func (m model) renderGauge(tokens, budget int) string {
	pct := 0.0
	if budget > 0 {
		pct = float64(tokens) / float64(budget)
	}
	if pct > 1 {
		pct = 1
	}
	filled := int(pct * gaugeCells)
	if filled > gaugeCells {
		filled = gaugeCells
	}
	bar := strings.Repeat("▓", filled) + strings.Repeat("░", gaugeCells-filled)

	style := okStyle
	warn := ""
	switch {
	case pct >= gaugeRedPct:
		style = redStyle
		warn = " ⚠"
	case pct >= gaugeAmberPct:
		style = amberStyle
		warn = " ⚠"
	}

	pctStr := fmt.Sprintf("%d%%", int(pct*100))
	return fmt.Sprintf("ctx %s/%s %s %s%s",
		humanizeTokensTenths(tokens),
		humanizeTokensTenths(budget),
		style.Render(bar),
		style.Render(pctStr),
		style.Render(warn),
	)
}

// renderBottomBar: "tok 1d124k 1w812k 1m3.2M·mem18MB·🔧8 🤖3 · 📜N · 🔌M · 🧩P".
// Extra counters (skills 📜, MCP 🔌, plugins 🧩) appear only when non-zero so
// the bottom bar stays clean for users who don't use them.
func (m model) renderBottomBar() string {
	if !m.hasStatus {
		return barStyle.Width(m.width).Render("")
	}
	s := m.status
	day := s.Usage.Day.InputTokens + s.Usage.Day.OutputTokens
	week := s.Usage.Week.InputTokens + s.Usage.Week.OutputTokens
	month := s.Usage.Month.InputTokens + s.Usage.Month.OutputTokens

	var extras strings.Builder
	if s.SkillCount > 0 {
		fmt.Fprintf(&extras, " · 📜%d", s.SkillCount)
	}
	if s.MCPServerCount > 0 {
		fmt.Fprintf(&extras, " · 🔌%d", s.MCPServerCount)
	}
	if s.PluginCount > 0 {
		fmt.Fprintf(&extras, " · 🧩%d", s.PluginCount)
	}

	line := fmt.Sprintf("tok 1d%s 1w%s 1m%s·mem%s·🔧%d 🤖%d%s",
		humanizeTokens(day),
		humanizeTokens(week),
		humanizeTokens(month),
		humanizeBytes(s.MemSizeBytes),
		len(s.ToolNames),
		len(s.SubagentNames),
		extras.String(),
	)
	return dimStyle.Width(m.width).Render(line)
}

// renderConversation builds the body string fed to the viewport.
func (m model) renderConversation() string {
	var b strings.Builder
	for _, h := range m.history {
		b.WriteString(renderEntry(h))
		b.WriteByte('\n')
	}
	if m.streaming {
		b.WriteString(botStyle.Render("bot: ") + m.stream)
		b.WriteByte('\n')
	}
	if m.lastErr != "" {
		b.WriteString(redStyle.Render("err: "+m.lastErr) + "\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

func renderEntry(h historyEntry) string {
	switch h.who {
	case "you":
		return youStyle.Render("you: ") + h.text
	case "bot":
		return botStyle.Render("bot: ") + h.text
	default:
		return sysStyle.Render(h.text)
	}
}

// refreshViewport recomputes viewport content and pins to the bottom.
func (m *model) refreshViewport() {
	m.viewport.SetContent(m.renderConversation())
	m.viewport.GotoBottom()
}

func (m model) View() string {
	sep := sepStyle.Render(strings.Repeat("─", m.width))
	return lipgloss.JoinVertical(
		lipgloss.Left,
		m.renderTopBar(),
		sep,
		m.viewport.View(),
		sep,
		m.renderBottomBar(),
		sep,
		m.input.View(),
	)
}

// humanizeTokensTenths renders e.g. 6100 → "6.1k" for the gauge numerator,
// keeping a tenth even above 10k when below the next unit, so "6.1k" matches
// the mockup. Falls back to humanizeTokens for sub-1000 values.
func humanizeTokensTenths(n int) string {
	switch {
	case n < 1000:
		return fmt.Sprintf("%d", n)
	case n < 1_000_000:
		return strings.TrimSuffix(fmt.Sprintf("%.1f", float64(n)/1000), ".0") + "k"
	default:
		return strings.TrimSuffix(fmt.Sprintf("%.1f", float64(n)/1_000_000), ".0") + "M"
	}
}
