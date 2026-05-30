package cli

import (
	"context"
	"log/slog"
	"strings"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/caxqueiroz/czcli/internal/channel"
	"github.com/caxqueiroz/czcli/internal/config"
	"github.com/caxqueiroz/czcli/internal/hooks"
	"github.com/caxqueiroz/czcli/internal/plugins"
	"github.com/caxqueiroz/czcli/internal/theme"
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

// historyEntry is one rendered conversation line (user prompt, assistant
// reply, or system message — distinguished by who).
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

	input    textarea.Model
	viewport viewport.Model
	spinner  spinner.Model

	history   []historyEntry
	stream    string // in-flight assistant text being streamed
	streaming bool

	status    channel.Status
	hasStatus bool
	running   []string // running subagent names (live)
	lastErr   string

	sched   scheduleBackend // optional; nil when the scheduler isn't wired
	plugins pluginBackend   // optional; nil when /plugin is not wired

	// hookEntries is the typed snapshot of plugin-declared hooks the /hooks
	// command renders. Populated via WithHookEntries on CLI start; nil when
	// no plugin contributes hooks. Status.HookCount remains the source of
	// truth for the bottom-bar counter.
	hookEntries []hooks.Entry

	// userCommands is the merged user+plugin command snapshot used by the
	// /help overlay listing. The dispatcher itself routes through the
	// existing handleCommand switch; this slice is rendering-only.
	userCommands []plugins.PluginCommand

	// themeStateFile is the path Ctrl+T persists the active theme to. Empty
	// disables persistence (Ctrl+T still cycles in-memory).
	themeStateFile string

	// helpOpen toggles the keybindings/commands overlay (Ctrl+/).
	helpOpen bool

	ready bool // viewport sized at least once
}

func newModel(width, height int) model {
	ta := textarea.New()
	ta.Prompt = "❯ "
	ta.Placeholder = "type a message, or /stats /tools /agents /schedule /model /skills /mcp /lsp /plugin /hooks /reload"
	ta.CharLimit = 4000
	ta.ShowLineNumbers = false
	// Disable Enter→newline so Enter falls through to model.Update's explicit
	// KeyEnter arm (which calls m.submit). Newline insertion is done by the
	// model when it sees Alt+Enter. ctrl+m is the carriage-return alias
	// bubbles bundles with Enter; we clear both at once by passing no keys.
	ta.KeyMap.InsertNewline = key.NewBinding(key.WithKeys())
	ta.SetWidth(max(1, width-2))
	ta.SetHeight(1)
	ta.MaxHeight = 6
	// Focus before the textarea is copied into the model value. Calling
	// Focus() inside a value-receiver Init() mutates only the local copy and
	// leaves the real input unfocused — which causes bubbles' textarea to
	// drop every character key.
	_ = ta.Focus()

	sp := spinner.New()
	sp.Spinner = spinner.Dot
	// Spinner color is a static "always dim" — the themed bag is per-render
	// but spinner.Tick captures Style at scheduling time, so we use a fixed
	// approximation rather than the active theme's Dim.
	sp.Style = lipgloss.NewStyle().Foreground(lipgloss.Color("244"))

	vp := viewport.New(width, max(1, height-7))

	return model{
		width:    width,
		height:   height,
		input:    ta,
		viewport: vp,
		spinner:  sp,
	}
}

func (m model) Init() tea.Cmd {
	// textarea is already focused by newModel; start the cursor blink.
	return textarea.Blink
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd

	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.viewport.Width = msg.Width
		m.input.SetWidth(max(1, msg.Width-2))
		m.resizeInput()
		m.ready = true
		m.refreshViewport()
		return m, nil

	case tea.KeyMsg:
		// Help overlay (Ctrl+/). Most terminals emit Ctrl+/ as KeyCtrlUnderscore.
		if msg.Type == tea.KeyCtrlUnderscore {
			m.helpOpen = !m.helpOpen
			m.refreshViewport()
			return m, nil
		}
		switch msg.Type {
		case tea.KeyCtrlC, tea.KeyEsc:
			return m, tea.Quit
		case tea.KeyCtrlL:
			out, _ := m.handleCommand("/model")
			if out != "" {
				m.history = append(m.history, historyEntry{who: "sys", text: out})
				m.refreshViewport()
			}
			return m, nil
		case tea.KeyCtrlR:
			out, _ := m.handleCommand("/reload")
			if out != "" {
				m.history = append(m.history, historyEntry{who: "sys", text: out})
				m.refreshViewport()
			}
			return m, nil
		case tea.KeyCtrlT:
			m.cycleTheme()
			m.refreshViewport()
			return m, nil
		case tea.KeyEnter:
			if msg.Alt {
				// Alt+Enter = newline (multi-line input). Bubbletea v1.3.10
				// surfaces this as KeyMsg{Type: KeyEnter, Alt: true}; Shift+Enter
				// is not distinguishable from bare Enter on standard terminals.
				m.input.InsertRune('\n')
				m.resizeInput()
				return m, nil
			}
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

	case spinner.TickMsg:
		// Animate while waiting for the assistant's reply; stop once streaming ends.
		if !m.streaming {
			return m, nil
		}
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		m.refreshViewport()
		return m, cmd
	}

	// Delegate remaining keys to the input; scroll keys to the viewport.
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	cmds = append(cmds, cmd)
	m.viewport, cmd = m.viewport.Update(msg)
	cmds = append(cmds, cmd)
	m.resizeInput()
	return m, tea.Batch(cmds...)
}

// submit handles Enter: slash-commands are dispatched locally; plain text is
// echoed and a submitMsg is emitted so cli.go can run the turn.
func (m model) submit() (tea.Model, tea.Cmd) {
	line := strings.TrimSpace(m.input.Value())
	m.input.Reset()
	m.resizeInput()
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
	return m, tea.Batch(emitSubmit(line), m.spinner.Tick)
}

// resizeInput resizes the textarea to fit its current content (clamped to
// [1,6]) and recomputes viewport height around it.
func (m *model) resizeInput() {
	h := m.input.LineCount()
	if h < 1 {
		h = 1
	}
	if h > 6 {
		h = 6
	}
	if m.input.Height() != h {
		m.input.SetHeight(h)
	}
	// Layout: top(1)+sep(1)+viewport+sep(1)+bottom(1)+sep(1)+pad(1)+input(h)
	// total fixed chrome = 6, plus input.
	vpH := m.height - 6 - h
	if vpH < 1 {
		vpH = 1
	}
	m.viewport.Height = vpH
}

// cycleTheme advances to the next theme in registry order and persists the
// choice via writeThemeState. Errors are logged; cycling itself never fails.
func (m *model) cycleTheme() {
	next := theme.Cycle()
	if next == nil {
		return
	}
	theme.Set(next)
	if m.themeStateFile != "" {
		if err := writeThemeState(m.themeStateFile, next.Name); err != nil {
			slog.Warn("cli: persist theme state", "file", m.themeStateFile, "error", err)
		}
	}
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
