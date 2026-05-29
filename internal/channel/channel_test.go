package channel

import (
	"context"
	"testing"
)

func TestContractTypesCompile(t *testing.T) {
	msg := Message{SessionID: "s1", Text: "hi"}
	if msg.SessionID != "s1" || msg.Text != "hi" {
		t.Fatalf("unexpected message: %+v", msg)
	}

	reply := Reply{Text: "ok"}
	if reply.Text != "ok" {
		t.Fatalf("unexpected reply: %+v", reply)
	}

	var got []StreamEvent
	var sink EventSink = func(ev StreamEvent) { got = append(got, ev) }
	sink(StreamEvent{Type: "text", Text: "delta"})
	if len(got) != 1 || got[0].Type != "text" || got[0].Text != "delta" {
		t.Fatalf("sink did not capture event: %+v", got)
	}

	var h Handler = func(ctx context.Context, m Message, emit EventSink) (Reply, error) {
		emit(StreamEvent{Type: "text", Text: m.Text})
		return Reply{Text: m.Text}, nil
	}
	r, err := h(context.Background(), Message{Text: "echo"}, sink)
	if err != nil || r.Text != "echo" {
		t.Fatalf("handler failed: r=%+v err=%v", r, err)
	}

	status := Status{
		Provider:      "bedrock",
		Model:         "claude",
		OnFallback:    true,
		FallbackIndex: 1,
		ContextTokens: 100,
		ContextBudget: 8000,
		Usage: UsageRollup{
			Day:   UsageTotals{InputTokens: 1, OutputTokens: 2},
			Week:  UsageTotals{InputTokens: 3, OutputTokens: 4},
			Month: UsageTotals{InputTokens: 5, OutputTokens: 6},
		},
		MemSizeBytes:     1024,
		MessageCount:     7,
		MemoryCount:      8,
		ToolNames:        []string{"bash"},
		SubagentNames:    []string{"explore"},
		RunningSubagents: []string{"plan"},
	}
	var sf StatusFunc = func(ctx context.Context) (Status, error) { return status, nil }
	gotStatus, err := sf(context.Background())
	if err != nil || gotStatus.Provider != "bedrock" || gotStatus.Usage.Day.OutputTokens != 2 {
		t.Fatalf("status func failed: %+v err=%v", gotStatus, err)
	}

	var _ Channel = (*noopChannel)(nil)
}

type noopChannel struct{}

func (noopChannel) Start(ctx context.Context, handle Handler, status StatusFunc) error {
	return nil
}
