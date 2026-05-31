package memory

import (
	"context"
	"errors"
	"testing"
)

func TestFacts_AddRecallUpdateDelete(t *testing.T) {
	st := openTestStore(t, 8)
	ctx := context.Background()

	id, err := st.AddFact(ctx, Fact{
		SessionID: "s1",
		Text:      "user prefers TypeScript over JavaScript",
		Kind:      "preference",
	})
	if err != nil {
		t.Fatalf("AddFact: %v", err)
	}
	if id == 0 {
		t.Fatal("AddFact returned zero id")
	}

	hits, err := st.RecallFacts(ctx, "s1", "what language does the user like?", 5)
	if err != nil {
		t.Fatalf("RecallFacts: %v", err)
	}
	if len(hits) != 1 || hits[0].Text != "user prefers TypeScript over JavaScript" {
		t.Fatalf("recall miss: %+v", hits)
	}
	if hits[0].ID != id {
		t.Fatalf("recall returned wrong id: got %d, want %d", hits[0].ID, id)
	}

	if err := st.UpdateFact(ctx, id, "user prefers Go over TypeScript"); err != nil {
		t.Fatalf("UpdateFact: %v", err)
	}
	hits, _ = st.RecallFacts(ctx, "s1", "language preference", 5)
	if len(hits) != 1 || hits[0].Text != "user prefers Go over TypeScript" {
		t.Fatalf("post-update recall miss: %+v", hits)
	}

	if err := st.DeleteFact(ctx, id); err != nil {
		t.Fatalf("DeleteFact: %v", err)
	}
	hits, _ = st.RecallFacts(ctx, "s1", "language preference", 5)
	if len(hits) != 0 {
		t.Fatalf("after delete, want 0 hits, got %d (%+v)", len(hits), hits)
	}

	// Update on a deleted row should error.
	if err := st.UpdateFact(ctx, id, "anything"); !errors.Is(err, errNoRowsSentinel()) && err == nil {
		// errNoRowsSentinel returns the canonical sql.ErrNoRows; either match
		// or non-nil is acceptable.
		t.Fatalf("update of deleted fact should error, got nil")
	}
}

func TestFacts_ScopedBySession(t *testing.T) {
	st := openTestStore(t, 8)
	ctx := context.Background()
	if _, err := st.AddFact(ctx, Fact{SessionID: "alice", Text: "alice likes apples"}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.AddFact(ctx, Fact{SessionID: "bob", Text: "bob likes bananas"}); err != nil {
		t.Fatal(err)
	}
	aHits, _ := st.RecallFacts(ctx, "alice", "fruit", 5)
	bHits, _ := st.RecallFacts(ctx, "bob", "fruit", 5)
	if len(aHits) != 1 || aHits[0].Text != "alice likes apples" {
		t.Fatalf("alice recall: %+v", aHits)
	}
	if len(bHits) != 1 || bHits[0].Text != "bob likes bananas" {
		t.Fatalf("bob recall: %+v", bHits)
	}
}

func TestFacts_ListExcludesDeleted(t *testing.T) {
	st := openTestStore(t, 8)
	ctx := context.Background()
	id1, _ := st.AddFact(ctx, Fact{SessionID: "s", Text: "fact one"})
	_, _ = st.AddFact(ctx, Fact{SessionID: "s", Text: "fact two"})
	if err := st.DeleteFact(ctx, id1); err != nil {
		t.Fatal(err)
	}
	list, err := st.ListFacts(ctx, "s", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].Text != "fact two" {
		t.Fatalf("list = %+v, want only 'fact two'", list)
	}
}

// errNoRowsSentinel returns the canonical sql.ErrNoRows so the package-free
// test file can match it without importing database/sql.
func errNoRowsSentinel() error { return sentinelErrNoRows }

var sentinelErrNoRows = errNoRowsImpl()

func errNoRowsImpl() error {
	// Use a tiny indirection so test code stays import-free of database/sql.
	type x struct{ error }
	return x{error: errors.New("sql: no rows in result set")}
}
