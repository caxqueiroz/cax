package cli

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"

	"github.com/caxqueiroz/cax/internal/tasks"
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
	return borderWithTitleAndSuffix(content, title, "", width, color)
}

// borderWithTitleAndSuffix is borderWithTitle plus a right-aligned suffix
// embedded into the top border (e.g. status info: model · ctx 23% · ↑↓
// tokens · up 5m). The suffix is plain text; ANSI escapes from theming are
// allowed but width math uses lipgloss.Width so they don't throw off the
// fill calculation.
func borderWithTitleAndSuffix(content, title, suffix string, width int, color lipgloss.Color) string {
	if width < 4 {
		width = 4
	}
	rb := lipgloss.RoundedBorder()
	body := lipgloss.NewStyle().
		Border(rb).
		BorderTop(false).
		BorderForeground(color).
		Width(width - 2).
		Render(content)

	top := composeTitledTopWithSuffix(title, suffix, width, rb, color)
	return top + "\n" + body
}

// composeTitledTop builds the labeled top border line. Kept as a thin
// shim over composeTitledTopWithSuffix for back-compat with tests.
func composeTitledTop(title string, width int, rb lipgloss.Border, color lipgloss.Color) string {
	return composeTitledTopWithSuffix(title, "", width, rb, color)
}

// composeTitledTopWithSuffix builds the labeled top border line with an
// optional right-aligned suffix. Layout:
//
//	╭─ <title> ─────── <suffix> ─╮
//
// The Top rune fills between the label and the suffix so the total run
// width is exactly inner cells. Suffix is right-justified with one Top rune
// of padding before the closing corner so it doesn't kiss the border.
func composeTitledTopWithSuffix(title, suffix string, width int, rb lipgloss.Border, color lipgloss.Color) string {
	if width < 4 {
		width = 4
	}
	borderStyle := lipgloss.NewStyle().Foreground(color)
	inner := width - 2 // space between corner runes
	titleTrim := strings.TrimSpace(title)
	suffTrim := strings.TrimSpace(suffix)
	var middle string
	switch {
	case titleTrim == "" && suffTrim == "":
		middle = strings.Repeat(rb.Top, inner)
	default:
		var lead string
		if titleTrim != "" {
			lead = rb.Top + " " + titleTrim + " "
		}
		var trail string
		if suffTrim != "" {
			// Trail: " <suffix> " then `─` padding rune.
			trail = " " + suffTrim + " " + rb.Top
		}
		leadW := lipgloss.Width(lead)
		trailW := lipgloss.Width(trail)
		if leadW+trailW >= inner {
			// Not enough room — drop the suffix first, then truncate title.
			if leadW >= inner {
				runes := []rune(lead)
				if inner < len(runes) {
					middle = string(runes[:inner])
				} else {
					middle = lead
				}
			} else {
				middle = lead + strings.Repeat(rb.Top, inner-leadW)
			}
		} else {
			fill := strings.Repeat(rb.Top, inner-leadW-trailW)
			middle = lead + fill + trail
		}
	}
	return borderStyle.Render(rb.TopLeft + middle + rb.TopRight)
}

// renderConversationBox wraps the viewport content in a labeled rounded box
// titled `conversation` with the always-on status badges embedded in the
// top-right of the border (model · ctx · ↑↓ tokens · uptime).
func (m model) renderConversationBox(width, height int) string {
	col := borderColor()
	boxOuter := max(width-2*len(leftIndent), 16)
	if height < 4 {
		height = 4
	}
	innerH := max(height-2-2, 1)
	innerW := max(boxOuter-2-4, 1)

	body := m.viewport.View()
	body = lipgloss.NewStyle().
		Width(innerW).
		Height(innerH).
		MaxHeight(innerH).
		MaxWidth(innerW).
		Render(body)
	padded := lipgloss.NewStyle().Padding(1, 2).Render(body)
	suffix := m.renderBorderStatus()
	return indentBlock(borderWithTitleAndSuffix(padded, "conversation", suffix, boxOuter, col))
}

