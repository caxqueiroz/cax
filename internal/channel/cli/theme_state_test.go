package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestWriteThemeStateRoundtrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")
	if err := writeThemeState(path, "dracula"); err != nil {
		t.Fatalf("writeThemeState: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	var got map[string]string
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got["theme"] != "dracula" {
		t.Fatalf("theme = %q, want dracula", got["theme"])
	}
}

func TestWriteThemeStatePreservesOtherFields(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")
	if err := os.WriteFile(path, []byte(`{"theme":"old","other":"keep"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := writeThemeState(path, "nord"); err != nil {
		t.Fatalf("writeThemeState: %v", err)
	}
	data, _ := os.ReadFile(path)
	var got map[string]string
	_ = json.Unmarshal(data, &got)
	if got["theme"] != "nord" || got["other"] != "keep" {
		t.Fatalf("got = %+v, want theme=nord and other=keep", got)
	}
}

func TestWriteThemeStateTolerantOfCorruption(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")
	if err := os.WriteFile(path, []byte("not json {{{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := writeThemeState(path, "mono"); err != nil {
		t.Fatalf("writeThemeState should tolerate corruption: %v", err)
	}
	data, _ := os.ReadFile(path)
	var got map[string]string
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("post-write file not valid JSON: %v", err)
	}
	if got["theme"] != "mono" {
		t.Fatalf("theme = %q, want mono", got["theme"])
	}
}
