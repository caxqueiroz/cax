package cli

import (
	"context"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/caxqueiroz/czcli/internal/channel"
)

func TestNewCLIDefaults(t *testing.T) {
	c := New()
	if c.sessionID == "" {
		t.Errorf("expected a default session id")
	}
	if c.statusInterval <= 0 {
		t.Errorf("expected a positive status interval")
	}
}

func TestNewCLIOptions(t *testing.T) {
	c := New(WithSessionID("sess-1"), WithStatusInterval(7*time.Second))
	if c.sessionID != "sess-1" {
		t.Errorf("session id = %q, want sess-1", c.sessionID)
	}
	if c.statusInterval != 7*time.Second {
		t.Errorf("interval = %v, want 7s", c.statusInterval)
	}
}

// runTurn exercises the worker bridge without a live terminal: it feeds a
// submitMsg through a captured sink recorder and asserts the emitted messages.
func TestRunTurnEmitsStreamAndDone(t *testing.T) {
	var sent []tea.Msg
	send := func(m tea.Msg) { sent = append(sent, m) }

	handle := func(_ context.Context, _ channel.Message, emit channel.EventSink) (channel.Reply, error) {
		emit(channel.StreamEvent{Type: "text", Text: "hel"})
		emit(channel.StreamEvent{Type: "text", Text: "lo"})
		emit(channel.StreamEvent{Type: "subagent_start", Text: "explore"})
		emit(channel.StreamEvent{Type: "subagent_end", Text: "explore"})
		return channel.Reply{Text: "hello!"}, nil
	}
	status := func(_ context.Context) (channel.Status, error) {
		return channel.Status{Model: "m"}, nil
	}

	c := New(WithSessionID("s"))
	c.runTurn(context.Background(), send, handle, status, "hi")

	var gotDelta, gotDone, gotStatus, gotSubStart bool
	for _, m := range sent {
		switch mm := m.(type) {
		case streamDeltaMsg:
			gotDelta = true
		case turnDoneMsg:
			gotDone = true
			if mm.reply != "hello!" {
				t.Errorf("turnDone reply = %q, want hello!", mm.reply)
			}
		case statusMsg:
			gotStatus = true
		case subagentEventMsg:
			if mm.kind == "subagent_start" {
				gotSubStart = true
			}
		}
	}
	if !gotDelta || !gotDone || !gotStatus || !gotSubStart {
		t.Errorf("missing messages: delta=%v done=%v status=%v subStart=%v", gotDelta, gotDone, gotStatus, gotSubStart)
	}
}
