package cli

import (
	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/caxqueiroz/czcli/internal/channel"
)

// historyEntry is one rendered conversation line ("you:" / "bot:" / system).
type historyEntry struct {
	who  string // "you" | "bot" | "sys"
	text string
}

// --- custom messages pushed in from the worker goroutine via program.Send ---

// streamDeltaMsg carries a streamed text fragment for the in-flight reply.
type streamDeltaMsg struct{ text string }

// toolEventMsg notes a tool starting/ending (Type from channel.StreamEvent).
type toolEventMsg struct {
	kind string // "tool_start" | "tool_end"
	name string
}

// subagentEventMsg notes a subagent starting/ending.
type subagentEventMsg struct {
	kind string // "subagent_start" | "subagent_end"
	name string
}

// turnDoneMsg signals the worker finished a turn with a final reply or error.
type turnDoneMsg struct {
	reply string
	err   error
}

// statusMsg delivers a fresh dashboard snapshot.
type statusMsg struct {
	status channel.Status
	err    error
}

// submitMsg is produced internally when the user presses Enter; it carries the
// submitted line so cli.go can dispatch it to the worker.
type submitMsg struct{ line string }

// statusRequestMsg asks the program wrapper (cli.go) to fetch a fresh status
// snapshot out of band; the pure model treats it as a no-op.
type statusRequestMsg struct{}

// model is the bubbletea model for the CLI channel.
type model struct {
	width  int
	height int

	input    textinput.Model
	viewport viewport.Model

	history   []historyEntry
	stream    string // in-flight assistant text being streamed
	streaming bool

	status    channel.Status
	hasStatus bool
	running   []string // running subagent names (live)
	lastErr   string

	ready bool // viewport sized at least once
}

func newModel(width, height int) model {
	ti := textinput.New()
	ti.Prompt = "> "
	ti.Placeholder = "type a message, or /stats /tools /agents /schedule /model"
	ti.CharLimit = 4000

	vp := viewport.New(width, max(1, height-6))

	return model{
		width:    width,
		height:   height,
		input:    ti,
		viewport: vp,
	}
}

func (m model) Init() tea.Cmd {
	return m.input.Focus()
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	_ = msg
	return m, nil
}
