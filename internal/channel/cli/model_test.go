package cli

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/caxqueiroz/cax/internal/channel"
	"github.com/caxqueiroz/cax/internal/creator"
	"github.com/caxqueiroz/cax/internal/theme"
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
	// v2 layout: viewport sits INSIDE the conversation box, so its width is
	// the inner area, not the terminal width. boxOuter = 100 − 2*2 (global
	// indent) = 96; viewport inner = 96 − 2 (border) − 4 (padX*2) = 90.
	if m.viewport.Width != 90 {
		t.Errorf("viewport width = %d, want 90 (inner area of conversation box)", m.viewport.Width)
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
	_ = update(m, tea.KeyMsg{Type: tea.KeyCtrlT})
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

// feedLine simulates "user types <line>, presses Enter" through the model's
// submit path. The wizard tests use it to drive the description → tools →
// body → confirm machine without spinning up bubbletea.
func feedLine(m model, line string) model {
	m.input.SetValue(line)
	next, _ := m.submit()
	return next.(model)
}

func TestNewWizard_SkillHappyPath_ViaSubmit(t *testing.T) {
	fb := &fakeCreatorBackend{}
	m := newModel(80, 24)
	m.creator = fb
	m = update(m, tea.WindowSizeMsg{Width: 80, Height: 24})

	// /new skill explain-go-embedding installs the wizard and prints the
	// description prompt.
	m.input.SetValue("/new skill explain-go-embedding")
	next, _ := m.submit()
	m = next.(model)
	if m.wizard == nil {
		t.Fatalf("/new skill should install a wizard")
	}
	if m.wizard.Step != creator.WizardStepDescription {
		t.Fatalf("expected description step; got %v", m.wizard.Step)
	}
	// During an active wizard, plain-text submit must NOT start a streaming
	// turn — verify before we feed input through it.
	m = feedLine(m, "Explain Go embedding succinctly.")
	if m.streaming {
		t.Fatalf("wizard submission must not start a streaming turn")
	}
	if m.wizard.Step != creator.WizardStepBody {
		t.Fatalf("after description, expected body step; got %v", m.wizard.Step)
	}
	m = feedLine(m, "Use a worked example.")
	if m.wizard.Step != creator.WizardStepConfirm {
		t.Fatalf("after body, expected confirm step; got %v", m.wizard.Step)
	}
	m = feedLine(m, "y")
	if fb.called != 1 || fb.kind != "skill" || fb.name != "explain-go-embedding" {
		t.Fatalf("backend not invoked correctly: %+v", fb)
	}
	if m.wizard != nil {
		t.Fatalf("wizard should clear after confirm; got: %+v", m.wizard)
	}
	if m.streaming {
		t.Fatalf("confirm step must not start a streaming turn")
	}
	last := m.history[len(m.history)-1].text
	if !strings.Contains(last, "wrote") || !strings.Contains(last, "explain-go-embedding") {
		t.Fatalf("expected success message naming the file; got: %q", last)
	}
}

func TestNewWizard_DeclineAtConfirmClears(t *testing.T) {
	fb := &fakeCreatorBackend{}
	m := newModel(80, 24)
	m.creator = fb
	m = update(m, tea.WindowSizeMsg{Width: 80, Height: 24})

	m.input.SetValue("/new skill foo")
	next, _ := m.submit()
	m = next.(model)
	m = feedLine(m, "desc")
	m = feedLine(m, "body")
	m = feedLine(m, "n") // decline
	if m.wizard != nil {
		t.Fatalf("wizard should clear after decline")
	}
	if fb.called != 0 {
		t.Fatalf("backend must not be called on decline; got %d", fb.called)
	}
}

func TestNewWizard_AgentRoutesToolsStep(t *testing.T) {
	fb := &fakeCreatorBackend{}
	m := newModel(80, 24)
	m.creator = fb
	m = update(m, tea.WindowSizeMsg{Width: 80, Height: 24})
	m.input.SetValue("/new agent reviewer")
	next, _ := m.submit()
	m = next.(model)
	m = feedLine(m, "Reviews Go diffs.")
	if m.wizard.Step != creator.WizardStepToolsOrHint {
		t.Fatalf("agent flow should hit tools step; got %v", m.wizard.Step)
	}
	m = feedLine(m, "Read, Glob")
	if m.wizard.Step != creator.WizardStepBody {
		t.Fatalf("after tools, expected body step; got %v", m.wizard.Step)
	}
	m = feedLine(m, "Be terse.")
	m = feedLine(m, "y")
	if fb.called != 1 || fb.kind != "agent" {
		t.Fatalf("backend not called correctly: %+v", fb)
	}
	if len(fb.tools) != 2 || fb.tools[0] != "Read" || fb.tools[1] != "Glob" {
		t.Fatalf("tools = %v, want [Read Glob]", fb.tools)
	}
}

func TestNewWizard_CancelMidFlowDoesNotInvokeBackend(t *testing.T) {
	fb := &fakeCreatorBackend{}
	m := newModel(80, 24)
	m.creator = fb
	m = update(m, tea.WindowSizeMsg{Width: 80, Height: 24})
	m.input.SetValue("/new skill foo")
	next, _ := m.submit()
	m = next.(model)
	m.input.SetValue("/cancel")
	next, _ = m.submit()
	m = next.(model)
	if m.wizard != nil {
		t.Fatalf("/cancel should clear the wizard")
	}
	if fb.called != 0 {
		t.Fatalf("backend must not be called when /cancel aborts; got %d", fb.called)
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
