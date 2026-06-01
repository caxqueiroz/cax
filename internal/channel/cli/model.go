package cli

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/caxqueiroz/cax/internal/channel"
	"github.com/caxqueiroz/cax/internal/config"
	"github.com/caxqueiroz/cax/internal/creator"
	"github.com/caxqueiroz/cax/internal/hooks"
	"github.com/caxqueiroz/cax/internal/plugins"
	"github.com/caxqueiroz/cax/internal/projectroot"
	"github.com/caxqueiroz/cax/internal/tasks"
	"github.com/caxqueiroz/cax/internal/theme"
)

// scheduleBackend is the store-backed CRUD surface the /schedule command drives.
// It is satisfied in cmd/cax by an adapter over memory.Store + scheduler so the
// CLI package depends only on config, not on the scheduler package.
type scheduleBackend interface {
	List(ctx context.Context) ([]config.ScheduleConfig, error)
	Upsert(ctx context.Context, sc config.ScheduleConfig) error
	Reload(ctx context.Context) error
}

// pluginBackend is the surface the /plugin command drives. It mirrors
// scheduleBackend's split: the CLI depends only on this minimal contract;
// cmd/cax wires a real plugins.Manager adapter. Every mutation triggers
// Rebuild so the agent picks up new contributions on the next turn.
type pluginBackend interface {
	List(ctx context.Context) ([]PluginListItem, error)
	Install(ctx context.Context, gitURL, name string) error
	Enable(ctx context.Context, name string) error
	Disable(ctx context.Context, name string) error
	Remove(ctx context.Context, name string) error
	Rebuild(ctx context.Context) error
}

// workspaceBackend is the surface the /workspace command drives. It mirrors
// the workspace.Workspace public API without forcing the cli package to
// depend on internal/workspace directly (avoids an import cycle if the
// adapter ever wraps something richer).
type workspaceBackend interface {
	List() []WorkspaceEntry
	Add(name, path string) (WorkspaceEntry, error)
	Remove(nameOrPath string) (WorkspaceEntry, error)
	Discover(start string) (root string, children []WorkspaceEntry, err error)
}

// WorkspaceEntry is the projection of workspace.Entry the CLI renders.
type WorkspaceEntry struct {
	Name string
	Path string
}

// factsBackend is the surface the /facts command drives. It exposes the
// memory.Store's fact CRUD without forcing the CLI to import internal/memory.
// cmd/cax wires a thin adapter over memory.Store; tests can leave it nil
// (the command then reports facts aren't wired).
type factsBackend interface {
	List(ctx context.Context, sessionID string, limit int) ([]FactDisplay, error)
	Search(ctx context.Context, sessionID, query string, k int) ([]FactDisplay, error)
	Clear(ctx context.Context, sessionID string) (int, error)
}

// FactDisplay is the projection of memory.Fact the CLI renders. It lives in
// the cli package so internal/memory is not imported here.
type FactDisplay struct {
	ID        int64
	Text      string
	Kind      string
	UpdatedAt time.Time
}

