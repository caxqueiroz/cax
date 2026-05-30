package cli

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"

	"github.com/caxqueiroz/czcli/internal/theme"
)

const (
	gaugeAmberPct = 0.75
	gaugeRedPct   = 0.90
	gaugeCells    = 8
	leftIndent    = "  " // global 2-space indent
)

// themedStyles is the per-render bag of lipgloss styles built from the active
// theme. Recomputing per frame is cheap and lets /theme switch take effect
// on the very next render without touching package vars.
type themedStyles struct {
	fg, dim, sep, accent             lipgloss.Style
	ok, amber, red                   lipgloss.Style
	user, sys                        lipgloss.Style
	gaugeFilled, gaugeEmpty          lipgloss.Style
	statusLabel, statusValue, marker lipgloss.Style
}

// styles returns the active theme's lipgloss bag. Falls back to a tiny
// safe-defaults bag when no theme has been registered yet (early render
// before LoadBuiltins, e.g. inside a test that forgot to set one).
func styles() themedStyles {
	t := theme.Active()
	if t == nil {
		t = &theme.Theme{
			Foreground: "#e6e6e6", Dim: "#7a7a7a", Separator: "#3a3a3a",
			Accent: "#5fafff", OK: "#5fd787", Amber: "#d7af00", Red: "#ff5f5f",
			UserPrefix: "#5fafff", AssistantText: "#e6e6e6", SysText: "#9a9a9a",
			CodeBG: "#262626", GaugeFilled: "#5fd787", GaugeEmpty: "#3a3a3a",
			Markdown: "auto",
		}
	}
	return themedStyles{
		fg:          lipgloss.NewStyle().Foreground(lipgloss.Color(t.Foreground)),
		dim:         lipgloss.NewStyle().Foreground(lipgloss.Color(t.Dim)),
		sep:         lipgloss.NewStyle().Foreground(lipgloss.Color(t.Separator)),
		accent:      lipgloss.NewStyle().Foreground(lipgloss.Color(t.Accent)),
		ok:          lipgloss.NewStyle().Foreground(lipgloss.Color(t.OK)),
		amber:       lipgloss.NewStyle().Foreground(lipgloss.Color(t.Amber)),
		red:         lipgloss.NewStyle().Foreground(lipgloss.Color(t.Red)),
		user:        lipgloss.NewStyle().Foreground(lipgloss.Color(t.UserPrefix)).Bold(true),
		sys:         lipgloss.NewStyle().Foreground(lipgloss.Color(t.SysText)).Italic(true),
		gaugeFilled: lipgloss.NewStyle().Foreground(lipgloss.Color(t.GaugeFilled)),
		gaugeEmpty:  lipgloss.NewStyle().Foreground(lipgloss.Color(t.GaugeEmpty)),
		statusLabel: lipgloss.NewStyle().Foreground(lipgloss.Color(t.Dim)),
		statusValue: lipgloss.NewStyle().Foreground(lipgloss.Color(t.Foreground)).Bold(true),
		marker:      lipgloss.NewStyle().Foreground(lipgloss.Color(t.Accent)).Bold(true),
	}
}

// renderTopBar: "  opus ✓                  hist 6.1k/8k ▓▓▓░ 76% ⚠".
func (m model) renderTopBar() string {
	s := styles()
	if !m.hasStatus {
		return leftIndent + s.dim.Render("connecting…")
	}
	st := m.status

	var modelPart string
	if st.OnFallback {
		modelPart = s.amber.Render(fmt.Sprintf("%s ⚠ fallback #%d", st.Model, st.FallbackIndex))
	} else {
		modelPart = s.ok.Render(st.Model + " ✓")
	}
	gauge := m.renderGauge(s, st.ContextTokens, st.ContextBudget)
	return leftIndent + modelPart + "   " + gauge
}

// renderGauge renders the in-memory history budget gauge (NOT the model's
// context window). Style switches between OK/amber/red at thresholds.
func (m model) renderGauge(s themedStyles, tokens, budget int) string {
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
	bar := s.gaugeFilled.Render(strings.Repeat("▓", filled)) +
		s.gaugeEmpty.Render(strings.Repeat("░", gaugeCells-filled))

	chip := s.ok
	warn := ""
	switch {
	case pct >= gaugeRedPct:
		chip = s.red
		warn = " ⚠"
	case pct >= gaugeAmberPct:
		chip = s.amber
		warn = " ⚠"
	}
	return fmt.Sprintf("hist %s/%s %s %s%s",
		humanizeTokensTenths(tokens),
		humanizeTokensTenths(budget),
		bar,
		chip.Render(fmt.Sprintf("%d%%", int(pct*100))),
		chip.Render(warn),
	)
}

