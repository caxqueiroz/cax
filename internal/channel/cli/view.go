package cli

import (
	"fmt"
	"os"
	"strings"

	"github.com/charmbracelet/lipgloss"

	"github.com/caxqueiroz/cax/internal/theme"
)

const (
	gaugeAmberPct = 0.75
	gaugeRedPct   = 0.90
	gaugeCells    = 8
	leftIndent    = "  " // global 2-space indent

	// minFullHeight is the minimum terminal height that lets every boxed
	// region render readably (header 3 + blank 1 + conv min 4 + blank 1 +
	// status 1 + blank 1 + message 3 = 14). Below this, we begin dropping
	// regions; below minBoxedHeight we fall back to the plain-line layout.
	minBoxedHeight = 9
	minHintHeight  = 14
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

// borderWithTitle wraps content in a rounded box whose top border carries
// a label, like `╭─ conversation ────────────╮`. The bottom/left/right are
// the standard rounded border; the top is composed manually from the
// rounded-border runes so the title sits at column 3 of the top run.
//
// width is the total outer width of the box (border inclusive). The caller
// is responsible for sizing inner content to width - 2 (border) - 2*padX
// (lipgloss padding) before passing it in — or via the returned box style's
// padding settings, which this helper applies internally.
func borderWithTitle(content, title string, width int, color lipgloss.Color) string {
	if width < 4 {
		width = 4
	}
	rb := lipgloss.RoundedBorder()
	// Body: rounded border with the top side disabled. Padding inside the
	// box is handled by the caller — we want the helper to compose just the
	// labeled border, so it composes cleanly with viewports/textareas of
	// known inner widths.
	body := lipgloss.NewStyle().
		Border(rb).
		BorderTop(false).
		BorderForeground(color).
		Width(width - 2). // -2 to account for left/right border cells
		Render(content)

	// Top: `╭` + `─ <title> ` + fill `─` + `╮`. When title is empty, render
	// a plain top line: `╭` + `─×(width-2)` + `╮`.
	top := composeTitledTop(title, width, rb, color)
	return top + "\n" + body
}

// composeTitledTop builds the labeled top border line. It is exported via
// borderWithTitle but kept separate so view_test.go can hit the title splice
// logic directly without rendering a full box.
func composeTitledTop(title string, width int, rb lipgloss.Border, color lipgloss.Color) string {
	if width < 4 {
		width = 4
	}
	borderStyle := lipgloss.NewStyle().Foreground(color)
	inner := width - 2 // space between corner runes
	var middle string
	if strings.TrimSpace(title) == "" {
		middle = strings.Repeat(rb.Top, inner)
	} else {
		label := " " + strings.TrimSpace(title) + " "
		// "─ <title> " starts at col 2 of the top run (one Top rune of
		// padding before the label). Total runes used by `─` + label = 1 +
		// len(label). The remainder fills with `─`.
		lead := rb.Top + label
		leadLen := lipgloss.Width(lead)
		if leadLen >= inner {
			// Title too long for this width: truncate to fit, keep the
			// leading rune so the box still reads as a label.
			middle = lead
			if leadLen > inner {
				// Best-effort: drop trailing runes off the label.
				runes := []rune(lead)
				if inner < len(runes) {
					middle = string(runes[:inner])
				}
			}
		} else {
			middle = lead + strings.Repeat(rb.Top, inner-leadLen)
		}
	}
	return borderStyle.Render(rb.TopLeft + middle + rb.TopRight)
}

// renderHeader composes the branded header box: `◆ cax` on the left,
// dim `personal AI assistant` tagline centered, and the active theme name
// accented on the right. Width is the full terminal width (the box's outer
// box matches width - 4 to honor the global 2-space indent).
func (m model) renderHeader(width int) string {
	s := styles()
	col := borderColor()

	boxOuter := width - 2*len(leftIndent)
	if boxOuter < 16 {
		boxOuter = 16
	}
	inner := boxOuter - 2 - 2 // -2 border, -2 padding (1 each side)
	if inner < 1 {
		inner = 1
	}

	left := s.accent.Render("◆") + " " + s.fg.Bold(true).Render("cax")
	right := s.accent.Render(theme.Active().Name)
	leftW := lipgloss.Width(left)
	rightW := lipgloss.Width(right)
	mid := "personal AI assistant"
	midW := inner - leftW - rightW
	if midW < 1 {
		midW = 1
	}
	midRendered := lipgloss.NewStyle().
		Foreground(lipgloss.Color(theme.Active().Dim)).
		Width(midW).
		Align(lipgloss.Center).
		Render(mid)

	row := lipgloss.JoinHorizontal(lipgloss.Top, left, midRendered, right)
	// Pad row to inner width so the right edge sits flush against the border.
	row = lipgloss.NewStyle().Width(inner).Render(row)
	// Apply horizontal padding by composing a one-cell space on each side.
	padded := " " + row + " "
	return indentBlock(borderWithTitle(padded, "", boxOuter, col))
}

// renderConversationBox wraps the viewport content in a labeled rounded box
// titled `conversation`. width is total terminal width; height is the
// number of rows allocated to the box (border + padding + content).
func (m model) renderConversationBox(width, height int) string {
	col := borderColor()
	boxOuter := width - 2*len(leftIndent)
	if boxOuter < 16 {
		boxOuter = 16
	}
	if height < 4 {
		height = 4
	}
	// Inner content lines = height - 2 (top + bottom border) - 2 (top+bottom padding row).
	// Padding(1,2) costs 1 row top + 1 row bottom + 2 cols left + 2 cols right.
	innerH := height - 2 - 2
	if innerH < 1 {
		innerH = 1
	}
	innerW := boxOuter - 2 - 4 // -2 border, -4 padding (2 each side)
	if innerW < 1 {
		innerW = 1
	}

	// The viewport's own width/height already reflects the inner area
	// (sized in WindowSizeMsg). Render it directly; padding adds margin.
	body := m.viewport.View()
	// Force the body to occupy exactly the inner area so the box bottom
	// border always lines up regardless of how many history rows there are.
	// MaxHeight pins the upper bound — Height alone only pads, it never
	// truncates, so a viewport sized larger than the box would overflow.
	body = lipgloss.NewStyle().
		Width(innerW).
		Height(innerH).
		MaxHeight(innerH).
		MaxWidth(innerW).
		Render(body)
	padded := lipgloss.NewStyle().Padding(1, 2).Render(body)
	return indentBlock(borderWithTitle(padded, "conversation", boxOuter, col))
}

// renderMessageBox wraps the textarea in a labeled rounded box titled
// `message`. Width is total terminal width; height grows with the textarea
// (1..6 inner rows + 2 border).
func (m model) renderMessageBox(width int) string {
	col := borderColor()
	boxOuter := width - 2*len(leftIndent)
	if boxOuter < 16 {
		boxOuter = 16
	}
	innerW := boxOuter - 2 - 2 // -2 border, -2 padding (1 each side)
	if innerW < 1 {
		innerW = 1
	}
	// Strip the trailing newline bubbles' textarea appends to each row — it
	// would otherwise round up to an extra visual row inside the box.
	body := strings.TrimRight(m.input.View(), "\n")
	// Bubbles' placeholder rendering pads the last line with spaces past
	// innerW, which lipgloss .Width then soft-wraps into an extra visual
	// row. Trim trailing spaces line-by-line to prevent the wrap.
	body = trimTrailingPerLine(body)
	// Pin width AND height to the textarea's logical size so the box bottom
	// border lands consistently. MaxHeight protects against any rogue line
	// that still wraps so the layout math never overflows.
	body = lipgloss.NewStyle().
		Width(innerW).
		Height(m.input.Height()).
		MaxHeight(m.input.Height()).
		Render(body)
	padded := lipgloss.NewStyle().Padding(0, 1).Render(body)
	return indentBlock(borderWithTitle(padded, "message", boxOuter, col))
}

// renderHintLine is the dim one-liner under the message box that reminds
// users of the two most-discoverable keybindings. Two-space indented to
// match the global indent rule.
func (m model) renderHintLine() string {
	s := styles()
	return leftIndent + s.dim.Render("ctrl+/ help  ·  ctrl+t theme")
}

// renderStatusRow is the single-line status row between the conversation
// and message boxes. Format: `<model marker>   <dot indicators> buffer <pct>%   1d <tokens> · mem <bytes> · 🔧 <tools>` + extras.
func (m model) renderStatusRow(_ int) string {
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

	dots, pct := renderBufferDots(s, st.ContextTokens, st.ContextBudget)

	day := st.Usage.Day.InputTokens + st.Usage.Day.OutputTokens

	mid := s.dim.Render(" · ")
	tokens := s.statusLabel.Render("1d") + " " + s.statusValue.Render(humanizeTokens(day))
	// "mem" is the agent's growing long-term memory: the count of semantic
	// vectors in the recall store (each turn embeds a chunk). Context-window
	// usage is shown separately by the buffer dots + percentage.
	mem := s.statusLabel.Render("mem") + " " + s.statusValue.Render(humanizeTokens(st.MemoryCount))
	cwd := s.statusLabel.Render("cwd") + " " + s.statusValue.Render(displayCWD(st.CWD, 24))
	tools := s.statusLabel.Render("🔧") + " " + s.statusValue.Render(fmt.Sprintf("%d", len(st.ToolNames)))

	extras := ""
	add := func(icon string, n int) {
		if n > 0 {
			extras += mid + s.statusLabel.Render(icon) + " " + s.statusValue.Render(fmt.Sprintf("%d", n))
		}
	}
	add("📜", st.SkillCount)
	add("🔌", st.MCPServerCount)
	add("🧩", st.PluginCount)
	add("🧠", st.LSPServerCount)
	add("⚓", st.HookCount)

	bufferLabel := s.statusLabel.Render("buffer") + " " + s.statusValue.Render(fmt.Sprintf("%d%%", pct))
	gap := "   "
	return leftIndent + modelPart + gap + dots + " " + bufferLabel + gap + tokens + mid + mem + mid + cwd + mid + tools + extras
}

// displayCWD formats a cwd path for the status row: HOME compressed to "~",
// and (if too long) left-truncated with an ellipsis prefix so the trailing
// components remain visible (those are the bits the user cares about).
// Returns "?" when path is empty.
func displayCWD(path string, max int) string {
	if path == "" {
		return "?"
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		switch {
		case path == home:
			path = "~"
		case strings.HasPrefix(path, home+string(os.PathSeparator)):
			path = "~" + path[len(home):]
		}
	}
	if max <= 1 || len([]rune(path)) <= max {
		return path
	}
	runes := []rune(path)
	// Keep the trailing components; prefix with "…".
	return "…" + string(runes[len(runes)-(max-1):])
}

// renderBufferDots draws the 8-cell dot indicator with threshold coloring.
// Returns the rendered dots string and the integer percentage (0..100).
func renderBufferDots(s themedStyles, tokens, budget int) (string, int) {
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

	chip := s.accent
	switch {
	case pct >= gaugeRedPct:
		chip = s.red
	case pct >= gaugeAmberPct:
		chip = s.amber
	}
	dots := chip.Render(strings.Repeat("●", filled)) + s.gaugeEmpty.Render(strings.Repeat("○", gaugeCells-filled))
	return dots, int(pct * 100)
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
	innerWidth := w
	if innerWidth < 1 {
		innerWidth = 1
	}
	wrap := lipgloss.NewStyle().Width(innerWidth)
	wrapErr := s.red.Width(innerWidth)

	var b strings.Builder
	for _, h := range m.history {
		switch h.who {
		case "you":
			b.WriteString(wrap.Render(s.user.Render("❯ ") + h.text))
		case "bot":
			b.WriteString(strings.TrimRight(RenderMarkdown(h.text, innerWidth), "\n"))
		default:
			b.WriteString(wrap.Render(s.sys.Render(h.text)))
		}
		b.WriteByte('\n')
		if h.who == "you" || h.who == "bot" {
			b.WriteByte('\n')
		}
	}
	if m.streaming {
		if m.stream == "" {
			b.WriteString(s.dim.Render(m.spinner.View() + " working…"))
		} else {
			b.WriteString(wrap.Render(m.stream))
		}
		b.WriteByte('\n')
	}
	if m.lastErr != "" {
		b.WriteString(wrapErr.Render("✗ " + m.lastErr))
		b.WriteByte('\n')
	}
	if m.pendingPerm != nil {
		// Banner: amber heading, command shown indented, keybind hint dim.
		banner := s.amber.Bold(true).Render("⚠ permission required: " + m.pendingPerm.title)
		body := ""
		if m.pendingPerm.message != "" {
			body = "\n" + wrap.Render("  "+m.pendingPerm.message)
		}
		hint := "\n" + s.dim.Render("press y or Enter to allow · n or Esc to deny")
		b.WriteString(banner + body + hint)
		b.WriteByte('\n')
	}
	return strings.TrimRight(b.String(), "\n")
}

// refreshViewport recomputes content and pins to the bottom.
func (m *model) refreshViewport() {
	m.viewport.SetContent(m.renderConversation())
	m.viewport.GotoBottom()
}

// View composes the v2 layout: header box → blank → conversation box →
// blank → status row → blank → message box → hint line. Falls back to a
// plain-line layout when terminal height is too small.
func (m model) View() string {
	if m.width <= 0 || m.height <= 0 {
		// Pre-WindowSizeMsg: render nothing visual yet.
		return ""
	}
	if m.height < minBoxedHeight {
		return m.viewFallback()
	}

	width := m.width
	header := m.renderHeader(width)
	status := m.renderStatusRow(width)
	message := m.renderMessageBox(width)
	hint := ""
	if m.height >= minHintHeight {
		hint = m.renderHintLine()
	}

	// Convo box height = total - header(3) - blank(1) - blank(1) -
	// status(1) - blank(1) - message(input.Height()+2) - hint(0 or 1).
	msgH := m.input.Height() + 2
	used := 3 + 1 + 1 + 1 + 1 + msgH
	if hint != "" {
		used++
	}

	// Welcome card sits between header and conversation on a fresh session
	// (no history, not streaming). Art is 5 rows + 2 border + 1 trailing
	// blank = 8 rows reserved.
	showWelcome := len(m.history) == 0 && !m.streaming
	const welcomeRows = 5 + 2 + 1
	if showWelcome {
		used += welcomeRows
	}

	convH := m.height - used
	if convH < 4 {
		convH = 4
	}
	conv := m.renderConversationBox(width, convH)

	parts := []string{header, "", conv, "", status, "", message}
	if hint != "" {
		parts = append(parts, hint)
	}

	if showWelcome {
		welcome := m.renderWelcomeBlock(width)
		parts = []string{header, "", welcome, "", conv, "", status, "", message}
		if hint != "" {
			parts = append(parts, hint)
		}
	}

	if m.helpOpen {
		overlay := indentBlock(styles().dim.Render(m.renderHelpOverlay()))
		// Insert the overlay between header and conversation so it doesn't
		// blow out the layout math; the conv box still renders below.
		return lipgloss.JoinVertical(lipgloss.Left,
			header, "", overlay, "", conv, "", status, "", message, hint)
	}
	return lipgloss.JoinVertical(lipgloss.Left, parts...)
}

// viewFallback is the minimal layout for terminals shorter than
// minBoxedHeight. It mirrors the pre-v2 plain-line layout so cax stays
// usable on tiny SSH/tmux panes.
func (m model) viewFallback() string {
	s := styles()
	sep := s.sep.Render(strings.Repeat("─", m.width))
	header := leftIndent + s.accent.Render("◆") + " " + s.fg.Bold(true).Render("cax")
	inputBlock := leftIndent + m.input.View()
	return lipgloss.JoinVertical(lipgloss.Left,
		header,
		sep,
		m.viewport.View(),
		sep,
		m.renderStatusRow(m.width),
		sep,
		inputBlock,
	)
}

// borderColor returns the active theme's separator color for use as a
// box border. Falls back to a safe gray when no theme is registered yet.
func borderColor() lipgloss.Color {
	t := theme.Active()
	if t == nil {
		return lipgloss.Color("#3a3a3a")
	}
	return lipgloss.Color(t.Separator)
}

// trimTrailingPerLine strips trailing spaces from each line. Used to undo
// the right-pad bubbles' textarea applies to placeholder rows so lipgloss
// doesn't soft-wrap a padded line into a second visual row.
func trimTrailingPerLine(text string) string {
	lines := strings.Split(text, "\n")
	for i, l := range lines {
		lines[i] = strings.TrimRight(l, " ")
	}
	return strings.Join(lines, "\n")
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
