package cli

import (
	"strings"
	"testing"

	"github.com/caxqueiroz/czcli/internal/channel"
	"github.com/caxqueiroz/czcli/internal/theme"
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

func TestViewLayoutThinSeparatorsAndIndent(t *testing.T) {
	theme.LoadBuiltins()
	th, _ := theme.Get("default-dark")
	theme.Set(th)

	m := newModel(60, 12)
	m.hasStatus = true
	out := m.View()
	// Must contain at least three horizontal separators (top/middle/bottom)
	if strings.Count(out, "─") < 3*10 { // generous lower bound across width
		t.Fatalf("expected thin separator lines, got %q", out)
	}
	// Heavy bottom-bar background ANSI should be gone (no Background SGR 48;5;236).
	if strings.Contains(out, "48;5;236") {
		t.Fatalf("unexpected legacy bottom background fill")
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

func TestTopBarShowsModelAndGauge(t *testing.T) {
	m := newModel(80, 24)
	m.status = statusFixture()
	m.hasStatus = true
	bar := m.renderTopBar()
	for _, want := range []string{"claude-opus", "hist", "8k", "76%"} {
		if !strings.Contains(bar, want) {
			t.Errorf("top bar missing %q\n%s", want, bar)
		}
	}
}

func TestTopBarFallbackIndicator(t *testing.T) {
	m := newModel(80, 24)
	s := statusFixture()
	s.OnFallback = true
	s.FallbackIndex = 2
	m.status = s
	m.hasStatus = true
	bar := m.renderTopBar()
	if !strings.Contains(bar, "fallback") {
		t.Errorf("expected fallback indicator, got\n%s", bar)
	}
}

func TestGaugeAmberWarningAtThreshold(t *testing.T) {
	// 6100/8000 = 76% → amber, must include the ⚠ marker.
	m := newModel(80, 24)
	m.status = statusFixture()
	m.hasStatus = true
	if !strings.Contains(m.renderTopBar(), "⚠") {
		t.Errorf("expected ⚠ at 76%% context usage")
	}
}

func TestBottomBarShowsUsageMemAndCounts(t *testing.T) {
	m := newModel(80, 24)
	m.status = statusFixture()
	m.hasStatus = true
	bar := m.renderBottomBar()
	for _, want := range []string{"1d", "124k", "1w", "1m", "mem", "18MB", "8", "3"} {
		if !strings.Contains(bar, want) {
			t.Errorf("bottom bar missing %q\n%s", want, bar)
		}
	}
}

func TestViewIncludesAllRegions(t *testing.T) {
	m := newModel(80, 24)
	m.status = statusFixture()
	m.hasStatus = true
	m.history = []historyEntry{{who: "you", text: "hey"}, {who: "bot", text: "hi!"}}
	m.refreshViewport()
	out := m.View()
	// "❯" is the user prompt prefix; assistant replies are routed through
	// glamour markdown so individual characters get wrapped in ANSI escapes
	// (e.g. "hi!" becomes "hi" + "!" with sgr resets between). Substrings
	// must therefore be glamour-stable single-char or single-word tokens.
	for _, want := range []string{"claude-opus", "❯", "hey", "1d", "mem"} {
		if !strings.Contains(out, want) {
			t.Errorf("View missing %q", want)
		}
	}
}

func TestGaugeNoWarningBelowAmber(t *testing.T) {
	m := newModel(80, 24)
	s := statusFixture()
	s.ContextTokens = 4000 // 50%
	m.status = s
	m.hasStatus = true
	g := m.renderGauge(styles(), s.ContextTokens, s.ContextBudget)
	if strings.Contains(g, "⚠") {
		t.Errorf("did not expect ⚠ at 50%%: %s", g)
	}
	if !strings.Contains(g, "50%") {
		t.Errorf("expected 50%% in gauge: %s", g)
	}
}

func TestGaugeRedAtNinety(t *testing.T) {
	m := newModel(80, 24)
	s := statusFixture()
	s.ContextTokens = 7600 // 95%
	m.status = s
	m.hasStatus = true
	g := m.renderGauge(styles(), s.ContextTokens, s.ContextBudget)
	if !strings.Contains(g, "⚠") || !strings.Contains(g, "95%") {
		t.Errorf("expected red warning + 95%%: %s", g)
	}
}
