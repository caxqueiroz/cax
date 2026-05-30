package memory

import (
	"context"
	"strings"
	"testing"
)

// fakeSummarizer concatenates message contents; records how many msgs it saw.
type fakeSummarizer struct {
	calls int
	saw   int
}

func (f *fakeSummarizer) Summarize(_ context.Context, _ string, msgs []Message) (string, error) {
	f.calls++
	f.saw = len(msgs)
	parts := make([]string, len(msgs))
	for i, m := range msgs {
		parts[i] = m.Content
	}
	return "SUMMARY:" + strings.Join(parts, "|"), nil
}

func TestMaybeSummarizeUnderBudgetNoop(t *testing.T) {
	st := openTestStore(t, 8)
	ctx := context.Background()
	if _, err := st.AppendMessage(ctx, Message{SessionID: "s1", Role: RoleUser, Content: "hi", Tokens: 2}); err != nil {
		t.Fatal(err)
	}
	fs := &fakeSummarizer{}
	if _, err := st.MaybeSummarize(ctx, "s1", fs, 8000); err != nil {
		t.Fatalf("MaybeSummarize: %v", err)
	}
	if fs.calls != 0 {
		t.Fatalf("summarizer called %d times, want 0 (under budget)", fs.calls)
	}
}

func TestMaybeSummarizeOverBudgetWritesSummary(t *testing.T) {
	st := openTestStore(t, 8)
	ctx := context.Background()
	// 4 messages, 4 tokens each = 16 tokens; budget 8 is exceeded.
	for _, txt := range []string{"m1", "m2", "m3", "m4"} {
		if _, err := st.AppendMessage(ctx, Message{SessionID: "s1", Role: RoleUser, Content: txt, Tokens: 4}); err != nil {
			t.Fatal(err)
		}
	}
	fs := &fakeSummarizer{}
	if _, err := st.MaybeSummarize(ctx, "s1", fs, 8); err != nil {
		t.Fatalf("MaybeSummarize: %v", err)
	}
	if fs.calls != 1 {
		t.Fatalf("summarizer calls = %d, want 1", fs.calls)
	}
	// Oldest chunk = messages beyond the newest budget-fitting window.
	// budget 8 / 4 = 2 newest kept; 2 oldest (m1,m2) summarized.
	if fs.saw != 2 {
		t.Fatalf("summarized %d msgs, want 2 oldest", fs.saw)
	}
	// A summary row must exist and the working window must now load it.
	summary, msgs, err := st.LoadWindow(ctx, "s1", 8)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(summary, "SUMMARY:m1|m2") {
		t.Fatalf("summary = %q, want prefix SUMMARY:m1|m2", summary)
	}
	if len(msgs) == 0 {
		t.Fatal("expected recent messages still present in window")
	}
}

func TestMaybeSummarizeIdempotentByCoverage(t *testing.T) {
	st := openTestStore(t, 8)
	ctx := context.Background()
	for _, txt := range []string{"m1", "m2", "m3", "m4"} {
		if _, err := st.AppendMessage(ctx, Message{SessionID: "s1", Role: RoleUser, Content: txt, Tokens: 4}); err != nil {
			t.Fatal(err)
		}
	}
	fs := &fakeSummarizer{}
	if _, err := st.MaybeSummarize(ctx, "s1", fs, 8); err != nil {
		t.Fatal(err)
	}
	// Second call with no new messages beyond coverage -> no new summary.
	if _, err := st.MaybeSummarize(ctx, "s1", fs, 8); err != nil {
		t.Fatal(err)
	}
	if fs.calls != 1 {
		t.Fatalf("summarizer calls = %d, want 1 (idempotent)", fs.calls)
	}
}
