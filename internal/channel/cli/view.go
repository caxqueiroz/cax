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
	sysStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("244")).Italic(true)
	sepStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("240"))

	// Bottom status bar: dark band, bold throughout, distinct colors per piece.
	bottomBarBg   = lipgloss.NewStyle().Background(lipgloss.Color("236")).Foreground(lipgloss.Color("252")).Bold(true).Padding(0, 1)
	bottomLabel   = lipgloss.NewStyle().Foreground(lipgloss.Color("244")).Background(lipgloss.Color("236")).Bold(true)
	bottomValue   = lipgloss.NewStyle().Foreground(lipgloss.Color("231")).Background(lipgloss.Color("236")).Bold(true)
	bottomAccent  = lipgloss.NewStyle().Foreground(lipgloss.Color("117")).Background(lipgloss.Color("236")).Bold(true)
	bottomDivider = lipgloss.NewStyle().Foreground(lipgloss.Color("240")).Background(lipgloss.Color("236")).Bold(true)
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

// renderGauge: "hist 6.1k/8k ▓▓▓░ 76% [⚠]" with threshold coloring. This is
// the in-memory history budget that triggers summarization — NOT the model's
// context window. Labeled "hist" so it's not confused with the latter.
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
	return fmt.Sprintf("hist %s/%s %s %s%s",
		humanizeTokensTenths(tokens),
		humanizeTokensTenths(budget),
		style.Render(bar),
		style.Render(pctStr),
		style.Render(warn),
	)
}

// renderBottomBar renders the dashboard band: dark background, bold throughout,
// labels in dim, window markers (1d/1w/1m) in cyan, values in bright white,
// dividers between groups. Extra counters (skills 📜, MCP 🔌, plugins 🧩,
// LSP 🧠, hooks ⚓) appear only when non-zero so the bar stays uncluttered.
func (m model) renderBottomBar() string {
	if !m.hasStatus {
		return bottomBarBg.Width(m.width).Render("")
	}
	s := m.status
	day := s.Usage.Day.InputTokens + s.Usage.Day.OutputTokens
	week := s.Usage.Week.InputTokens + s.Usage.Week.OutputTokens
	month := s.Usage.Month.InputTokens + s.Usage.Month.OutputTokens

	div := bottomDivider.Render("  │  ")
	kv := func(lbl, val string) string {
		return bottomAccent.Render(lbl) + bottomBarBg.Render(" ") + bottomValue.Render(val)
	}
	tagged := func(lbl, val string) string {
		return bottomLabel.Render(lbl) + bottomBarBg.Render(" ") + bottomValue.Render(val)
	}
	emo := func(icon string, n int) string {
		return bottomBarBg.Render(icon+" ") + bottomValue.Render(fmt.Sprintf("%d", n))
	}

	var parts []string
	parts = append(parts,
		bottomLabel.Render("tok"),
		kv("1d", humanizeTokens(day)),
		kv("1w", humanizeTokens(week)),
		kv("1m", humanizeTokens(month)),
	)
	tokGroup := strings.Join(parts, bottomBarBg.Render("  "))

	mem := tagged("mem", humanizeBytes(s.MemSizeBytes))
	tools := emo("🔧", len(s.ToolNames)) + bottomBarBg.Render("  ") + emo("🤖", len(s.SubagentNames))

	extras := ""
	add := func(icon string, n int) {
		if n > 0 {
			extras += div + emo(icon, n)
		}
	}
	add("📜", s.SkillCount)
	add("🔌", s.MCPServerCount)
	add("🧩", s.PluginCount)
	add("🧠", s.LSPServerCount)
	add("⚓", s.HookCount)

	line := tokGroup + div + mem + div + tools + extras
	return bottomBarBg.Width(m.width).Render(line)
}

// renderConversation builds the body string fed to the viewport. All entries
// are wrapped to the viewport width so long lines (esp. error messages) can't
// blow past the bottom bar and shred the layout. Style: user lines carry a
// "❯" prefix in accent; assistant replies have no prefix (so the text reads
// like prose) and are followed by a blank line for breathing room.
func (m model) renderConversation() string {
	w := m.viewport.Width
	if w <= 0 {
		w = m.width
	}
	wrap := lipgloss.NewStyle().Width(w)
	wrapErr := redStyle.Width(w)
	var b strings.Builder
	for _, h := range m.history {
		b.WriteString(wrap.Render(renderEntry(h)))
		b.WriteByte('\n')
		// One blank line after every user/assistant entry gives even
		// breathing room around each turn (user→reply and reply→next user).
		if h.who == "you" || h.who == "bot" {
			b.WriteByte('\n')
		}
	}
	if m.streaming {
		if m.stream == "" {
			// No deltas yet — show the spinner so the user knows we're working.
			b.WriteString(dimStyle.Render(m.spinner.View() + " working…"))
		} else {
			b.WriteString(wrap.Render(m.stream))
		}
		b.WriteByte('\n')
	}
	if m.lastErr != "" {
		b.WriteString(wrapErr.Render("✗ " + m.lastErr))
		b.WriteByte('\n')
	}
	return strings.TrimRight(b.String(), "\n")
}

func renderEntry(h historyEntry) string {
	switch h.who {
	case "you":
		return youStyle.Render("❯ ") + h.text
	case "bot":
		// No prefix: assistant replies read as plain prose blocks.
		return h.text
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
	// Input region: a blank line of padding above gives the textinput
	// breathing room instead of jamming it against the separator.
	inputBlock := lipgloss.JoinVertical(
		lipgloss.Left,
		"",
		m.input.View(),
	)
	return lipgloss.JoinVertical(
		lipgloss.Left,
		m.renderTopBar(),
		sep,
		m.viewport.View(),
		sep,
		m.renderBottomBar(),
		sep,
		inputBlock,
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
