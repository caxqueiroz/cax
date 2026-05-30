package cli

import (
	"context"
	"sync"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/deepnoodle-ai/dive"
)

// PermDialog is a TUI-native dive.Dialog. When the agent's permission gate
// needs a y/n answer, Show() pushes a permRequestMsg into the bubbletea
// program and blocks until the model answers via the response channel.
// Auto-approve is supported via NewPermDialog(requireConfirm=false): in that
// mode every confirm prompt returns Confirmed=true without going through the
// TUI.
type PermDialog struct {
	requireConfirm bool

	mu     sync.RWMutex
	sender func(tea.Msg)
}

var _ dive.Dialog = (*PermDialog)(nil)

// NewPermDialog constructs a TUI permission dialog. If requireConfirm is
// false every Show with Confirm=true is auto-approved (matches the legacy
// stdin dialog's behavior). The bubbletea sender is wired by cli.Start.
func NewPermDialog(requireConfirm bool) *PermDialog {
	return &PermDialog{requireConfirm: requireConfirm}
}

// setSender is called by cli.Start with the program's Send function so that
// later Show() invocations can deliver requests into the running TUI.
func (d *PermDialog) setSender(s func(tea.Msg)) {
	d.mu.Lock()
	d.sender = s
	d.mu.Unlock()
}

// SetRequireConfirm toggles the require-confirm flag at runtime. Used by the
// /permissions slash command; cfg.Tools.RequireConfirm in config is the
// startup default.
func (d *PermDialog) SetRequireConfirm(b bool) {
	d.mu.Lock()
	d.requireConfirm = b
	d.mu.Unlock()
}

// RequireConfirm reports the current value. Useful for /permissions status.
func (d *PermDialog) RequireConfirm() bool {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.requireConfirm
}

// Show implements dive.Dialog. For confirm prompts it routes through the
// TUI; for non-confirm prompts (text input, select) it returns the default
// without prompting — the permission gate only issues confirms today.
func (d *PermDialog) Show(ctx context.Context, in *dive.DialogInput) (*dive.DialogOutput, error) {
	if !in.Confirm {
		return &dive.DialogOutput{Text: in.Default}, nil
	}
	if !d.requireConfirm {
		return &dive.DialogOutput{Confirmed: true}, nil
	}
	d.mu.RLock()
	send := d.sender
	d.mu.RUnlock()
	if send == nil {
		// TUI not running yet — deny by default rather than risk silently
		// approving a tool call before the user can see it.
		return &dive.DialogOutput{Confirmed: false}, nil
	}
	respCh := make(chan bool, 1)
	send(permRequestMsg{title: in.Title, message: in.Message, response: respCh})
	select {
	case yes := <-respCh:
		return &dive.DialogOutput{Confirmed: yes}, nil
	case <-ctx.Done():
		return &dive.DialogOutput{Confirmed: false}, ctx.Err()
	}
}

// permRequestMsg is delivered to the model when the agent needs permission.
// The model writes the user's answer into response and clears its pending
// state; the dialog's Show() unblocks and returns.
type permRequestMsg struct {
	title    string
	message  string
	response chan<- bool
}

// pendingPermission is the model-side representation of an in-flight request.
type pendingPermission struct {
	title    string
	message  string
	response chan<- bool
}

// answer delivers a yes/no result back to the blocked Dialog.Show call.
// Safe to call once per request; subsequent sends are dropped because the
// channel is buffered with capacity 1.
func (p *pendingPermission) answer(yes bool) {
	select {
	case p.response <- yes:
	default:
	}
}
