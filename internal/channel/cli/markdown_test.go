package cli

import (
	"strings"
	"testing"

	"github.com/caxqueiroz/cax/internal/theme"
)

func TestRenderMarkdownNonEmpty(t *testing.T) {
	theme.LoadBuiltins()
	for _, name := range []string{"default-dark", "dracula", "nord", "mono"} {
		th, err := theme.Get(name)
		if err != nil {
			t.Fatalf("Get(%s): %v", name, err)
		}
		theme.Set(th)
		out := RenderMarkdown("# Hello\n\nworld with `code`.", 60)
		if strings.TrimSpace(out) == "" {
			t.Fatalf("%s: empty render", name)
		}
		if !strings.Contains(out, "Hello") || !strings.Contains(out, "world") {
			t.Fatalf("%s: missing content, got %q", name, out)
		}
	}
}

func TestRenderMarkdownFallbackOnError(t *testing.T) {
	theme.LoadBuiltins()
	bad := &theme.Theme{Name: "bad", Foreground: "#fff", Dim: "#888", Separator: "#444",
		Accent: "#fff", OK: "#fff", Amber: "#fff", Red: "#fff",
		UserPrefix: "#fff", AssistantText: "#fff", SysText: "#888", CodeBG: "#222",
		GaugeFilled: "#fff", GaugeEmpty: "#444", Markdown: "no-such-style"}
	theme.Register(bad)
	theme.Set(bad)
	in := "raw input"
	got := RenderMarkdown(in, 40)
	if got == "" {
		t.Fatalf("fallback should return non-empty")
	}
	if !strings.Contains(got, "raw input") {
		t.Fatalf("fallback should preserve input, got %q", got)
	}
}
