package cli

import "github.com/charmbracelet/lipgloss"

// welcomeArt is the brand mark printed in the welcome card and by /about.
// Five rows of figlet "graffiti"-style block letters spelling "cax".
const welcomeArt = ` ____  ____ ___  _
/   _\/  _ \\  \//
|  /  | / \| \  /
|  \__| |-|| /  \
\____/\_/ \|/__/\\`

// welcomeTagline is shown next to the art in the welcome card and on its own
// line in /about.
const welcomeTagline = "personal AI assistant"

// welcomeHint sits below the art in the welcome card; one-line summary of the
// fastest things a brand-new user can try.
const welcomeHint = "type a message · /help · /theme · /new"

// renderWelcomeBlock builds the labeled welcome card: art on the left,
// tagline on the first content row, hint on the last. Width is the terminal
// width minus the global 2-space indent.
func (m model) renderWelcomeBlock(width int) string {
	s := styles()
	artStyled := s.accent.Render(welcomeArt)
	tagline := s.dim.Render(welcomeTagline)
	hint := s.dim.Render(welcomeHint)

	// Two columns: art (fixed width) on the left, info (tagline + blank +
	// hint, vertically stacked) on the right. lipgloss handles alignment.
	infoBlock := lipgloss.JoinVertical(lipgloss.Left, tagline, "", hint)
	body := lipgloss.JoinHorizontal(lipgloss.Top, artStyled, "  ", infoBlock)

	inner := width - 4 // 2 indent + 2 border
	if inner < 20 {
		inner = 20
	}
	return borderWithTitle(body, "welcome", inner, borderColor())
}
