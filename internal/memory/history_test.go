package memory

import (
	"testing"
)

func TestAppendAndLoadWindowChronological(t *testing.T) {
	st := openTestStore(t, 8)
	ctx := t.Context()
	for i, txt := range []string{"one", "two", "three"} {
		role := RoleUser
		if i%2 == 1 {
			role = RoleAssistant
		}
		if _, err := st.AppendMessage(ctx, Message{SessionID: "s1", Role: role, Content: txt, Tokens: EstimateTokens(txt)}); err != nil {
			t.Fatalf("AppendMessage %q: %v", txt, err)
		}
	}
	summary, msgs, err := st.LoadWindow(ctx, "s1", 8000)
	if err != nil {
		t.Fatalf("LoadWindow: %v", err)
	}
	if summary != "" {
		t.Fatalf("summary = %q, want empty", summary)
	}
	if len(msgs) != 3 {
		t.Fatalf("len(msgs) = %d, want 3", len(msgs))
	}
	want := []string{"one", "two", "three"}
	for i, m := range msgs {
		if m.Content != want[i] {
			t.Fatalf("msgs[%d] = %q, want %q (chronological)", i, m.Content, want[i])
		}
	}
}

func TestLoadWindowRespectsBudgetNewestFirst(t *testing.T) {
	st := openTestStore(t, 8)
	ctx := t.Context()
	// Each message is 4 tokens (Tokens set explicitly). Budget 8 fits exactly 2 newest.
	for _, txt := range []string{"aaa", "bbb", "ccc"} {
		if _, err := st.AppendMessage(ctx, Message{SessionID: "s1", Role: RoleUser, Content: txt, Tokens: 4}); err != nil {
			t.Fatalf("AppendMessage: %v", err)
		}
	}
	_, msgs, err := st.LoadWindow(ctx, "s1", 8)
	if err != nil {
		t.Fatalf("LoadWindow: %v", err)
	}
	if len(msgs) != 2 {
		t.Fatalf("len(msgs) = %d, want 2 (budget 8 / 4 each)", len(msgs))
	}
	// newest-first selection, chronological return -> ["bbb","ccc"]
	if msgs[0].Content != "bbb" || msgs[1].Content != "ccc" {
		t.Fatalf("window = [%q,%q], want [bbb,ccc]", msgs[0].Content, msgs[1].Content)
	}
}

func TestLoadWindowSessionScoped(t *testing.T) {
	st := openTestStore(t, 8)
	ctx := t.Context()
	if _, err := st.AppendMessage(ctx, Message{SessionID: "s1", Role: RoleUser, Content: "mine", Tokens: 1}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.AppendMessage(ctx, Message{SessionID: "other", Role: RoleUser, Content: "theirs", Tokens: 1}); err != nil {
		t.Fatal(err)
	}
	_, msgs, err := st.LoadWindow(ctx, "s1", 8000)
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 1 || msgs[0].Content != "mine" {
		t.Fatalf("got %d msgs, want only s1's 'mine'", len(msgs))
	}
}
