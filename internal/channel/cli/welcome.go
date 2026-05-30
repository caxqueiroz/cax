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

// welcomeHint sits below the user info in the welcome card; one-line summary
// of the fastest things a brand-new user can try.
const welcomeHint = "type a message · /help · /theme · /new · /code"

// renderWelcomeBlock builds the labeled welcome card: art on the left,
// per-session greeting (username + local date/time) and version on the right,
// followed by the quick-start hint.
func (m model) renderWelcomeBlock(width int) string {
	s := styles()
	artStyled := s.accent.Render(welcomeArt)

	uname := "you"
	if u, err := user.Current(); err == nil && u.Username != "" {
		uname = u.Username
	}
	when := time.Now().Format("Mon 02 Jan · 15:04")

	greet := s.fg.Bold(true).Render("hello, "+uname) + s.dim.Render("  ·  "+when)
	version := s.dim.Render("cax v" + Version)
	hint := s.dim.Render(welcomeHint)

	// Info column (3 rows) is shorter than the art (5 rows). Joining at
	// lipgloss.Center makes lipgloss pad above and below the info block so
	// the greeting sits at the vertical mid-line of the art.
	infoBlock := lipgloss.JoinVertical(lipgloss.Left, greet, version, hint)
	body := lipgloss.JoinHorizontal(lipgloss.Center, artStyled, "   ", infoBlock)

	inner := width - 4 // 2 indent + 2 border
	if inner < 20 {
		inner = 20
	}
	return borderWithTitle(body, "welcome", inner, borderColor())
}