// renderBottomBar lays out the status row with accent window markers and
// bold values. No background band — the row inherits the terminal bg so
// both light and dark terminals look intentional.
func (m model) renderBottomBar() string {
	s := styles()
	if !m.hasStatus {
		return leftIndent + s.dim.Render("")
	}
	st := m.status
	day := st.Usage.Day.InputTokens + st.Usage.Day.OutputTokens
	week := st.Usage.Week.InputTokens + st.Usage.Week.OutputTokens
	month := st.Usage.Month.InputTokens + st.Usage.Month.OutputTokens

	div := s.dim.Render("  │  ")
	kv := func(marker, val string) string {
		return s.marker.Render(marker) + " " + s.statusValue.Render(val)
	}
	tagged := func(label, val string) string {
		return s.statusLabel.Render(label) + " " + s.statusValue.Render(val)
	}
	emo := func(icon string, n int) string {
		return s.statusLabel.Render(icon) + " " + s.statusValue.Render(fmt.Sprintf("%d", n))
	}

	tok := strings.Join([]string{
		kv("1d", humanizeTokens(day)),
		kv("1w", humanizeTokens(week)),
		kv("1m", humanizeTokens(month)),
	}, "  ")
	mem := tagged("mem", humanizeBytes(st.MemSizeBytes))
	tools := emo("🔧", len(st.ToolNames)) + "  " + emo("🤖", len(st.SubagentNames))

	extras := ""
	add := func(icon string, n int) {
		if n > 0 {
			extras += div + emo(icon, n)
		}
	}
	add("📜", st.SkillCount)
	add("🔌", st.MCPServerCount)
	add("🧩", st.PluginCount)
	add("🧠", st.LSPServerCount)
	add("⚓", st.HookCount)

	return leftIndent + tok + div + mem + div + tools + extras
}

// renderConversation builds the body for the viewport. Assistant entries
// are rendered as markdown via glamour; user/sys entries stay raw with
// theme-driven prefixes. Spinner/working… is unchanged.
func (m model) renderConversation() string {
	s := styles()
	w := m.viewport.Width
	if w <= 0 {
		w = m.width
	}
	if w < 4 {
		w = 4
	}
	innerWidth := w - len(leftIndent)
	if innerWidth < 1 {
		innerWidth = 1
	}
	wrap := lipgloss.NewStyle().Width(innerWidth)
	wrapErr := s.red.Width(innerWidth)
	indent := func(text string) string {
		var b strings.Builder
		for i, line := range strings.Split(text, "\n") {
			if i > 0 {
				b.WriteByte('\n')
			}
			b.WriteString(leftIndent)
			b.WriteString(line)
		}
		return b.String()
	}

	var b strings.Builder
	for _, h := range m.history {
		switch h.who {
		case "you":
			b.WriteString(indent(wrap.Render(s.user.Render("❯ ") + h.text)))
		case "bot":
			b.WriteString(indent(strings.TrimRight(RenderMarkdown(h.text, innerWidth), "\n")))
		default:
			b.WriteString(indent(wrap.Render(s.sys.Render(h.text))))
		}
		b.WriteByte('\n')
		if h.who == "you" || h.who == "bot" {
			b.WriteByte('\n')
		}
	}
	if m.streaming {
		if m.stream == "" {
			b.WriteString(leftIndent + s.dim.Render(m.spinner.View()+" working…"))
		} else {
			b.WriteString(indent(wrap.Render(m.stream)))
		}
		b.WriteByte('\n')
	}
	if m.lastErr != "" {
		b.WriteString(indent(wrapErr.Render("✗ " + m.lastErr)))
		b.WriteByte('\n')
	}
	return strings.TrimRight(b.String(), "\n")
}

// refreshViewport recomputes content and pins to the bottom.
func (m *model) refreshViewport() {
	m.viewport.SetContent(m.renderConversation())
	m.viewport.GotoBottom()
}

// View composes top bar / sep / viewport / sep / bottom bar / sep / input.
// When helpOpen is true an overlay is rendered between the top bar and the
// viewport listing keybindings + current slash commands.
func (m model) View() string {
	s := styles()
	sep := s.sep.Render(strings.Repeat("─", m.width))
	inputBlock := lipgloss.JoinVertical(
		lipgloss.Left,
		"",
		leftIndent+m.input.View(),
	)
	if m.helpOpen {
		overlay := indentBlock(s.dim.Render(m.renderHelpOverlay()))
		return lipgloss.JoinVertical(
			lipgloss.Left,
			m.renderTopBar(),
			sep,
			overlay,
			sep,
			m.viewport.View(),
			sep,
			m.renderBottomBar(),
			sep,
			inputBlock,
		)
	}
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

// indentBlock prefixes every line of text with the standard left indent.
func indentBlock(text string) string {
	var b strings.Builder
	for i, line := range strings.Split(text, "\n") {
		if i > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(leftIndent)
		b.WriteString(line)
	}
	return b.String()
}

// humanizeTokensTenths renders e.g. 6100 → "6.1k" for the gauge numerator.
// Sub-1000 values fall back to humanizeTokens.
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
