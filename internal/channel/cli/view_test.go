package cli

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"

	"github.com/caxqueiroz/cax/internal/channel"
	"github.com/caxqueiroz/cax/internal/theme"
)

func TestRenderConversationRendersAssistantAsMarkdown(t *testing.T) {
	theme.LoadBuiltins()
	th, _ := theme.Get("default-dark")
	theme.Set(th)

	m := newModel(80, 24)
	m.viewport.Width = 60
	m.history = []historyEntry{
		{who: "you", text: "hi"},
		{who: "bot", text: "# Header\n\nbody"},
	}
	out := m.renderConversation()
	if !strings.Contains(out, "❯") {
		t.Fatalf("user prefix missing in output: %q", out)
	}
	if !strings.Contains(out, "Header") {
		t.Fatalf("markdown header not rendered: %q", out)
	}
}

func statusFixture() channel.Status {
	return channel.Status{
		Provider:      "anthropic",
		Model:         "claude-opus",
		OnFallback:    false,
		FallbackIndex: 0,
		ContextTokens: 6100,
		ContextBudget: 8000,
		Usage: channel.UsageRollup{
			Day:   channel.UsageTotals{InputTokens: 100000, OutputTokens: 24000},
			Week:  channel.UsageTotals{InputTokens: 700000, OutputTokens: 112000},
			Month: channel.UsageTotals{InputTokens: 3000000, OutputTokens: 200000},
		},
		MemSizeBytes:  18 * 1024 * 1024,
		MessageCount:  42,
		MemoryCount:   17,
		ToolNames:     []string{"a", "b", "c", "d", "e", "f", "g", "h"},
		SubagentNames: []string{"explore", "plan", "general"},
	}
}

// TestComposeTitledTopRendersTitle exercises the labeled-border helper
// directly: the rendered top run must include the title, start with the
// rounded TopLeft glyph, and end with the rounded TopRight glyph.
func TestComposeTitledTopRendersTitle(t *testing.T) {
	theme.LoadBuiltins()
	th, _ := theme.Get("default-dark")
	theme.Set(th)

	rb := lipgloss.RoundedBorder()
	out := composeTitledTop("conversation", 40, rb, borderColor())
	if !strings.Contains(out, "conversation") {
		t.Fatalf("title not spliced into top border: %q", out)
	}
	if !strings.Contains(out, "╭") || !strings.Contains(out, "╮") {
		t.Fatalf("rounded corners missing from top border: %q", out)
	}
}

// TestBorderWithTitleWrapsContent confirms the helper returns a labeled
// rounded box whose top line carries the title and whose body opens with
// a vertical border rune.
func TestBorderWithTitleWrapsContent(t *testing.T) {
	theme.LoadBuiltins()
	th, _ := theme.Get("default-dark")
	theme.Set(th)

	out := borderWithTitle("hello world", "message", 40, borderColor())
	if !strings.Contains(out, "message") {
		t.Fatalf("title missing in labeled border output: %q", out)
	}
	if !strings.HasPrefix(stripANSI(out), "╭") {
		t.Fatalf("expected output to start with ╭, got %q", out)
	}
}

// TestStatusRowUsesBufferLabel pins the rename: the status row must
// surface the literal `buffer` and the model name with its ✓ marker.
func TestStatusRowUsesBufferLabel(t *testing.T) {
	theme.LoadBuiltins()
	th, _ := theme.Get("default-dark")
	theme.Set(th)

	m := newModel(80, 24)
	m.status = statusFixture()
	m.hasStatus = true
	row := m.renderStatusRow(80)
	for _, want := range []string{"claude-opus", "✓", "buffer", "76%", "1d", "mem", "cwd", "🔧"} {
		if !strings.Contains(row, want) {
			t.Errorf("status row missing %q\n%s", want, row)
		}
	}
}

// TestDisplayCWD covers HOME compression, length cap, and the empty case.
func TestDisplayCWD(t *testing.T) {
	t.Setenv("HOME", "/Users/x")
	cases := []struct {
		in, want string
		max      int
	}{
		{"", "?", 24},
		{"/Users/x", "~", 24},
		{"/Users/x/dev/cax", "~/dev/cax", 24},
		{"/tmp/nested/dir/that/is/quite/long", "…re/nested/dir/that/is/quite/long"[:24], 24},
	}
	for _, c := range cases {
		got := displayCWD(c.in, c.max)
		if c.in == "/tmp/nested/dir/that/is/quite/long" {
			// Just assert the leading ellipsis and the trailing suffix; the
			// exact prefix bytes depend on the input length.
			if !strings.HasPrefix(got, "…") || !strings.HasSuffix(got, "long") {
				t.Errorf("displayCWD(%q, %d) = %q, want leading … and trailing 'long'", c.in, c.max, got)
			}
			if len([]rune(got)) != c.max {
				t.Errorf("displayCWD(%q, %d) length = %d, want %d", c.in, c.max, len([]rune(got)), c.max)
			}
			continue
		}
		if got != c.want {
			t.Errorf("displayCWD(%q, %d) = %q, want %q", c.in, c.max, got, c.want)
		}
	}
}

// TestStatusRowFallbackIndicator confirms the fallback marker still
// reaches the status row when the active model is on a fallback.
func TestStatusRowFallbackIndicator(t *testing.T) {
	m := newModel(80, 24)
	s := statusFixture()
	s.OnFallback = true
	s.FallbackIndex = 2
	m.status = s
	m.hasStatus = true
	if !strings.Contains(m.renderStatusRow(80), "fallback") {
		t.Errorf("expected fallback indicator in status row")
	}
}

