package cli

import (
	"log/slog"

	"github.com/charmbracelet/glamour"

	"github.com/caxqueiroz/cax/internal/theme"
)

// knownGlamourStyles enumerates the built-in glamour style names accepted
// at v1.0.0. Anything outside this set falls back to "auto".
var knownGlamourStyles = map[string]struct{}{
	"auto": {}, "dark": {}, "light": {}, "dracula": {},
	"tokyo-night": {}, "notty": {}, "pink": {}, "ascii": {},
}

// RenderMarkdown renders input as ANSI-styled markdown using glamour, keyed
// to the active theme's Markdown field. Width is clamped to a sensible
// minimum so glamour doesn't panic on very narrow viewports. On any error
// (unknown style, glamour build/render failure) the original input is
// returned verbatim so the TUI never breaks on weird markdown.
func RenderMarkdown(input string, width int) string {
	if input == "" {
		return ""
	}
	if width < 20 {
		width = 20
	}
	style := "auto"
	if a := theme.Active(); a != nil && a.Markdown != "" {
		if _, ok := knownGlamourStyles[a.Markdown]; ok {
			style = a.Markdown
		} else {
			slog.Warn("markdown: unknown glamour style, falling back to auto",
				"requested", a.Markdown)
		}
	}
	r, err := glamour.NewTermRenderer(
		glamour.WithStandardStyle(style),
		glamour.WithWordWrap(width),
	)
	if err != nil {
		slog.Warn("markdown: build renderer", "style", style, "err", err)
		return input
	}
	out, err := r.Render(input)
	if err != nil {
		slog.Warn("markdown: render", "err", err)
		return input
	}
	return out
}
