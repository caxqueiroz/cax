// Package cli implements channel.Channel as a Bubble Tea terminal UI: a top
// status bar (model + fallback + context gauge), a scrolling conversation pane,
// a bottom status bar (token usage + memory size + tool/subagent counts), and an
// input line that streams the agent's reply live and supports slash-commands.
package cli

import (
	"context"
	"fmt"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/caxqueiroz/czcli/internal/channel"
	"github.com/caxqueiroz/czcli/internal/hooks"
)

// CLI satisfies channel.Channel.
var _ channel.Channel = (*CLI)(nil)

// CLI is the Bubble Tea implementation of channel.Channel.
type CLI struct {
	sessionID      string
	statusInterval time.Duration
	sched          scheduleBackend
	plugins        pluginBackend
	hookEntries    []hooks.Entry
}

// Option configures a CLI.
type Option func(*CLI)

// WithSessionID sets the session id used for every inbound message.
func WithSessionID(id string) Option { return func(c *CLI) { c.sessionID = id } }

// WithStatusInterval sets how often the dashboard is refreshed while idle.
func WithStatusInterval(d time.Duration) Option {
	return func(c *CLI) { c.statusInterval = d }
}

// WithScheduler wires the store-backed /schedule CRUD backend. When unset, the
// /schedule command reports that scheduling is not available.
func WithScheduler(b scheduleBackend) Option {
	return func(c *CLI) { c.sched = b }
}

// WithPlugins wires the /plugin (list|install|enable|disable|remove) backend.
// When unset, /plugin reports that plugins are not available.
func WithPlugins(b pluginBackend) Option {
	return func(c *CLI) { c.plugins = b }
}

// WithHookEntries wires the typed snapshot of plugin-declared hooks the
// /hooks command renders. The caller (cmd/czcli/main.go) re-computes this
// snapshot on every /plugin mutation by reading the dispatcher's Entries().
func WithHookEntries(entries []hooks.Entry) Option {
	return func(c *CLI) { c.hookEntries = entries }
}

// New builds a CLI channel with sensible defaults.
func New(opts ...Option) *CLI {
	c := &CLI{
		sessionID:      "cli",
		statusInterval: 5 * time.Second,
	}
	for _, o := range opts {
		o(c)
	}
	return c
}

// sender abstracts (*tea.Program).Send for testability.
type sender func(msg tea.Msg)

// tickMsg drives idle status refreshes.
type tickMsg struct{}

// Start runs the bubbletea program, bridging inbound lines to handle and
// refreshing status via status. It blocks until ctx is cancelled or the user
// quits.
func (c *CLI) Start(ctx context.Context, handle channel.Handler, status channel.StatusFunc) error {
	m := newModel(80, 24)
	m.sched = c.sched
	m.plugins = c.plugins
	m.hookEntries = c.hookEntries
	pm := &programModel{
		model:  m,
		cli:    c,
		ctx:    ctx,
		handle: handle,
		status: status,
	}
	// Alt-screen isolates the TUI from the underlying shell; mouse-cell
	// motion routes the scroll wheel to the in-app viewport instead of the
	// terminal's scrollback (so scrolling shows past conversation, not the
	// shell history that was behind us when czcli launched).
	p := tea.NewProgram(pm, tea.WithAltScreen(), tea.WithMouseCellMotion())
	pm.send = p.Send

	// Cancel the program when the context ends.
	go func() {
		<-ctx.Done()
		p.Quit()
	}()

	// Prime an initial status snapshot.
	go func() {
		if st, err := status(ctx); err == nil {
			p.Send(statusMsg{status: st})
		}
	}()

	if _, err := p.Run(); err != nil {
		return fmt.Errorf("running cli program: %w", err)
	}
	return nil
}

// runTurn executes one turn: it calls handle with an EventSink that forwards
// stream events as tea messages, then sends turnDoneMsg and a fresh status.
func (c *CLI) runTurn(ctx context.Context, send sender, handle channel.Handler, status channel.StatusFunc, line string) {
	emit := func(ev channel.StreamEvent) {
		switch ev.Type {
		case "text":
			send(streamDeltaMsg{text: ev.Text})
		case "tool_start", "tool_end":
			send(toolEventMsg{kind: ev.Type, name: ev.Text})
		case "subagent_start", "subagent_end":
			send(subagentEventMsg{kind: ev.Type, name: ev.Text})
		case "error":
			send(streamDeltaMsg{text: "\n[error] " + ev.Text})
		}
	}
	reply, err := handle(ctx, channel.Message{SessionID: c.sessionID, Text: line}, emit)
	send(turnDoneMsg{reply: reply.Text, err: err})
	if st, serr := status(ctx); serr == nil {
		send(statusMsg{status: st})
	}
}

// programModel embeds the pure model and intercepts the side-effecting messages
// (submitMsg, statusRequestMsg, tickMsg) to launch goroutines that talk to the
// handler/status funcs via the captured program sender. Pure UI messages fall
// through to model.Update so view/update logic stays testable in isolation.
type programModel struct {
	model  model
	cli    *CLI
	ctx    context.Context
	send   sender
	handle channel.Handler
	status channel.StatusFunc
}

func (pm *programModel) Init() tea.Cmd {
	return tea.Batch(
		pm.model.Init(),
		tea.Tick(pm.cli.statusInterval, func(time.Time) tea.Msg { return tickMsg{} }),
	)
}

func (pm *programModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch m := msg.(type) {
	case submitMsg:
		line := m.line
		go pm.cli.runTurn(pm.ctx, pm.send, pm.handle, pm.status, line)
		return pm, nil
	case statusRequestMsg:
		go pm.refreshStatus()
		return pm, nil
	case tickMsg:
		go pm.refreshStatus()
		return pm, tea.Tick(pm.cli.statusInterval, func(time.Time) tea.Msg { return tickMsg{} })
	}
	next, cmd := pm.model.Update(msg)
	pm.model = next.(model)
	return pm, cmd
}

func (pm *programModel) View() string { return pm.model.View() }

func (pm *programModel) refreshStatus() {
	if st, err := pm.status(pm.ctx); err == nil {
		pm.send(statusMsg{status: st})
	}
}