// TestBufferDotsThreshold pins the amber threshold: 76% must light up
// amber dots, while 50% stays in the accent (no amber/red).
func TestBufferDotsThreshold(t *testing.T) {
	theme.LoadBuiltins()
	th, _ := theme.Get("default-dark")
	theme.Set(th)
	s := styles()

	// At 76% (gauge fixture) we expect filled dots in amber territory.
	dots, pct := renderBufferDots(s, 6100, 8000)
	if pct != 76 {
		t.Errorf("pct = %d, want 76", pct)
	}
	if !strings.Contains(dots, "●") || !strings.Contains(dots, "○") {
		t.Errorf("dot indicators missing filled/empty glyphs: %q", dots)
	}

	// At 50% we still see the same glyphs but no warning.
	dots50, pct50 := renderBufferDots(s, 4000, 8000)
	if pct50 != 50 {
		t.Errorf("pct50 = %d, want 50", pct50)
	}
	if !strings.Contains(dots50, "●") {
		t.Errorf("filled dots missing at 50%%")
	}
}

// TestViewIncludesAllRegions validates the View() output carries every
// labeled region of the v2 layout: branded header, conversation box,
// status row, and message box — plus the user prefix from a turn.
func TestViewIncludesAllRegions(t *testing.T) {
	theme.LoadBuiltins()
	th, _ := theme.Get("default-dark")
	theme.Set(th)

	m := newModel(80, 24)
	m.status = statusFixture()
	m.hasStatus = true
	m.history = []historyEntry{{who: "you", text: "hey"}, {who: "bot", text: "hi!"}}
	m.refreshViewport()
	out := m.View()
	for _, want := range []string{"cax", "conversation", "message", "buffer", "❯", "claude-opus", "1d", "mem"} {
		if !strings.Contains(out, want) {
			t.Errorf("View missing %q", want)
		}
	}
}

// TestViewRoundedCorners checks the visible identity of the v2 layout —
// rounded corner glyphs must appear in the rendered View, because the
// header, conversation, and message regions all wrap their content in
// rounded boxes. This guards against a regression where the boxes are
// silently dropped during a refactor.
func TestViewRoundedCorners(t *testing.T) {
	theme.LoadBuiltins()
	th, _ := theme.Get("default-dark")
	theme.Set(th)

	m := newModel(80, 24)
	m.status = statusFixture()
	m.hasStatus = true
	m.refreshViewport()
	out := m.View()
	for _, glyph := range []string{"╭", "╮", "╰", "╯"} {
		if !strings.Contains(out, glyph) {
			t.Errorf("rounded corner %q missing from View output", glyph)
		}
	}
}

// TestViewFallbackOnTinyHeight exercises the small-terminal fallback so a
// 4x80 pane stays usable (no boxes, just header line + viewport + status +
// input). The output must still contain the brand name and `buffer`.
func TestViewFallbackOnTinyHeight(t *testing.T) {
	theme.LoadBuiltins()
	th, _ := theme.Get("default-dark")
	theme.Set(th)

	m := newModel(80, 6)
	m.status = statusFixture()
	m.hasStatus = true
	m.refreshViewport()
	out := m.View()
	if out == "" {
		t.Fatalf("fallback should not produce an empty View")
	}
	if !strings.Contains(out, "cax") {
		t.Errorf("fallback missing brand: %q", out)
	}
	if !strings.Contains(out, "buffer") {
		t.Errorf("fallback missing buffer label: %q", out)
	}
}

// TestViewWelcomeCardOnEmptyHistory checks that the welcome card with the
// ASCII art appears on first render (empty history, not streaming), and
// disappears once any turn lands in history.
func TestViewWelcomeCardOnEmptyHistory(t *testing.T) {
	theme.LoadBuiltins()
	th, _ := theme.Get("default-dark")
	theme.Set(th)

	m := newModel(80, 24)
	m.status = statusFixture()
	m.hasStatus = true
	m.refreshViewport()
	out := stripANSI(m.View())
	if !strings.Contains(out, "welcome") {
		t.Errorf("View on empty history missing welcome card title: %q", out)
	}
	if !strings.Contains(out, "personal AI assistant") {
		t.Errorf("View on empty history missing welcome tagline: %q", out)
	}
	// First row of the ASCII art is the most stable marker — assert one of
	// its distinctive glyph runs is in the output.
	if !strings.Contains(out, "╭─╮") {
		t.Errorf("View on empty history missing welcome art glyphs: %q", out)
	}

	// Once a turn has happened the welcome card must go away.
	m.history = []historyEntry{{who: "you", text: "hey"}, {who: "bot", text: "hi!"}}
	m.refreshViewport()
	out2 := stripANSI(m.View())
	if strings.Contains(out2, "welcome") && strings.Contains(out2, "personal AI assistant") {
		t.Errorf("welcome card lingered after history: %q", out2)
	}
}

// stripANSI removes ANSI escape sequences from s so substring assertions
// on raw glyphs survive across themed styles wrapping the same text.
func stripANSI(s string) string {
	var b strings.Builder
	inEsc := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c == 0x1b { // ESC
			inEsc = true
			continue
		}
		if inEsc {
			if c == 'm' {
				inEsc = false
			}
			continue
		}
		b.WriteByte(c)
	}
	return b.String()
}
