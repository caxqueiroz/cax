package memory

import (
	"context"
	"testing"
)

func TestAddMemoryAndRecallOrdering(t *testing.T) {
	st := openTestStore(t, 16)
	ctx := context.Background()
	texts := []string{
		"the cat sat on the mat",
		"quantum chromodynamics lattice gauge theory",
		"my cat likes to sit on warm laps",
	}
	for i, txt := range texts {
		if err := st.AddMemory(ctx, "s1", txt, int64(i+1)); err != nil {
			t.Fatalf("AddMemory %q: %v", txt, err)
		}
	}
	// Recall with the exact text of one stored memory -> it must be the closest (distance ~0).
	got, err := st.Recall(ctx, "s1", "the cat sat on the mat", 3)
	if err != nil {
		t.Fatalf("Recall: %v", err)
	}
	if len(got) == 0 {
		t.Fatal("Recall returned no results")
	}
	if got[0].Text != "the cat sat on the mat" {
		t.Fatalf("closest = %q, want exact match first", got[0].Text)
	}
	// Distances must be non-decreasing (KNN ordering).
	for i := 1; i < len(got); i++ {
		if got[i].Distance < got[i-1].Distance {
			t.Fatalf("distances not sorted: %v then %v", got[i-1].Distance, got[i].Distance)
		}
	}
	// The exact match should be ~0 distance.
	if got[0].Distance > 1e-3 {
		t.Fatalf("exact-match distance = %v, want ~0", got[0].Distance)
	}
}

func TestRecallRespectsK(t *testing.T) {
	st := openTestStore(t, 16)
	ctx := context.Background()
	for i, txt := range []string{"alpha", "beta", "gamma", "delta", "epsilon"} {
		if err := st.AddMemory(ctx, "s1", txt, int64(i+1)); err != nil {
			t.Fatal(err)
		}
	}
	got, err := st.Recall(ctx, "s1", "alpha", 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("len = %d, want 2 (k)", len(got))
	}
}

func TestRecallSessionScoped(t *testing.T) {
	st := openTestStore(t, 16)
	ctx := context.Background()
	if err := st.AddMemory(ctx, "s1", "mine secret", 1); err != nil {
		t.Fatal(err)
	}
	if err := st.AddMemory(ctx, "other", "their secret", 2); err != nil {
		t.Fatal(err)
	}
	got, err := st.Recall(ctx, "s1", "secret", 5)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range got {
		if r.Text == "their secret" {
			t.Fatal("recall leaked another session's memory")
		}
	}
	if len(got) != 1 {
		t.Fatalf("len = %d, want 1 (only s1)", len(got))
	}
}