// creatorBackend is the surface the /new wizard drives. Implementations call
// the shared Writer + Reloader pair (cmd/cax wires the real one over the
// same Writer + Reloader the create_* FuncTools use, so /new and
// natural-language requests produce identical files). Nil-safe: when
// unwired, the wizard's confirm step reports the missing backend.
type creatorBackend interface {
	CreateSkill(ctx context.Context, name, description, body string) (path string, err error)
	CreateAgent(ctx context.Context, name, description string, tools []string, body string) (path string, err error)
	CreateCommand(ctx context.Context, name, description, argumentHint, body string) (path string, err error)
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
// reply, or system message — distinguished by who). duration is non-zero
// for assistant entries and reports how long the turn took to complete.
type historyEntry struct {
	who      string // "you" | "bot" | "sys"
	text     string
	duration time.Duration
	// rendered caches the width-dependent display block for this entry.
	// Populated lazily by renderConversation; invalidated en masse when
	// the viewport width changes (cache stamped at renderedWidth).
	rendered      string
	renderedWidth int
}

// --- custom messages pushed in from the worker goroutine via program.Send ---

// streamDeltaMsg carries a streamed text fragment for the in-flight reply.
type streamDeltaMsg struct{ text string }

// inputTokensMsg delivers the estimated prompt-side token count for the
// current turn — emitted once by the agent's PreGeneration hook after the
// memory/recall/facts/code_search blocks have been assembled. The spinner
// row uses it to show "↑ N · ↓ M" instead of just the downstream count.
type inputTokensMsg struct{ tokens int }

// turnUsageMsg delivers the AUTHORITATIVE token counts from the provider's
// response usage payload — emitted once by the agent's PostGeneration hook
// at turn end. Replaces the provisional PreGeneration estimate (which was
// chars/4) with the real numbers so the badge ↑N ↓M is accurate.
type turnUsageMsg struct {
	input  int
	output int
}

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
// summarized, when non-empty, holds the sys notice text emitted by the
// agent's PostGeneration hook if memory compaction fired this turn.
type turnDoneMsg struct {
	reply      string
	err        error
	summarized string
}

// statusMsg delivers a fresh dashboard snapshot.
type statusMsg struct {
	status channel.Status
	err    error
}

// tasksUpdateMsg carries a fresh snapshot of the agent-maintained task list,
// pushed by tasks.Board.Subscribe when the tasks_set tool fires.
type tasksUpdateMsg struct {
	tasks []tasks.Task
}

// allTasksTerminal reports whether every task in ts is completed or failed
// (no pending, no in_progress). Used by the turnDoneMsg handler to decide
// whether to auto-hide the panel.
func allTasksTerminal(ts []tasks.Task) bool {
	if len(ts) == 0 {
		return false
	}
	for _, t := range ts {
		if t.Status != tasks.StatusCompleted && t.Status != tasks.StatusFailed {
			return false
		}
	}
	return true
}

// submitMsg is produced internally when the user presses Enter; it carries the
// submitted line so cli.go can dispatch it to the worker.
type submitMsg struct{ line string }

// statusRequestMsg asks the program wrapper (cli.go) to fetch a fresh status
// snapshot out of band; the pure model treats it as a no-op.
type statusRequestMsg struct{}

// sysHistoryMsg appends a one-line sys notice to the conversation.
type sysHistoryMsg struct{ text string }

// execDoneMsg is posted by the /code (and any future) external-program
// callback when the suspended process exits. The Update handler appends the
// optional sys notice AND re-enables alt-screen + mouse-cell-motion, because
// tea.ExecProcess doesn't always restore those terminal modes on resume —
// without this the in-app viewport stops capturing the scroll wheel and the
// user scrolls the terminal's underlying scrollback instead.
type execDoneMsg struct{ notice string }

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

	sched       scheduleBackend       // optional; nil when the scheduler isn't wired
	plugins     pluginBackend         // optional; nil when /plugin is not wired
	creator     creatorBackend        // optional; nil when /new wizard finalize is not wired
	facts       factsBackend          // optional; nil when memory.mode == snippets or backend isn't wired
	workspace   workspaceBackend      // optional; powers /workspace command + agent code_search fan-out
	projectRoot *projectroot.Resolver // optional; powers /cwd + per-turn code_search root

	// wizard holds an in-progress /new flow. Nil when no /new is active —
	// existing tests that drive submit() with plain text keep passing because
	// submit checks this pointer before routing through Advance.
	wizard *creator.Wizard

	// pendingPerm holds an in-flight permission request from the agent. While
	// non-nil the input is locked and key handlers route y/Enter -> allow,
	// n/Esc -> deny. View renders an inline banner over the conversation.
	pendingPerm *pendingPermission

	// permDialog is the live TUI permission dialog. /permissions toggles its
	// require-confirm flag; nil leaves /permissions as read-only "unavailable".
	permDialog *PermDialog

	// completion tracks the live slash-command autocomplete dropdown. Empty
	// matches = no dropdown visible.
	completion completionState

	// pendingExec is a tea.Cmd queued by a slash command (e.g. /code) that
	// needs to suspend the TUI to run an external program. The dispatch
	// site picks it up after handleCommand returns and clears the field.
	pendingExec tea.Cmd

	// showStreaming mirrors cli.show_streaming in config. When false the
	// model discards streamDeltaMsg events; the spinner stays up and the
	// full reply lands at once on turnDoneMsg.
	showStreaming bool

	// sessionStart is set in newModel and used by the status row's `up N`
	// timer. turnStart is the wall clock at submit; turnDone computes the
	// turn duration as time.Since(turnStart).
	sessionStart time.Time
	turnStart    time.Time

	// turnGerund is the playful action label rendered on the spinner line
	// during a turn (e.g. "Shimmying"). Picked at submit time so it stays
	// stable for the whole turn instead of jittering each spinner tick.
	turnGerund string

	// turnInputTokens is the estimated prompt token count for the active
	// turn, delivered once by the agent's PreGeneration hook via an
	// inputTokensMsg. 0 = not yet known. Reset on submitMsg. Overwritten
	// at turn end with the authoritative count from turnUsageMsg.
	turnInputTokens int

	// turnOutputTokens is the AUTHORITATIVE downstream token count from
	// the provider's response usage. Populated by turnUsageMsg at turn
	// end. Persists between turns so the badge keeps showing "↓ N" until
	// the next turn lands a new count.
	turnOutputTokens int

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

	// mouseGrabbed tracks the current mouse-capture state. True (default)
	// means the scroll wheel + drag route into the bubbletea program (so
	// scrolling the conversation pane works); false means the terminal
	// handles them natively (so text selection / copy works). Ctrl+G
	// toggles. Initialized to true to match the tea.WithMouseCellMotion
	// option on program construction.
	mouseGrabbed bool

	// taskList mirrors tasks.Board for live render in the sticky panel above
	// the input. Empty list → panel is hidden. Populated by tasksUpdateMsg.
	taskList []tasks.Task

	ready bool // viewport sized at least once
}

