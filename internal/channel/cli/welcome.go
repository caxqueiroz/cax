package cli

import (
	"os/user"
	"time"

	"github.com/charmbracelet/lipgloss"
)

// welcomeArt is the brand mark printed in the welcome card and by /about.
// Five rows of figlet "graffiti"-style block letters spelling "cax".
const welcomeArt = ` ____  ____ ___  _
/   _\/  _ \\  \//
|  /  | / \| \  /
|  \__| |-|| /  \
\____/\_/ \|/__/\\`

// Version is the binary's release tag. Override via
// `go build -ldflags "-X github.com/caxqueiroz/cax/internal/channel/cli.Version=1.2.3"`.
var Version = "dev"

// renderWelcomeBlock builds the labeled welcome card: art on the left,
// per-session greeting (username + local date/time) and version centered
// vertically alongside the art.
func (m model) renderWelcomeBlock(width int) string {
	s := styles()
	// Art rendered in bright white regardless of theme accent — matches the
	// "fill it up" look of figlet output on a bare terminal.
	artStyled := lipgloss.NewStyle().Foreground(lipgloss.Color("231")).Bold(true).Render(welcomeArt)

	uname := "you"
	if u, err := user.Current(); err == nil && u.Username != "" {
		uname = u.Username
	}
	when := time.Now().Format("Mon 02 Jan · 15:04")

	greet := s.fg.Bold(true).Render("hello, "+uname) + s.dim.Render("  ·  "+when)
	version := s.dim.Render("cax v" + Version)

	// Info column has a blank row between greet and version so the two
	// lines aren't crammed together. lipgloss.Center then pads above and
	// below so they sit on the art's row-2 / row-4 (the 5-row art has its
	// horizontal mid-line at row 3).
	infoBlock := lipgloss.JoinVertical(lipgloss.Left, greet, "", version)
	body := lipgloss.JoinHorizontal(lipgloss.Center, artStyled, "   ", infoBlock)

	inner := width - 4 // 2 indent + 2 border
	if inner < 20 {
		inner = 20
	}
	return borderWithTitle(body, "welcome", inner, borderColor())
}