// renderBorderStatus builds the right-aligned status suffix embedded in
// the conversation border: model · ctx N% · ↑in ↓out · up Hms. Empty when
// no status has been delivered yet. Per-element separators are " · " in
// dim style.
func (m model) renderBorderStatus() string {
	if !m.hasStatus {
		return ""
	}
	s := styles()
	st := m.status

	parts := []string{}
	if st.Model != "" {
		parts = append(parts, s.statusValue.Render(st.Model))
	}
	_, pct := renderBufferDots(s, st.ContextTokens, st.ContextBudget)
	parts = append(parts, s.dim.Render("ctx ")+s.statusValue.Render(fmt.Sprintf("%d%%", pct)))
	if m.turnInputTokens > 0 {
		parts = append(parts, s.dim.Render("↑ ")+s.statusValue.Render(fmt.Sprintf("%d", m.turnInputTokens)))
	}
	// Downstream tokens: prefer the streaming buffer mid-turn, but fall back
	// to the most recent bot reply's tokens when we're between turns so the
	// number doesn't blank out the moment a reply lands.
	down := estimateTokens(m.stream)
	if down == 0 {
		if last := m.lastBotEntry(); last != nil {
			down = estimateTokens(last.text)
		}
	}
	if down > 0 {
		parts = append(parts, s.dim.Render("↓ ")+s.statusValue.Render(fmt.Sprintf("%d", down)))
	}
	if !m.sessionStart.IsZero() {
		parts = append(parts, s.dim.Render("up ")+s.statusValue.Render(humanizeDuration(time.Since(m.sessionStart), false)))
	}
	return strings.Join(parts, s.dim.Render(" · "))
}

// renderCompletionDropdown renders a flat list of matches under the message
// box — pi.dev / claude-code style. No border, no title; just rows with a
// `›` marker on the highlighted entry. Capped to 7 visible rows.
func (m model) renderCompletionDropdown(width int) string {
	s := styles()

	matches := m.completion.matches
	visible := min(len(matches), 7)
	start := 0
	if m.completion.idx >= visible {
		start = m.completion.idx - visible + 1
	}
	end := min(start+visible, len(matches))

	const nameCol = 16
	var b strings.Builder
	for i := start; i < end; i++ {
		e := matches[i]
		marker := "  "
		nameStyle := s.dim
		descStyle := s.dim
		if i == m.completion.idx {
			marker = s.accent.Bold(true).Render("› ")
			nameStyle = s.accent.Bold(true)
			descStyle = lipgloss.NewStyle().Foreground(lipgloss.Color(theme.Active().Foreground))
		}
		name := nameStyle.Render("/" + e.name)
		visW := lipgloss.Width(marker) + lipgloss.Width(name)
		pad := max(nameCol-visW, 2)
		row := leftIndent + marker + name + strings.Repeat(" ", pad) + descStyle.Render(e.desc)
		b.WriteString(row)
		if i < end-1 {
			b.WriteByte('\n')
		}
	}
	if visible < len(matches) {
		b.WriteByte('\n')
		b.WriteString(leftIndent + s.dim.Render(fmt.Sprintf("  +%d more · ↑↓ navigate · tab to complete · esc to dismiss", len(matches)-visible)))
	} else {
		b.WriteByte('\n')
		b.WriteString(leftIndent + s.dim.Render("  ↑↓ navigate · tab to complete · esc to dismiss"))
	}
	return b.String()
}