func newModel(width, height int) model {
	ta := textarea.New()
	// Per-line prompt: ❯ on row 0 only, blank on every other row. The
	// default bubbles behaviour shows the Prompt on every visible row
	// which makes the 5-row input look like a stack of empty `❯` lines.
	// SetPromptFunc supersedes the Prompt field entirely.
	ta.SetPromptFunc(2, func(lineIdx int) string {
		if lineIdx == 0 {
			return "❯ "
		}
		return "  "
	})
	// Placeholder on the cursor row (Claude Code style) — inline next to ❯,
	// dim, disappears as you type. Bubbles renders this on row 0 only.
	ta.Placeholder = "type a message, or / for commands"
	ta.CharLimit = 4000
	ta.ShowLineNumbers = false
	// Disable Enter→newline so Enter falls through to model.Update's explicit
	// KeyEnter arm (which calls m.submit). Newline insertion is done by the
	// model when it sees Alt+Enter. ctrl+m is the carriage-return alias
	// bubbles bundles with Enter; we clear both at once by passing no keys.
	ta.KeyMap.InsertNewline = key.NewBinding(key.WithKeys())
	ta.SetWidth(max(1, width-2))
	ta.SetHeight(5) // big-by-default input; grows up to 10 in resizeInput
	ta.MaxHeight = 10 // matches resizeInput's growth ceiling
	// Focus before the textarea is copied into the model value. Calling
	// Focus() inside a value-receiver Init() mutates only the local copy and
	// leaves the real input unfocused — which causes bubbles' textarea to
	// drop every character key.
	_ = ta.Focus()

	sp := spinner.New()
	// Custom shimmer glyphs (✶✷…✿) so the spinner reads as a small twinkling
	// asterisk — pairs with the "Shimmying…" gerund line.
	sp.Spinner = spinner.Spinner{
		Frames: []string{"✶", "✷", "✸", "✹", "✺", "✻", "✼", "✽", "✾", "✿", "✾", "✽", "✼", "✻", "✺", "✹", "✸", "✷"},
		FPS:    time.Second / 10,
	}
	// Spinner color is a static "always dim" — the themed bag is per-render
	// but spinner.Tick captures Style at scheduling time, so we use a fixed
	// approximation rather than the active theme's Dim.
	sp.Style = lipgloss.NewStyle().Foreground(lipgloss.Color("244"))

	// Initial viewport sizing matches the v2 layout (see resizeInput for the
	// authoritative math). On the first WindowSizeMsg these get rewritten
	// against the real terminal size; the values here only have to be sane
	// before the first message arrives so refreshViewport doesn't panic.
	vpW := max(1, width-2*len(leftIndent)-2-4) // -border -2*padX
	vpH := max(1, height-9-1)                  // -fixed chrome -input(1)
	vp := viewport.New(vpW, vpH)

	return model{
		width:         width,
		height:        height,
		input:         ta,
		viewport:      vp,
		spinner:       sp,
		sessionStart:  time.Now(),
		showStreaming: true, // overridden by cli.WithShowStreaming if config sets it
		mouseGrabbed:  true, // matches tea.WithMouseCellMotion in cli.Start
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
		// Conversation viewport inner width = boxOuter − 2 (border) − 4 (padX*2).
		// Message textarea inner width = boxOuter − 2 (border) − 2 (padX*2).
		boxOuter := max(msg.Width-2*len(leftIndent), 16)
		m.viewport.Width = max(1, boxOuter-2-4)
		m.input.SetWidth(max(1, boxOuter-2-2))
		m.resizeInput()
		m.ready = true
		m.refreshViewport()
		return m, nil

	case permRequestMsg:
		m.pendingPerm = &pendingPermission{title: msg.title, message: msg.message, response: msg.response}
		m.refreshViewport()
		return m, nil

	case tea.KeyMsg:
		// Permission gate is modal: while waiting, accept only y/Enter (allow)
		// and n/Esc (deny); swallow every other key so it doesn't leak into
		// the input or trigger another command.
		if m.pendingPerm != nil {
			// Grant path: clear the modal silently. The tool's actual output
			// follows in the next stream chunk — printing "✓ permission
			// granted: Bash" once per Bash call (×N per turn) was just noise.
			// Deny still logs because the tool didn't run; the user needs to
			// see why.
			switch msg.Type {
			case tea.KeyEnter:
				m.pendingPerm.answer(true)
				m.pendingPerm = nil
			case tea.KeyEsc:
				m.pendingPerm.answer(false)
				m.history = append(m.history, historyEntry{who: "sys", text: "✗ permission denied: " + m.pendingPerm.title})
				m.pendingPerm = nil
			case tea.KeyRunes:
				switch strings.ToLower(string(msg.Runes)) {
				case "y":
					m.pendingPerm.answer(true)
					m.pendingPerm = nil
				case "n":
					m.pendingPerm.answer(false)
					m.history = append(m.history, historyEntry{who: "sys", text: "✗ permission denied: " + m.pendingPerm.title})
					m.pendingPerm = nil
				}
			case tea.KeyCtrlC:
				m.pendingPerm.answer(false)
				m.pendingPerm = nil
				return m, tea.Quit
			}
			m.refreshViewport()
			return m, nil
		}
		// Completion dropdown intercepts navigation keys when visible.
		if len(m.completion.matches) > 0 {
			switch msg.Type {
			case tea.KeyTab, tea.KeyEnter:
				// Enter on a visible dropdown accepts the highlighted match
				// instead of submitting the partial text the user typed to
				// trigger the dropdown. To submit, press Esc first (closes
				// the dropdown) and then Enter — or press Enter twice
				// (this accepts, then the next Enter submits the completed
				// command).
				if m.completion.idx < len(m.completion.matches) {
					m.applyCompletion(m.completion.matches[m.completion.idx].name)
				}
				return m, nil
			case tea.KeyUp:
				if m.completion.idx > 0 {
					m.completion.idx--
				}
				return m, nil
			case tea.KeyDown:
				if m.completion.idx < len(m.completion.matches)-1 {
					m.completion.idx++
				}
				return m, nil
			case tea.KeyEsc:
				m.completion.matches = nil
				m.resizeInput()
				return m, nil
			}
		}
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
			out, _, _ := m.handleCommand("/model")
			if out != "" {
				m.history = append(m.history, historyEntry{who: "sys", text: out})
				m.refreshViewport()
			}
			return m, nil
		case tea.KeyCtrlR:
			out, _, _ := m.handleCommand("/reload")
			if out != "" {
				m.history = append(m.history, historyEntry{who: "sys", text: out})
				m.refreshViewport()
			}
			return m, nil
		case tea.KeyCtrlT:
			m.cycleTheme()
			m.refreshViewport()
			return m, nil
		case tea.KeyCtrlG:
			// Toggle mouse capture so the user can drag-select text in
			// the terminal without needing platform-specific modifiers
			// (Option on macOS, Shift on Windows Terminal). When released,
			// scroll wheel reverts to terminal scrollback; when grabbed,
			// scroll routes to the in-app viewport again.
			m.mouseGrabbed = !m.mouseGrabbed
			var msg string
			var cmd tea.Cmd
			if m.mouseGrabbed {
				cmd = tea.EnableMouseCellMotion
				msg = "mouse: grabbed (scroll wheel scrolls cax; Ctrl+G to release for native selection)"
			} else {
				cmd = tea.DisableMouse
				msg = "mouse: released (drag to select text natively; Ctrl+G to grab again for scroll)"
			}
			m.history = append(m.history, historyEntry{who: "sys", text: msg})
			m.refreshViewport()
			return m, cmd
		case tea.KeyCtrlE:
			// /code shortcut — open the last assistant reply's code in the
			// pager so the user can use the terminal's native mouse to
			// select & copy.
			out, _, _ := m.handleCommand("/code")
			if out != "" {
				m.history = append(m.history, historyEntry{who: "sys", text: out})
				m.refreshViewport()
			}
			if cmd := m.pendingExec; cmd != nil {
				m.pendingExec = nil
				return m, cmd
			}
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
		// When streaming is disabled (cli.show_streaming: false), drop the
		// delta on the floor — the spinner keeps animating; the full reply
		// arrives on turnDoneMsg and lands in one shot.
		if !m.showStreaming {
			return m, nil
		}
		m.stream += msg.text
		m.refreshViewport()
		return m, nil

	case inputTokensMsg:
		m.turnInputTokens = msg.tokens
		m.refreshViewport()
		return m, nil

	case turnUsageMsg:
		m.turnInputTokens = msg.input
		m.turnOutputTokens = msg.output
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
		dur := time.Duration(0)
		if !m.turnStart.IsZero() {
			dur = time.Since(m.turnStart)
		}
		m.turnStart = time.Time{}
		if msg.err != nil {
			m.lastErr = msg.err.Error()
		} else {
			text := msg.reply
			if text == "" {
				text = m.stream
			}
			m.history = append(m.history, historyEntry{who: "bot", text: text, duration: dur})
		}
		if msg.summarized != "" {
			// Render the memory-compaction notice after the bot reply so the
			// order in the transcript matches reality (model finished, then
			// the hook condensed older messages).
			m.history = append(m.history, historyEntry{who: "sys", text: "✂ " + msg.summarized})
		}
		// Auto-hide the task panel when every task reached a terminal state.
		// Leaves the panel up if the model left work in_progress or pending —
		// that's a signal something didn't get closed out, surface it.
		if allTasksTerminal(m.taskList) {
			m.taskList = nil
		}
		m.stream = ""
		m.refreshViewport()
		return m, requestStatus

	case tasksUpdateMsg:
		m.taskList = msg.tasks
		m.refreshViewport()
		return m, nil

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

	case sysHistoryMsg:
		// Posted from in-process notices (e.g. summarisation events).
		m.history = append(m.history, historyEntry{who: "sys", text: msg.text})
		m.refreshViewport()
		return m, nil

	case execDoneMsg:
		// External program (pager/editor from /code) just exited. Re-enable
		// alt-screen + mouse-cell-motion so the in-app viewport captures the
		// scroll wheel again — otherwise the wheel falls through to the
		// terminal's scrollback and the user sees the pre-cax shell history.
		if msg.notice != "" {
			m.history = append(m.history, historyEntry{who: "sys", text: msg.notice})
			m.refreshViewport()
		}
		return m, tea.Batch(tea.EnterAltScreen, tea.EnableMouseCellMotion)

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
	prevInput := m.input.Value()
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	cmds = append(cmds, cmd)
	m.viewport, cmd = m.viewport.Update(msg)
	cmds = append(cmds, cmd)
	if m.input.Value() != prevInput {
		// Recompute completions whenever the buffer changes. Idx resets to 0
		// so the top match is always the default Tab target.
		m.completion.matches = m.completionFor(m.input.Value())
		m.completion.idx = 0
	}
	m.resizeInput()
	return m, tea.Batch(cmds...)
}

// submit handles Enter. The dispatch order is:
//  1. slash commands run through handleCommand (which may install a wizard).
//  2. when a wizard is active and the input is NOT a slash command, the line
//     advances the wizard one step; on confirm the creator backend is invoked.
//  3. otherwise the line is echoed and a submitMsg fires so cli.go runs the turn.
//
// Routing wizards before plain text means a /new flow never triggers a
// streaming turn — required so users can type free-form descriptions without
// the agent racing ahead.
func (m model) submit() (tea.Model, tea.Cmd) {
	line := strings.TrimSpace(m.input.Value())
	m.input.Reset()
	m.resizeInput()
	if line == "" {
		return m, nil
	}
	if strings.HasPrefix(line, "/") {
		out, quit, wiz := m.handleCommand(line)
		if quit {
			return m, tea.Quit
		}
		if wiz != nil {
			m.wizard = wiz
		}
		if out != "" {
			m.history = append(m.history, historyEntry{who: "sys", text: out})
			m.refreshViewport()
		}
		// A slash command may have queued an external program (e.g. /code).
		// Pick it up, clear, and run it — the TUI suspends until it exits.
		if cmd := m.pendingExec; cmd != nil {
			m.pendingExec = nil
			return m, cmd
		}
		return m, nil
	}
	if m.wizard != nil {
		return m.advanceWizard(line)
	}
	wasEmpty := len(m.history) == 0
	m.history = append(m.history, historyEntry{who: "you", text: line})
	m.streaming = true
	m.stream = ""
	m.lastErr = ""
	m.turnStart = time.Now()
	m.turnGerund = pickGerund()
	m.turnInputTokens = 0
	if wasEmpty {
		// Welcome card just disappeared — recompute viewport so the conv box
		// can claim the freed rows on the very next render.
		m.resizeInput()
	}
	m.refreshViewport()
	return m, tea.Batch(emitSubmit(line), m.spinner.Tick)
}

// advanceWizard pushes one line through the active wizard. On the confirm
// step (transition to WizardStepDone) the model dispatches to the creator
// backend and clears the wizard. The wizard line itself is echoed as a "you"
// entry so the user has a transcript of what they answered.
func (m model) advanceWizard(line string) (tea.Model, tea.Cmd) {
	m.history = append(m.history, historyEntry{who: "you", text: line})
	prompt := m.wizard.Advance(line)
	if m.wizard.Step != creator.WizardStepDone {
		m.history = append(m.history, historyEntry{who: "sys", text: prompt})
		m.refreshViewport()
		return m, nil
	}
	// Done: dispatch to the backend if confirmed; otherwise just clear.
	w := m.wizard
	m.wizard = nil
	if !w.Confirmed() {
		m.history = append(m.history, historyEntry{who: "sys", text: "/new: cancelled (declined at confirm)"})
		m.refreshViewport()
		return m, nil
	}
	if m.creator == nil {
		m.history = append(m.history, historyEntry{who: "sys", text: "/new: creator backend not wired (cli.WithCreator not set)"})
		m.refreshViewport()
		return m, nil
	}
	ctx := context.Background()
	var (
		path string
		err  error
	)
	switch w.Kind {
	case creator.WizardKindSkill:
		path, err = m.creator.CreateSkill(ctx, w.Name, w.Description, w.Body)
	case creator.WizardKindAgent:
		path, err = m.creator.CreateAgent(ctx, w.Name, w.Description, w.Tools, w.Body)
	case creator.WizardKindCommand:
		path, err = m.creator.CreateCommand(ctx, w.Name, w.Description, w.ArgumentHint, w.Body)
	}
	if err != nil {
		m.history = append(m.history, historyEntry{who: "sys", text: fmt.Sprintf("/new failed: %v", err)})
	} else {
		m.history = append(m.history, historyEntry{who: "sys", text: fmt.Sprintf("wrote %s; agent reloaded", path)})
	}
	m.refreshViewport()
	return m, nil
}

// resizeInput resizes the textarea to fit its current content (clamped to
// [1,6]) and recomputes the conversation viewport height around it.
//
// v2 layout chrome (per WindowSizeMsg):
//
//	header box       3 (top+content+bottom)
//	blank            1
//	conversation box N (this is what we size; min 4)
//	blank            1
//	status row       1
//	blank            1
//	message box      input.Height()+2 (top+bottom border)
//	hint line        1 (skipped when m.height < minHintHeight)
//	─────────────
//	total            = msg.Height
//
// Conversation box outer height = m.height - 9 - input.Height() (or 8 when
// the hint line is dropped). The viewport sits INSIDE the box, so its
// height is boxOuterHeight - 2 (border) - 2 (Padding(1,2) rows).
func (m *model) resizeInput() {
	// Floor of 5 rows so the input always looks substantial, even when
	// empty. Grows up to 10 as the user types multi-line content.
	h := max(m.input.LineCount(), 5)
	if h > 10 {
		h = 10
	}
	if m.input.Height() != h {
		m.input.SetHeight(h)
	}
	// New layout (Option A): conv + blank + message(h+2) + blank + footer(1).
	// No welcome card, no external status row, no tasks panel (tasks render
	// inside the conversation now).
	fixed := 1 + 2 + 1 + 1 // blank + msg-border + blank + footer
	if vis := len(m.completion.matches); vis > 0 {
		if vis > 7 {
			vis = 7
		}
		fixed += vis + 1 // dropdown replaces the footer when visible
	}
	boxOuter := max(m.height-fixed-h, 4)
	vpH := max(boxOuter-2-2, 1)
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
