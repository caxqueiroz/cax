package scheduler

import (
	"context"
	"testing"
)

func TestNewStartStop(t *testing.T) {
	called := 0
	run := func(_ context.Context, _, _ string) error {
		called++
		return nil
	}

	s := New(nil, run)
	if s == nil {
		t.Fatal("New returned nil")
	}

	// Start then Stop must be safe even with no schedules registered.
	s.Start()
	s.Stop()

	if called != 0 {
		t.Fatalf("RunFunc should not be invoked without schedules, got %d calls", called)
	}
}
