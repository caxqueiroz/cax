package cli

import (
	"strings"
	"testing"

	"github.com/caxqueiroz/czcli/internal/channel"
)

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
	for _, want := range []string{"claude-opus", "ctx", "8k", "76%"} {
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
	// "❯" is the user prompt prefix; assistant replies have no prefix so we
	// just assert the message text is present.
	for _, want := range []string{"claude-opus", "❯", "hey", "hi!", "1d", "mem"} {
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
	g := m.renderGauge(s.ContextTokens, s.ContextBudget)
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
	g := m.renderGauge(s.ContextTokens, s.ContextBudget)
	if !strings.Contains(g, "⚠") || !strings.Contains(g, "95%") {
		t.Errorf("expected red warning + 95%%: %s", g)
	}
}
