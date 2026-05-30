package hooks

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func newTestLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError}))
}

func skipOnWindows(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("hook dispatcher tests require POSIX /bin/sh")
	}
}

func TestLoadEntriesRoundtrip(t *testing.T) {
	entries := []Entry{
		{Event: EventPreToolUse, Matcher: Matcher{Tool: "Bash"}, Command: []string{"/bin/sh", "-c", "exit 0"}, TimeoutSeconds: 5, Source: "p1"},
		{Event: EventStop, Command: []string{"/bin/sh", "-c", "exit 0"}, Source: "p2"},
	}
	d := Load(entries, newTestLogger())
	if d == nil {
		t.Fatal("Load returned nil")
	}
	got := d.Entries()
	if len(got) != len(entries) {
		t.Fatalf("Entries length: got %d want %d", len(got), len(entries))
	}
	for i := range got {
		if got[i].Source != entries[i].Source || got[i].Event != entries[i].Event {
			t.Fatalf("entry %d mismatch: got %+v want %+v", i, got[i], entries[i])
		}
	}
}

func TestDispatchNoMatchingEntries(t *testing.T) {
	d := Load([]Entry{
		{Event: EventStop, Command: []string{"/bin/sh", "-c", "exit 1"}, Source: "p1"},
	}, newTestLogger())

	got := d.Dispatch(context.Background(), EventPreToolUse, map[string]any{"tool_name": "Bash"})
	if got.Block {
		t.Fatalf("Dispatch with no matching entries should not block, got %+v", got)
	}
	if got.Feedback != "" {
		t.Fatalf("Feedback should be empty when nothing matches, got %q", got.Feedback)
	}
}

func TestDispatchNilDispatcherSafe(t *testing.T) {
	var d *Dispatcher
	got := d.Dispatch(context.Background(), EventPreToolUse, nil)
	if got.Block || got.Feedback != "" {
		t.Fatalf("nil dispatcher should be a no-op, got %+v", got)
	}
	if entries := d.Entries(); entries != nil {
		t.Fatalf("nil dispatcher Entries should return nil, got %v", entries)
	}
}

func TestDispatchExitZeroDoesNotBlock(t *testing.T) {
	skipOnWindows(t)
	d := Load([]Entry{{
		Event:   EventPreToolUse,
		Matcher: Matcher{Tool: "Bash"},
		Command: []string{"/bin/sh", "-c", "echo ignored; exit 0"},
		Source:  "p1",
	}}, newTestLogger())

	got := d.Dispatch(context.Background(), EventPreToolUse, map[string]any{
		"tool_name":  "Bash",
		"tool_input": map[string]any{"command": "ls"},
	})
	if got.Block {
		t.Fatalf("exit 0 must not block, got %+v", got)
	}
	if got.Feedback != "" {
		t.Fatalf("Feedback should be empty on no-block, got %q", got.Feedback)
	}
}

func TestDispatchExitNonzeroBlocksWithStdoutFeedback(t *testing.T) {
	skipOnWindows(t)
	d := Load([]Entry{{
		Event:   EventPreToolUse,
		Matcher: Matcher{Tool: "Bash"},
		Command: []string{"/bin/sh", "-c", "echo 'destructive rm blocked'; exit 1"},
		Source:  "policy",
	}}, newTestLogger())

	got := d.Dispatch(context.Background(), EventPreToolUse, map[string]any{
		"tool_name":  "Bash",
		"tool_input": map[string]any{"command": "rm -rf /"},
	})
	if !got.Block {
		t.Fatalf("exit 1 must block, got %+v", got)
	}
	if got.Feedback != "destructive rm blocked" {
		t.Fatalf("Feedback should equal trimmed stdout, got %q", got.Feedback)
	}
}

func TestDispatchEnvelopeWrittenOnStdin(t *testing.T) {
	skipOnWindows(t)
	dir := t.TempDir()
	out := filepath.Join(dir, "envelope.json")
	d := Load([]Entry{{
		Event:   EventPreToolUse,
		Matcher: Matcher{Tool: "Bash"},
		// Capture stdin to a file, then exit 0 (no block).
		Command: []string{"/bin/sh", "-c", "cat > " + out + "; exit 0"},
		Source:  "capture",
	}}, newTestLogger())

	payload := map[string]any{
		"tool_name":  "Bash",
		"tool_input": map[string]any{"command": "ls -la"},
	}
	got := d.Dispatch(context.Background(), EventPreToolUse, payload)
	if got.Block {
		t.Fatalf("capture hook should not block, got %+v", got)
	}
	raw, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("read envelope file: %v", err)
	}
	var env map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(raw), &env); err != nil {
		t.Fatalf("envelope is not JSON: %v (raw=%q)", err, string(raw))
	}
	if env["hook_event_name"] != string(EventPreToolUse) {
		t.Fatalf("hook_event_name: got %v want %q", env["hook_event_name"], EventPreToolUse)
	}
	if env["tool_name"] != "Bash" {
		t.Fatalf("tool_name: got %v want %q", env["tool_name"], "Bash")
	}
	ti, ok := env["tool_input"].(map[string]any)
	if !ok || ti["command"] != "ls -la" {
		t.Fatalf("tool_input.command: got %v", env["tool_input"])
	}
}

func TestDispatchMultipleBlockingHooksConcatenateFeedback(t *testing.T) {
	skipOnWindows(t)
	d := Load([]Entry{
		{Event: EventStop, Command: []string{"/bin/sh", "-c", "echo first; exit 1"}, Source: "a"},
		{Event: EventStop, Command: []string{"/bin/sh", "-c", "echo second; exit 1"}, Source: "b"},
	}, newTestLogger())

	got := d.Dispatch(context.Background(), EventStop, nil)
	if !got.Block {
		t.Fatalf("both hooks should block, got %+v", got)
	}
	if !strings.Contains(got.Feedback, "first") || !strings.Contains(got.Feedback, "second") {
		t.Fatalf("Feedback should join both stdout, got %q", got.Feedback)
	}
}

func TestDispatchSpawnFailureIsBestEffort(t *testing.T) {
	skipOnWindows(t)
	d := Load([]Entry{{
		Event:   EventStop,
		Command: []string{"/no/such/binary/at/all"},
		Source:  "broken",
	}}, newTestLogger())

	got := d.Dispatch(context.Background(), EventStop, nil)
	if got.Block {
		t.Fatalf("spawn failure must be treated as no-op, got %+v", got)
	}
	if got.Feedback != "" {
		t.Fatalf("spawn failure must not surface stdout, got %q", got.Feedback)
	}
}

func TestDispatchTimeoutKillsChild(t *testing.T) {
	skipOnWindows(t)
	d := Load([]Entry{{
		Event:          EventStop,
		Command:        []string{"/bin/sh", "-c", "sleep 10; echo too-late"},
		TimeoutSeconds: 1,
		Source:         "slow",
	}}, newTestLogger())

	start := time.Now()
	got := d.Dispatch(context.Background(), EventStop, nil)
	elapsed := time.Since(start)

	if elapsed >= 5*time.Second {
		t.Fatalf("timeout did not kill child within budget, took %s", elapsed)
	}
	if got.Block {
		t.Fatalf("timeout must be treated as no-op (best-effort), got %+v", got)
	}
	if strings.Contains(got.Feedback, "too-late") {
		t.Fatalf("child should have been killed before printing, got feedback %q", got.Feedback)
	}
}
