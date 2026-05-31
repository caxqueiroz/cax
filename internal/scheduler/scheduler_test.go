package scheduler

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/caxqueiroz/cax/internal/config"
	"github.com/caxqueiroz/cax/internal/memory"
)

// fakeEmbedder is a deterministic hash->vector embedder for memory tests.
type fakeEmbedder struct{ dim int }

func (f fakeEmbedder) Embed(_ context.Context, texts []string) ([][]float32, error) {
	out := make([][]float32, len(texts))
	for i, t := range texts {
		v := make([]float32, f.dim)
		for j := 0; j < f.dim; j++ {
			v[j] = float32((len(t)+j*7)%13) / 13.0
		}
		out[i] = v
	}
	return out, nil
}
func (f fakeEmbedder) Dim() int      { return f.dim }
func (f fakeEmbedder) Model() string { return "fake" }

func openStore(t *testing.T) *memory.Store {
	t.Helper()
	st, err := memory.Open(config.MemoryConfig{
		DBPath:      filepath.Join(t.TempDir(), "mem.db"),
		TokenBudget: 8000,
		RecallK:     5,
	}, fakeEmbedder{dim: 8})
	if err != nil {
		t.Fatalf("memory.Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

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

func TestLoadRegistersEnabledAndRunsJob(t *testing.T) {
	ctx := t.Context()
	st := openStore(t)

	if err := st.UpsertSchedule(ctx, config.ScheduleConfig{
		Name: "nightly", Cron: "0 0 * * *", Prompt: "daily report", Channel: "cli", Enabled: true,
	}); err != nil {
		t.Fatalf("upsert enabled: %v", err)
	}
	if err := st.UpsertSchedule(ctx, config.ScheduleConfig{
		Name: "off", Cron: "0 0 * * *", Prompt: "nope", Channel: "cli", Enabled: false,
	}); err != nil {
		t.Fatalf("upsert disabled: %v", err)
	}

	type call struct{ prompt, channel string }
	var got []call
	run := func(_ context.Context, prompt, ch string) error {
		got = append(got, call{prompt, ch})
		return nil
	}

	s := New(st, run)
	if err := s.Load(ctx); err != nil {
		t.Fatalf("Load: %v", err)
	}

	if _, ok := s.jobs["off"]; ok {
		t.Fatal("disabled schedule must not be registered")
	}
	job, ok := s.jobs["nightly"]
	if !ok {
		t.Fatal("enabled schedule was not registered")
	}

	job() // invoke the registered job func directly — deterministic, no clock wait

	if len(got) != 1 || got[0].prompt != "daily report" || got[0].channel != "cli" {
		t.Fatalf("RunFunc not invoked correctly: %+v", got)
	}

	scheds, err := st.ListSchedules(ctx)
	if err != nil {
		t.Fatalf("ListSchedules: %v", err)
	}
	var found bool
	for _, sc := range scheds {
		if sc.Name == "nightly" {
			found = true
		}
	}
	if !found {
		t.Fatal("nightly schedule missing after load")
	}
}

func TestJobRecoversPanic(t *testing.T) {
	ctx := t.Context()
	st := openStore(t)

	if err := st.UpsertSchedule(ctx, config.ScheduleConfig{
		Name: "boom", Cron: "* * * * *", Prompt: "p", Channel: "cli", Enabled: true,
	}); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	s := New(st, func(context.Context, string, string) error {
		panic("kaboom")
	})
	if err := s.Load(ctx); err != nil {
		t.Fatalf("Load: %v", err)
	}

	job, ok := s.jobs["boom"]
	if !ok {
		t.Fatal("schedule not registered")
	}

	// Must NOT panic; recovered inside the job wrapper.
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("panic propagated out of job: %v", r)
		}
	}()
	job()
}

func TestLoadSkipsInvalidCron(t *testing.T) {
	ctx := t.Context()
	st := openStore(t)

	if err := st.UpsertSchedule(ctx, config.ScheduleConfig{
		Name: "good", Cron: "*/15 * * * *", Prompt: "p", Channel: "cli", Enabled: true,
	}); err != nil {
		t.Fatalf("upsert good: %v", err)
	}
	if err := st.UpsertSchedule(ctx, config.ScheduleConfig{
		Name: "bad", Cron: "not a cron expr", Prompt: "p", Channel: "cli", Enabled: true,
	}); err != nil {
		t.Fatalf("upsert bad: %v", err)
	}

	s := New(st, func(context.Context, string, string) error { return nil })
	if err := s.Load(ctx); err != nil {
		t.Fatalf("Load must not fail on invalid cron: %v", err)
	}

	if _, ok := s.jobs["bad"]; ok {
		t.Fatal("invalid cron schedule must be skipped, not registered")
	}
	if _, ok := s.jobs["good"]; !ok {
		t.Fatal("valid cron schedule must be registered")
	}
}

func TestReloadReflectsStoreChanges(t *testing.T) {
	ctx := t.Context()
	st := openStore(t)

	if err := st.UpsertSchedule(ctx, config.ScheduleConfig{
		Name: "a", Cron: "0 0 * * *", Prompt: "pa", Channel: "cli", Enabled: true,
	}); err != nil {
		t.Fatalf("upsert a: %v", err)
	}

	s := New(st, func(context.Context, string, string) error { return nil })
	if err := s.Load(ctx); err != nil {
		t.Fatalf("Load: %v", err)
	}
	if _, ok := s.jobs["a"]; !ok {
		t.Fatal("schedule a not registered after Load")
	}

	// Disable "a", add enabled "b".
	if err := st.UpsertSchedule(ctx, config.ScheduleConfig{
		Name: "a", Cron: "0 0 * * *", Prompt: "pa", Channel: "cli", Enabled: false,
	}); err != nil {
		t.Fatalf("disable a: %v", err)
	}
	if err := st.UpsertSchedule(ctx, config.ScheduleConfig{
		Name: "b", Cron: "0 9 * * *", Prompt: "pb", Channel: "cli", Enabled: true,
	}); err != nil {
		t.Fatalf("upsert b: %v", err)
	}

	if err := s.Reload(ctx); err != nil {
		t.Fatalf("Reload: %v", err)
	}

	if _, ok := s.jobs["a"]; ok {
		t.Fatal("disabled schedule a must be removed after Reload")
	}
	if _, ok := s.jobs["b"]; !ok {
		t.Fatal("new schedule b must be registered after Reload")
	}
}
