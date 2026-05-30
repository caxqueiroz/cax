package cli

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/caxqueiroz/czcli/internal/channel"
	"github.com/caxqueiroz/czcli/internal/theme"
)

func TestNewModelDefaults(t *testing.T) {
	m := newModel(80, 24)
	if m.width != 80 || m.height != 24 {
		t.Fatalf("size = %dx%d, want 80x24", m.width, m.height)
	}
	if m.input.Value() != "" {
		t.Errorf("input should start empty, got %q", m.input.Value())
	}
	if len(m.history) != 0 {
		t.Errorf("history should start empty, got %d entries", len(m.history))
	}
	if m.streaming {
		t.Errorf("model should not start in streaming state")
	}
	// Regression: the input must be focused so bubbles textarea accepts
	// character keys. Focusing inside the value-receiver Init() mutates only
	// a copy and leaves typing dead.
	if !m.input.Focused() {
		t.Errorf("input should be focused so character keys are accepted")
	}
}

func TestInitReturnsCmd(t *testing.T) {
	m := newModel(80, 24)
	if cmd := m.Init(); cmd == nil {
		t.Errorf("Init should return a focus command, got nil")
	}
}

func update(m model, msg tea.Msg) model {
	next, _ := m.Update(msg)
	return next.(model)
}

func TestWindowResizeSizesViewport(t *testing.T) {
	m := newModel(10, 10)
	m = update(m, tea.WindowSizeMsg{Width: 100, Height: 40})
	if m.width != 100 || m.height != 40 {
		t.Fatalf("size = %dx%d, want 100x40", m.width, m.height)
	}
	if m.viewport.Width != 100 {
		t.Errorf("viewport width = %d, want 100", m.viewport.Width)
	}
}

func TestEnterEchoesUserAndStreams(t *testing.T) {
	m := newModel(80, 24)
	m.input.SetValue("hey")
	m = update(m, tea.KeyMsg{Type: tea.KeyEnter})
	if !m.streaming {
		t.Errorf("expected streaming after Enter")
	}
	if len(m.history) != 1 || m.history[0].who != "you" || m.history[0].text != "hey" {
		t.Fatalf("expected echoed user line, got %+v", m.history)
	}
	if m.input.Value() != "" {
		t.Errorf("input should be cleared after submit, got %q", m.input.Value())
	}
}

func TestStreamDeltaAccumulates(t *testing.T) {
	m := newModel(80, 24)
	m.streaming = true
	m = update(m, streamDeltaMsg{text: "hel"})
	m = update(m, streamDeltaMsg{text: "lo"})
	if m.stream != "hello" {
		t.Errorf("stream = %q, want %q", m.stream, "hello")
	}
}

func TestTurnDoneFinalizesBotEntry(t *testing.T) {
	m := newModel(80, 24)
	m.streaming = true
	m.stream = "partial"
	m = update(m, turnDoneMsg{reply: "final answer"})
	if m.streaming {
		t.Errorf("should leave streaming state when turn done")
	}
	last := m.history[len(m.history)-1]
	if last.who != "bot" || last.text != "final answer" {
		t.Errorf("expected finalized bot entry, got %+v", last)
	}
	if m.stream != "" {
		t.Errorf("stream buffer should be cleared, got %q", m.stream)
	}
}

func TestSubagentEventTracksRunning(t *testing.T) {
	m := newModel(80, 24)
	m = update(m, subagentEventMsg{kind: "subagent_start", name: "explore"})
	if len(m.running) != 1 || m.running[0] != "explore" {
		t.Fatalf("expected running [explore], got %v", m.running)
	}
	m = update(m, subagentEventMsg{kind: "subagent_end", name: "explore"})
	if len(m.running) != 0 {
		t.Errorf("expected no running subagents, got %v", m.running)
	}
}

