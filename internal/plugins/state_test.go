package plugins

import (
	"os"
	"path/filepath"
	"testing"
)

func TestStateRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "plugins.json")
	in := state{
		"foo": {Enabled: true, Source: "https://github.com/x/foo"},
		"bar": {Enabled: false, Source: "local"},
	}
	if err := writeState(path, in); err != nil {
		t.Fatalf("writeState: %v", err)
	}
	out, err := readState(path)
	if err != nil {
		t.Fatalf("readState: %v", err)
	}
	if !out["foo"].Enabled || out["foo"].Source != "https://github.com/x/foo" {
		t.Errorf("foo round-trip = %+v", out["foo"])
	}
	if out["bar"].Enabled {
		t.Errorf("bar should be disabled, got %+v", out["bar"])
	}
}

func TestStateAtomicWriteOverwrites(t *testing.T) {
	path := filepath.Join(t.TempDir(), "plugins.json")
	if err := writeState(path, state{"a": {Enabled: true, Source: "s"}}); err != nil {
		t.Fatal(err)
	}
	if err := writeState(path, state{"b": {Enabled: false, Source: "t"}}); err != nil {
		t.Fatal(err)
	}
	out, _ := readState(path)
	if _, has := out["a"]; has {
		t.Error("second writeState should fully overwrite")
	}
	if out["b"].Enabled {
		t.Error("b should be disabled")
	}
}

func TestStateMissingFile(t *testing.T) {
	out, err := readState(filepath.Join(t.TempDir(), "nope.json"))
	if err != nil {
		t.Fatalf("missing file should be nil error, got %v", err)
	}
	if len(out) != 0 {
		t.Errorf("missing file should yield empty state, got %+v", out)
	}
}

func TestStateCorruptionTolerated(t *testing.T) {
	path := filepath.Join(t.TempDir(), "plugins.json")
	if err := os.WriteFile(path, []byte(`{not json`), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := readState(path)
	if err != nil {
		t.Fatalf("corrupt file should be nil error (log + empty fallback), got %v", err)
	}
	if len(out) != 0 {
		t.Errorf("corrupt file should yield empty state, got %+v", out)
	}
}
