package cli

import (
	"os/user"
	"time"

	"github.com/charmbracelet/lipgloss"
)

// welcomeArt is the brand mark printed in the welcome card and by /about.
// Six rows of double-line box-drawing block letters spelling "cax".
const welcomeArt = `░█████╗░░█████╗░██╗░░██╗
██╔══██╗██╔══██╗╚██╗██╔╝
██║░░╚═╝███████║░╚███╔╝░
██║░░██╗██╔══██║░██╔██╗░
╚█████╔╝██║░░██║██╔╝╚██╗
░╚════╝░╚═╝░░╚═╝╚═╝░░╚═╝`

// Version is the binary's release tag. Override via
// `go build -ldflags "-X github.com/caxqueiroz/cax/internal/channel/cli.Version=1.2.3"`.
var Version = "dev"

// renderWelcomeBlock builds the labeled welcome card: art on the left,
// per-session greeting (username + local date/time) and version centered
// vertically alongside the art, with uptime sitting on the TOP RIGHT row
// of the info column so it's the first thing the eye lands on.
func (m model) renderWelcomeBlock(width int) string {
	s := styles()
	// Art rendered in true white (#ffffff) regardless of the active theme.
	artStyled := lipgloss.NewStyle().Foreground(lipgloss.Color("#ffffff")).Bold(true).Render(welcomeArt)

	uname := "you"
	if u, err := user.Current(); err == nil && u.Username != "" {
		uname = u.Username
	}
	when := time.Now().Format("Mon 02 Jan · 15:04")

	uptime := ""
	if !m.sessionStart.IsZero() {
		uptime = s.dim.Render("up " + humanizeDuration(time.Since(m.sessionStart), false))
	}
	greet := s.fg.Bold(true).Render("hello, "+uname) + s.dim.Render("  ·  "+when)
	version := s.dim.Render("cax v" + Version)

	// Info column rows: uptime · greet · blank · version. lipgloss.Center
	// then pads above and below so the four lines sit at the vertical
	// mid-line of the 6-row art.
	infoRows := []string{}
	if uptime != "" {
		infoRows = append(infoRows, uptime)
	}
	infoRows = append(infoRows, greet, "", version)
	infoBlock := lipgloss.JoinVertical(lipgloss.Left, infoRows...)
	body := lipgloss.JoinHorizontal(lipgloss.Center, artStyled, "   ", infoBlock)
	// Pad 1 row above and below the body so the art has breathing room
	// inside the rounded "welcome" border.
	padded := lipgloss.NewStyle().Padding(1, 0).Render(body)

	inner := max(
		// 2 indent + 2 border
		width-4, 20)
	return borderWithTitle(padded, "welcome", inner, borderColor())
}
