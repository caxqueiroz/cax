package theme

import (
	"os"
	"path/filepath"
	"testing"
)

func TestStateRoundTrip(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "state.json")

	if name := readStateTheme(p); name != "" {
		t.Fatalf("missing file should return empty, got %q", name)
	}

	if err := writeStateTheme(p, "dracula"); err != nil {
		t.Fatalf("write: %v", err)
	}
	if name := readStateTheme(p); name != "dracula" {
		t.Fatalf("read = %q want dracula", name)
	}

	// Corrupt file -> empty + no error from reader (silent recovery).
	if err := os.WriteFile(p, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if name := readStateTheme(p); name != "" {
		t.Fatalf("corrupt file should return empty, got %q", name)
	}
}

func TestStateAtomic(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "state.json")
	if err := writeStateTheme(p, "nord"); err != nil {
		t.Fatalf("write: %v", err)
	}
	// No leftover *.tmp from the rename.
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if filepath.Ext(e.Name()) == ".tmp" {
			t.Fatalf("leftover tmp file %s", e.Name())
		}
	}
}