func TestStatusMsgStored(t *testing.T) {
	m := newModel(80, 24)
	m = update(m, statusMsg{status: channel.Status{Model: "gpt-x"}})
	if !m.hasStatus || m.status.Model != "gpt-x" {
		t.Errorf("expected stored status, got %+v hasStatus=%v", m.status, m.hasStatus)
	}
	if !strings.Contains(m.View(), "gpt-x") {
		t.Errorf("View should reflect new model name")
	}
}

func TestEnterSubmitsAndAltEnterInsertsNewline(t *testing.T) {
	m := newModel(80, 24)
	m = update(m, tea.WindowSizeMsg{Width: 80, Height: 24})
	m.input.SetValue("ab")
	// Alt+Enter must insert a newline, not submit.
	m = update(m, tea.KeyMsg{Type: tea.KeyEnter, Alt: true})
	if m.streaming {
		t.Fatalf("Alt+Enter must not start a turn")
	}
	if !strings.Contains(m.input.Value(), "\n") {
		t.Fatalf("Alt+Enter should insert newline; input = %q", m.input.Value())
	}
	// Now append "cd" by direct SetValue (the test isn't trying to drive raw
	// runes through textarea — it just asserts the Enter-vs-Alt+Enter routing).
	m.input.SetValue(m.input.Value() + "cd")
	// Enter must submit.
	m = update(m, tea.KeyMsg{Type: tea.KeyEnter})
	if !m.streaming {
		t.Fatalf("Enter should kick off a streaming turn")
	}
	if m.input.Value() != "" {
		t.Fatalf("Enter should reset the input; got %q", m.input.Value())
	}
}

func TestTextareaGrowsAndCaps(t *testing.T) {
	m := newModel(80, 24)
	m = update(m, tea.WindowSizeMsg{Width: 80, Height: 24})
	// Start at height 1.
	if m.input.Height() != 1 {
		t.Fatalf("starting height = %d, want 1", m.input.Height())
	}
	// Insert 8 newlines via SetValue then a no-op Update so the height
	// recompute runs.
	m.input.SetValue("a\nb\nc\nd\ne\nf\ng\nh\ni")
	m = update(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{' '}})
	if m.input.Height() != 6 {
		t.Fatalf("capped height = %d, want 6", m.input.Height())
	}
}

func TestCtrlTCyclesTheme(t *testing.T) {
	theme.LoadBuiltins()
	dark, _ := theme.Get("default-dark")
	theme.Set(dark)
	before := theme.Active()

	m := newModel(80, 24)
	m = update(m, tea.WindowSizeMsg{Width: 80, Height: 24})
	m = update(m, tea.KeyMsg{Type: tea.KeyCtrlT})
	after := theme.Active()
	if len(theme.List()) > 1 && before != nil && after != nil && before.Name == after.Name {
		t.Fatalf("Ctrl+T did not change theme (still %q with %d themes registered)", before.Name, len(theme.List()))
	}
}

func TestCtrlLDispatchesModelCommand(t *testing.T) {
	m := newModel(80, 24)
	m = update(m, tea.WindowSizeMsg{Width: 80, Height: 24})
	m = update(m, tea.KeyMsg{Type: tea.KeyCtrlL})
	if len(m.history) == 0 {
		t.Fatalf("Ctrl+L should have appended a sys-history entry, got none")
	}
	if m.history[len(m.history)-1].who != "sys" {
		t.Errorf("last history who = %q, want sys", m.history[len(m.history)-1].who)
	}
}

func TestCtrlUnderscoreTogglesHelp(t *testing.T) {
	m := newModel(80, 24)
	if m.helpOpen {
		t.Fatalf("helpOpen should default to false")
	}
	m = update(m, tea.KeyMsg{Type: tea.KeyCtrlUnderscore})
	if !m.helpOpen {
		t.Fatalf("Ctrl+/ should have toggled helpOpen on")
	}
	m = update(m, tea.KeyMsg{Type: tea.KeyCtrlUnderscore})
	if m.helpOpen {
		t.Fatalf("Ctrl+/ should have toggled helpOpen back off")
	}
}
