package bgproc

import (
	"strings"
	"testing"
	"time"
)

// TestStart_CapturesStdoutAndExit runs a quick command, polls until completion,
// then asserts the output and exit code are recorded.
func TestStart_CapturesStdoutAndExit(t *testing.T) {
	r := New(nil)
	id, err := r.Start("echo hello && echo bye 1>&2", "")
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		p, ok := r.Status(id)
		if !ok {
			t.Fatalf("status: process missing")
		}
		if p.Status != StatusRunning {
			if p.Status != StatusCompleted {
				t.Fatalf("status: want completed, got %s (err=%v)", p.Status, p.Err)
			}
			if p.ExitCode != 0 {
				t.Fatalf("exit code: want 0, got %d", p.ExitCode)
			}
			if !strings.Contains(string(p.Stdout), "hello") {
				t.Fatalf("stdout: want %q, got %q", "hello", string(p.Stdout))
			}
			if !strings.Contains(string(p.Stderr), "bye") {
				t.Fatalf("stderr: want %q, got %q", "bye", string(p.Stderr))
			}
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("timed out waiting for command to finish")
}

// TestStart_FailedExitCode asserts non-zero exit shows as StatusFailed.
func TestStart_FailedExitCode(t *testing.T) {
	r := New(nil)
	id, err := r.Start("exit 3", "")
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		p, _ := r.Status(id)
		if p.Status == StatusRunning {
			time.Sleep(20 * time.Millisecond)
			continue
		}
		if p.Status != StatusFailed {
			t.Fatalf("want failed, got %s", p.Status)
		}
		if p.ExitCode != 3 {
			t.Fatalf("want exit 3, got %d", p.ExitCode)
		}
		return
	}
	t.Fatal("timed out")
}

// TestDrainCompletionNotices fires once per completed process and only once.
func TestDrainCompletionNotices(t *testing.T) {
	r := New(nil)
	id, _ := r.Start("true", "")
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		p, _ := r.Status(id)
		if p.Status != StatusRunning {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	first := r.DrainCompletionNotices()
	if len(first) != 1 {
		t.Fatalf("first drain: want 1 notice, got %d (%v)", len(first), first)
	}
	if !strings.Contains(first[0], id) {
		t.Fatalf("notice should mention id %s, got %q", id, first[0])
	}
	second := r.DrainCompletionNotices()
	if len(second) != 0 {
		t.Fatalf("second drain: want 0 notices, got %d (%v)", len(second), second)
	}
}
