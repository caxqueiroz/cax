// Package cli — welcome card shown on first render (empty history, not
// streaming) and reused by the /about slash command. Keeping the ASCII art
// and the rendering helper here isolates the brand surface from view.go's
// layout math.
package cli

import (
	"strings"

	"github.com/caxqueiroz/cax/internal/theme"
	"github.com/charmbracelet/lipgloss"
)

// welcomeArt is the brand mark printed above the tagline. Three rows by
// design — wide enough to be recognizable, short enough not to dominate a
// fresh terminal. It is intentionally rendered verbatim (no centering of
// individual lines), and the renderer pads each line to match the widest
// row before composing the card.
const welcomeArt = "   ╭─╮   ╱╲    ╲ ╱\n" +
	"   │    ├─┤     × \n" +
	"   ╰─╯  ╵ ╵    ╱ ╲"

// welcomeTagline is the dim subtitle next to the art. /about echoes the
// same string so the two surfaces stay aligned.
const welcomeTagline = "personal AI assistant"

// welcomeHint is the third line inside the card — the four most useful
// entry points for a brand-new user. /help opens the overlay, /theme cycles
// themes, /new starts a creator wizard.
const welcomeHint = "type a message · /help · /theme · /new"

// renderWelcomeCard composes the welcome card inside a rounded box titled
// "welcome". Layout (inside the box):
//
//	<art row 1>     personal AI assistant
//	<art row 2>
//	<art row 3>     type a message · /help · /theme · /new
//
// width is the full terminal width; the card honors the global 2-space
// indent like every other box.
func renderWelcomeCard(width int) string {
	col := borderColor()
	boxOuter := width - 2*len(leftIndent)
	if boxOuter < 24 {
		boxOuter = 24
	}
	// -2 border, -2 padding (1 each side).
	innerW := boxOuter - 2 - 2
	if innerW < 1 {
		innerW = 1
	}

	s := styles()

	// Normalize art rows to the widest line so the right column lines up.
	artLines := strings.Split(welcomeArt, "\n")
	maxArtW := 0
	for _, ln := range artLines {
		if w := lipgloss.Width(ln); w > maxArtW {
			maxArtW = w
		}
	}
	pad := lipgloss.NewStyle().Width(maxArtW)
	for i, ln := range artLines {
		artLines[i] = pad.Render(ln)
	}

	// Right column: tagline on row 1, blank on row 2, hint on row 3. Both
	// the tagline and the hint are dimmed; the tagline gets a leading
	// 3-space gutter from the art column.
	gutter := "   "
	rightW := innerW - maxArtW - lipgloss.Width(gutter)
	if rightW < 1 {
		rightW = 1
	}
	rightStyle := lipgloss.NewStyle().Foreground(lipgloss.Color(theme.Active().Dim)).Width(rightW)
	rightLines := []string{
		rightStyle.Render(welcomeTagline),
		rightStyle.Render(""),
		rightStyle.Render(welcomeHint),
	}

	// Compose rows. If the art is taller than the right column (it isn't
	// here, but be defensive), pad the right column with blanks.
	rows := make([]string, 0, len(artLines))
	for i, art := range artLines {
		var right string
		if i < len(rightLines) {
			right = rightLines[i]
		} else {
			right = rightStyle.Render("")
		}
		rows = append(rows, s.fg.Render(art)+gutter+right)
	}
	body := strings.Join(rows, "\n")
	// Apply one-cell horizontal padding so the content doesn't kiss the
	// border. borderWithTitle expects the caller to pre-size the inner
	// content; we sized to innerW already.
	padded := lipgloss.NewStyle().Padding(0, 1).Render(body)
	return indentBlock(borderWithTitle(padded, "welcome", boxOuter, col))
}

// welcomeBlock is the rendered welcome card plus a leading blank line so it
// drops in directly between the header and the conversation box.
func (m model) renderWelcomeBlock(width int) string {
	return renderWelcomeCard(width)
}
