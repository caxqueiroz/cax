package cli

import (
	"context"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/caxqueiroz/czcli/internal/channel"
	"github.com/caxqueiroz/czcli/internal/config"
)

// scheduleBackend is the store-backed CRUD surface the /schedule command drives.
// It is satisfied in cmd/czcli by an adapter over memory.Store + scheduler so the
// CLI package depends only on config, not on the scheduler package.
type scheduleBackend interface {
	List(ctx context.Context) ([]config.ScheduleConfig, error)
	Upsert(ctx context.Context, sc config.ScheduleConfig) error
	Reload(ctx context.Context) error
}

// pluginBackend is the surface the /plugin command drives. It mirrors
// scheduleBackend's split: the CLI depends only on this minimal contract;
// cmd/czcli wires a real plugins.Manager adapter. Every mutation triggers
// Rebuild so the agent picks up new contributions on the next turn.
type pluginBackend interface {
	List(ctx context.Context) ([]PluginListItem, error)
	Install(ctx context.Context, gitURL, name string) error
	Enable(ctx context.Context, name string) error
	Disable(ctx context.Context, name string) error
	Remove(ctx context.Context, name string) error
	Rebuild(ctx context.Context) error
}

// PluginListItem is the projection of plugins.PluginInfo the CLI renders. It
// lives in the cli package so internal/plugins is not imported here (mirror
// of the scheduleBackend pattern, which keeps cli package-clean of plugins).
type PluginListItem struct {
	Name       string
	Version    string
	Source     string
	Enabled    bool
	SkillCount int
	MCPCount   int
	LSPCount   int
	HookCount  int
	CmdCount   int
	AgentCount int
}

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

	sched   scheduleBackend // optional; nil when the scheduler isn't wired
	plugins pluginBackend   // optional; nil when /plugin is not wired

	ready bool // viewport sized at least once
}

func newModel(width, height int) model {
	ti := textinput.New()
	ti.Prompt = "> "
	ti.Placeholder = "type a message, or /stats /tools /agents /schedule /model /skills /mcp"
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
	var cmds []tea.Cmd

	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.viewport.Width = msg.Width
		m.viewport.Height = max(1, msg.Height-6)
		m.input.Width = max(1, msg.Width-2)
		m.ready = true
		m.refreshViewport()
		return m, nil

	case tea.KeyMsg:
		switch msg.Type {
		case tea.KeyCtrlC, tea.KeyEsc:
			return m, tea.Quit
		case tea.KeyEnter:
			return m.submit()
		}

	case streamDeltaMsg:
		m.stream += msg.text
		m.refreshViewport()
		return m, nil

	case toolEventMsg:
		// Surfaced via /tools; no inline echo to keep the pane clean.
		return m, nil

	case subagentEventMsg:
		switch msg.kind {
		case "subagent_start":
			m.running = append(m.running, msg.name)
		case "subagent_end":
			m.running = removeFirst(m.running, msg.name)
		}
		return m, nil

	case turnDoneMsg:
		m.streaming = false
		if msg.err != nil {
			m.lastErr = msg.err.Error()
		} else {
			text := msg.reply
			if text == "" {
				text = m.stream
			}
			m.history = append(m.history, historyEntry{who: "bot", text: text})
		}
		m.stream = ""
		m.refreshViewport()
		return m, requestStatus

	case statusMsg:
		if msg.err == nil {
			m.status = msg.status
			m.hasStatus = true
			if len(msg.status.RunningSubagents) > 0 {
				m.running = append([]string(nil), msg.status.RunningSubagents...)
			}
		}
		return m, nil

	case statusRequestMsg:
		// The program wrapper (cli.go) intercepts this to fetch status out of
		// band; the pure model treats it as a no-op.
		return m, nil
	}

	// Delegate remaining keys to the input; scroll keys to the viewport.
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	cmds = append(cmds, cmd)
	m.viewport, cmd = m.viewport.Update(msg)
	cmds = append(cmds, cmd)
	return m, tea.Batch(cmds...)
}

// submit handles Enter: slash-commands are dispatched locally; plain text is
// echoed and a submitMsg is emitted so cli.go can run the turn.
func (m model) submit() (tea.Model, tea.Cmd) {
	line := strings.TrimSpace(m.input.Value())
	m.input.Reset()
	if line == "" {
		return m, nil
	}
	if strings.HasPrefix(line, "/") {
		out, quit := m.handleCommand(line)
		if quit {
			return m, tea.Quit
		}
		if out != "" {
			m.history = append(m.history, historyEntry{who: "sys", text: out})
			m.refreshViewport()
		}
		return m, nil
	}
	m.history = append(m.history, historyEntry{who: "you", text: line})
	m.streaming = true
	m.stream = ""
	m.lastErr = ""
	m.refreshViewport()
	return m, emitSubmit(line)
}

func emitSubmit(line string) tea.Cmd {
	return func() tea.Msg { return submitMsg{line: line} }
}

// requestStatus is a sentinel cmd whose statusRequestMsg the program wrapper in
// cli.go intercepts to run the StatusFunc out of band.
func requestStatus() tea.Msg { return statusRequestMsg{} }

func removeFirst(xs []string, v string) []string {
	for i, x := range xs {
		if x == v {
			return append(xs[:i:i], xs[i+1:]...)
		}
	}
	return xs
}
