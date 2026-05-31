package tools

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/caxqueiroz/cax/internal/config"
	"github.com/caxqueiroz/cax/internal/memory"
	"github.com/deepnoodle-ai/dive"
)

// fakeEmbedder deterministically maps text to a fixed-dim vector via SHA-256.
type fakeEmbedder struct{ dim int }

func (e *fakeEmbedder) Dim() int      { return e.dim }
func (e *fakeEmbedder) Model() string { return "fake-embed" }
func (e *fakeEmbedder) Embed(_ context.Context, texts []string) ([][]float32, error) {
	out := make([][]float32, len(texts))
	for i, t := range texts {
		sum := sha256.Sum256([]byte(t))
		v := make([]float32, e.dim)
		for j := range v {
			v[j] = float32(binary.BigEndian.Uint32(sum[(j*4)%len(sum):][:4]) % 1000)
		}
		out[i] = v
	}
	return out, nil
}

func openTestStore(t *testing.T) *memory.Store {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "mem.db")
	store, err := memory.Open(config.MemoryConfig{DBPath: dbPath, TokenBudget: 8000, RecallK: 5}, &fakeEmbedder{dim: 8})
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func callRecall(t *testing.T, tool dive.Tool, input map[string]any) string {
	t.Helper()
	raw, _ := json.Marshal(input)
	res, err := tool.Call(t.Context(), json.RawMessage(raw))
	if err != nil {
		t.Fatalf("recall call: %v", err)
	}
	if len(res.Content) == 0 {
		return ""
	}
	return res.Content[0].Text
}

func TestRecallTool_NameAndSchema(t *testing.T) {
	tool := RecallTool(openTestStore(t))
	if tool.Name() != "search_memory" {
		t.Fatalf("name = %q want search_memory", tool.Name())
	}
	if tool.Schema() == nil {
		t.Fatal("nil schema")
	}
}

func TestRecallTool_ReturnsStoredMemory(t *testing.T) {
	store := openTestStore(t)
	ctx := t.Context()
	if err := store.AddMemory(ctx, "s1", "the cat sat on the mat", 0); err != nil {
		t.Fatalf("add memory: %v", err)
	}
	tool := RecallTool(store)
	out := callRecall(t, tool, map[string]any{"query": "cat", "k": 3, "session_id": "s1"})
	if out == "" {
		t.Fatal("expected non-empty recall output")
	}
	if !strings.Contains(out, "cat") {
		t.Fatalf("recall output missing match: %q", out)
	}
}

func TestRecallTool_EmptyQuery(t *testing.T) {
	tool := RecallTool(openTestStore(t))
	out := callRecall(t, tool, map[string]any{"query": "", "k": 3})
	if out == "" {
		t.Fatal("expected a message for empty query")
	}
}