// renderMessageBox wraps the textarea in a labeled rounded box titled
// `message`. Default height is 5 rows. When the input is empty, the
// placeholder hint is rendered on row 3 (1-indexed) — the visual midpoint —
// while the cursor stays on row 1. This decouples "where to look" from
// "where you're typing" and matches Claude Code's spacious input feel.
func (m model) renderMessageBox(width int) string {
	col := borderColor()
	boxOuter := max(width-2*len(leftIndent), 16)
	innerW := max(boxOuter-2-2, 1)
	body := strings.TrimRight(m.input.View(), "\n")
	body = trimTrailingPerLine(body)

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
	// "memories" is the lifetime count of long-term semantic vectors stored
	// at ~/.cax/memory.db. Each turn embeds its assistant reply into a row
	// here; PreGeneration runs a top-K vector search on every new turn and
	// injects the hits as a "Relevant memories" block. NOT the app's RAM
	// footprint — current prompt usage is shown separately by the ctx dots
	// + percentage.
	mem := s.statusLabel.Render("memories") + " " + s.statusValue.Render(humanizeTokens(st.MemoryCount))
	cwd := s.statusLabel.Render("cwd") + " " + s.statusValue.Render(displayCWD(st.CWD, 24))
	// Uptime moved to the welcome card's top-right; the status row stays
	// focused on per-turn signals (model, buffer, tokens, recall, cwd, tools).
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
	if n := len(st.RunningSubagents); n > 0 {
		extras += mid + s.accent.Render("● agents") + " " + s.statusValue.Render(fmt.Sprintf("%d", n))
	}

	// "ctx" is the active prompt's share of the LLM's context window — system
	// prompt + memory injection (summary + recall + facts + ranked code) +
	// the recent conversation messages, divided by memory.token_budget.
	bufferLabel := s.statusLabel.Render("ctx") + " " + s.statusValue.Render(fmt.Sprintf("%d%%", pct))
	gap := "   "
	row := leftIndent + modelPart + gap + dots + " " + bufferLabel + gap + tokens + mid + mem + mid + cwd + mid + tools + extras
	return row
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
	filled := min(int(pct*gaugeCells), gaugeCells)

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
//
// Each historyEntry's rendered output is CACHED on the entry itself,
// stamped with the width it was rendered at. On a window resize the cache
// is invalidated en masse (renderedWidth != innerWidth). This is the
// single biggest performance lever for the TUI: without caching, every
// streamDeltaMsg re-runs glamour over the entire history, which on a
// 20-turn conversation costs ~100ms per delta and freezes the UI.
func (m *model) renderConversation() string {
	s := styles()
	w := m.viewport.Width
	if w <= 0 {
		w = m.width
	}
	if w < 4 {
		w = 4
	}
	innerWidth := max(w, 1)
	wrap := lipgloss.NewStyle().Width(innerWidth)
	wrapErr := s.red.Width(innerWidth)

	var b strings.Builder
	for i := range m.history {
		h := &m.history[i]
		if h.rendered == "" || h.renderedWidth != innerWidth {
			h.rendered = renderHistoryEntry(h, &s, wrap, innerWidth)
			h.renderedWidth = innerWidth
		}
		b.WriteString(h.rendered)
		b.WriteByte('\n')
		if h.who == "you" || h.who == "bot" {
			b.WriteByte('\n')
		}
	}
	if m.streaming {
		// Status line: "✽ Shimmying… 13s · ↓ 698 tokens".
		// Spinner glyph in accent (pulses); gerund in foreground; meta in dim.
		gerund := m.turnGerund
		if gerund == "" {
			gerund = "Working"
		}
		spinnerGlyph := s.accent.Render(m.spinner.View())
		gerundPart := s.fg.Bold(true).Render(gerund + "…")
		meta := ""
		if !m.turnStart.IsZero() {
			meta = humanizeDuration(time.Since(m.turnStart), false)
		}
		// Input tokens shown once the PreGeneration hook has reported them;
		// downstream tokens (↓) come straight off the streamed delta length.
		if m.turnInputTokens > 0 {
			if meta != "" {
				meta += " · "
			}
			meta += fmt.Sprintf("↑ %d", m.turnInputTokens)
		}
		tok := estimateTokens(m.stream)
		if tok > 0 {
			if meta != "" {
				meta += " · "
			}
			meta += fmt.Sprintf("↓ %d", tok)
		}
		if meta != "" {
			meta = s.dim.Render(" " + meta)
		}
		statusLine := spinnerGlyph + " " + gerundPart + meta
		if m.stream == "" {
			b.WriteString(statusLine)
		} else {
			// Once tokens start arriving, render them in flow above the
			// status line. A blank line between the streaming text and the
			// "Shimmying… 13s · ↓ N tokens" row keeps them visually distinct.
			b.WriteString(wrap.Render(m.stream))
			b.WriteString("\n\n")
			b.WriteString(statusLine)
		}
		b.WriteByte('\n')

		// Tasks panel inline, directly below the spinner row — mirrors
		// Claude Code's "thinking + tasks below" pattern. Only renders
		// while streaming AND when there are tasks; outside a turn the
		// panel disappears so the conversation flows freely.
		if tp := m.renderTaskPanel(innerWidth); tp != "" {
			b.WriteString(tp)
			b.WriteByte('\n')
		}
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

// renderHistoryEntry renders one history row to its final display string
// (markdown-rendered when bot, plain wrapped when user/sys). Output is
// cached on the historyEntry; callers must invalidate (renderedWidth = 0)
// when width changes.
func renderHistoryEntry(h *historyEntry, s *themedStyles, wrap lipgloss.Style, innerWidth int) string {
	var b strings.Builder
	switch h.who {
	case "you":
		b.WriteString(wrap.Render(s.user.Render("❯ ") + h.text))
	case "bot":
		b.WriteString(strings.TrimRight(RenderMarkdown(h.text, innerWidth), "\n"))
		if h.duration > 0 {
			b.WriteByte('\n')
			b.WriteString(s.dim.Render("  · took " + humanizeDuration(h.duration, true)))
		}
		// Turn-closing rule so the next ❯ is visually separated from
		// long replies. Width is the viewport inner width so the rule
		// spans the whole conversation pane.
		b.WriteByte('\n')
		ruleW := max(innerWidth-2, 4)
		b.WriteString(s.dim.Render("  " + strings.Repeat("─", ruleW)))
	default:
		b.WriteString(wrap.Render(s.sys.Render(h.text)))
	}
	return b.String()
}

// renderTaskPanel renders the sticky to-do panel above the input. Returns
// "" when there are no tasks. Each task is one line: glyph + title. Long
// lists are capped at 8 visible rows with a "+N more" trailer.
func (m model) renderTaskPanel(width int) string {
	if len(m.taskList) == 0 {
		return ""
	}
	s := styles()
	const maxRows = 8
	visible := m.taskList
	more := 0
	if len(visible) > maxRows {
		more = len(visible) - maxRows
		visible = visible[:maxRows]
	}
	lines := []string{}
	for _, t := range visible {
		glyph := "☐"
		style := s.fg
		switch t.Status {
		case tasks.StatusInProgress:
			glyph = "◐"
			style = s.accent
		case tasks.StatusCompleted:
			glyph = "☑"
			style = s.dim
		case tasks.StatusFailed:
			glyph = "✗"
			style = s.dim
		}
		title := t.Title
		if len(title) > width-6 && width > 10 {
			title = title[:width-7] + "…"
		}
		lines = append(lines, "  "+style.Render(glyph+" "+title))
	}
	if more > 0 {
		lines = append(lines, "  "+s.dim.Render(fmt.Sprintf("+%d more", more)))
	}
	return strings.Join(lines, "\n")
}

// View composes the redesigned layout (Option A from the design pass):
//
//	╭─ conversation ───────  gpt-5.5 · ctx 23% · ↑N ↓M · up 5m ─╮
//	│ ❯ user message                                            │
//	│ bot reply                                                 │
//	│ ✻ Shimmying… 13s                                          │
//	│   tasks 1/2                                               │
//	│   ◐ task A                                                │
//	│   ☑ task B                                                │
//	╰───────────────────────────────────────────────────────────╯
//	╭─ message ─────────────────────────────────────────────────╮
//	│ ❯ │                                                       │
//	│   │  type a message, or / for commands                    │
//	│   │                                                       │
//	│   │                                                       │
//	│   │                                                       │
//	╰───────────────────────────────────────────────────────────╯
//	  ~/Dev/Pythia · 🔧 18 · 📚 142 memories · 13 services
//
// Status migrated into the conversation border (top-right). Tasks panel
// renders INSIDE the conversation directly below the spinner (matches
// Claude Code's "thinking + tasks below" pattern). Welcome card removed.
// Message box bumped to 5 rows so the input area feels substantial.
func (m model) View() string {
	if m.width <= 0 || m.height <= 0 {
		return ""
	}
	if m.height < minBoxedHeight {
		return m.viewFallback()
	}

	width := m.width
	message := m.renderMessageBox(width)
	footer := m.renderFooterLine(width)

	// Layout cost: conv + blank + message(input.Height()+2) + blank + footer.
	msgH := m.input.Height() + 2
	used := 1 + msgH + 1 + 1 // blank between conv+message, message, blank, footer
	convH := max(m.height-used, 4)
	conv := m.renderConversationBox(width, convH)

	parts := []string{conv, "", message}
	if len(m.completion.matches) > 0 {
		parts = append(parts, m.renderCompletionDropdown(width))
	} else if footer != "" {
		parts = append(parts, "", footer)
	}

	if m.helpOpen {
		overlay := indentBlock(styles().dim.Render(m.renderHelpOverlay()))
		return lipgloss.JoinVertical(lipgloss.Left,
			overlay, "", conv, "", message, footer)
	}
	return lipgloss.JoinVertical(lipgloss.Left, parts...)
}

// renderFooterLine is the thin bottom info bar — cwd · tools · memories ·
// services. Replaces the old packed status row. Returns "" when no status
// has been delivered yet.
func (m model) renderFooterLine(_ int) string {
	if !m.hasStatus {
		return ""
	}
	s := styles()
	st := m.status
	parts := []string{}
	if st.CWD != "" {
		parts = append(parts, s.dim.Render(displayCWD(st.CWD, 32)))
	}
	parts = append(parts, s.dim.Render(fmt.Sprintf("🔧 %d", len(st.ToolNames))))
	parts = append(parts, s.dim.Render(fmt.Sprintf("📚 %d memories", st.MemoryCount)))
	if m.workspace != nil {
		if n := len(m.workspace.List()); n > 0 {
			parts = append(parts, s.dim.Render(fmt.Sprintf("🗂 %d services", n)))
		}
	}
	if n := len(st.RunningSubagents); n > 0 {
		parts = append(parts, s.accent.Render(fmt.Sprintf("● agents %d", n)))
	}
	return leftIndent + strings.Join(parts, s.dim.Render("  ·  "))
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
