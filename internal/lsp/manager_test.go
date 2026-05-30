package lsp

import (
	"context"
	"testing"

	_ "github.com/caxqueiroz/czcli/internal/config"
)

func TestNewEmpty(t *testing.T) {
	ctx := context.Background()
	m, infos, err := New(ctx, nil, t.TempDir())
	if err != nil {
		t.Fatalf("New empty: %v", err)
	}
	if m == nil {
		t.Fatal("New returned nil manager")
	}
	if len(infos) != 0 {
		t.Fatalf("expected 0 ServerInfo, got %d", len(infos))
	}
	tools := m.Tools()
	want := []string{
		"lsp_definition", "lsp_references", "lsp_hover",
		"lsp_document_symbols", "lsp_workspace_symbols", "lsp_diagnostics",
	}
	if len(tools) != len(want) {
		t.Fatalf("Tools count = %d, want %d", len(tools), len(want))
	}
	got := map[string]bool{}
	for _, tl := range tools {
		got[tl.Name()] = true
	}
	for _, n := range want {
		if !got[n] {
			t.Errorf("missing tool %q", n)
		}
	}
	if err := m.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}
