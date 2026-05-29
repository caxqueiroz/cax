package memory

import (
	"path/filepath"
	"testing"

	"github.com/caxqueiroz/czcli/internal/config"
)

// openTestStore opens an in-memory store with a fake embedder of the given dim.
func openTestStore(t *testing.T, dim int) *Store {
	t.Helper()
	st, err := Open(config.MemoryConfig{DBPath: ":memory:", TokenBudget: 8000, RecallK: 5}, newFakeEmbedder(dim))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func TestOpenCreatesSchemaAndMeta(t *testing.T) {
	st := openTestStore(t, 8)
	// meta should record model + dim
	var model, dimStr string
	if err := st.db.QueryRow(`SELECT value FROM meta WHERE key='embed_model'`).Scan(&model); err != nil {
		t.Fatalf("read embed_model: %v", err)
	}
	if err := st.db.QueryRow(`SELECT value FROM meta WHERE key='embed_dim'`).Scan(&dimStr); err != nil {
		t.Fatalf("read embed_dim: %v", err)
	}
	if model != "fake-embed" || dimStr != "8" {
		t.Fatalf("meta = %q/%q, want fake-embed/8", model, dimStr)
	}
	// vec0 table must be queryable
	if _, err := st.db.Exec(`INSERT INTO vec_memories(memory_id, embedding) VALUES (1, vec_f32('[` +
		"0,0,0,0,0,0,0,1" + `]'))`); err != nil {
		t.Fatalf("vec0 insert: %v", err)
	}
}

func TestOpenDimMismatchFails(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "mem.db")
	cfg := config.MemoryConfig{DBPath: path, TokenBudget: 8000, RecallK: 5}

	st, err := Open(cfg, newFakeEmbedder(8))
	if err != nil {
		t.Fatalf("first Open: %v", err)
	}
	_ = st.Close()

	// Reopen with a different dim -> must error.
	if _, err := Open(cfg, newFakeEmbedder(16)); err == nil {
		t.Fatal("expected dim-mismatch error on reopen")
	}
}

// TODO(task3): enable after Tasks 4 (AppendMessage) & 6 (AddMemory) land.
/*
func TestStats(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "mem.db")
	st, err := Open(config.MemoryConfig{DBPath: path, TokenBudget: 8000, RecallK: 5}, newFakeEmbedder(8))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = st.Close() }()

	ctx := context.Background()
	if _, err := st.AppendMessage(ctx, Message{SessionID: "s1", Role: RoleUser, Content: "hi", Tokens: 1}); err != nil {
		t.Fatalf("AppendMessage: %v", err)
	}
	if err := st.AddMemory(ctx, "s1", "remember this", 1); err != nil {
		t.Fatalf("AddMemory: %v", err)
	}
	stats, err := st.Stats(ctx)
	if err != nil {
		t.Fatalf("Stats: %v", err)
	}
	if stats.MessageCount != 1 {
		t.Fatalf("MessageCount = %d, want 1", stats.MessageCount)
	}
	if stats.MemoryCount != 1 {
		t.Fatalf("MemoryCount = %d, want 1", stats.MemoryCount)
	}
	if stats.DBSizeBytes <= 0 {
		t.Fatalf("DBSizeBytes = %d, want > 0 for a file-backed DB", stats.DBSizeBytes)
	}
}
*/
