// Package scheduler runs stored prompts through the agent on cron schedules,
// loading schedules from the memory store and routing each run's output to a
// named channel. Invalid cron specs are skipped, job panics are recovered, and
// run errors are logged — none of which stop the scheduler.
package scheduler

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/robfig/cron/v3"

	"github.com/caxqueiroz/czcli/internal/config"
	"github.com/caxqueiroz/czcli/internal/memory"
)

// RunFunc executes a scheduled prompt and routes its output to the named
// channel. It returns an error if the run fails; the scheduler logs and
// continues. Defined verbatim in 00-shared-contracts.md.
type RunFunc func(ctx context.Context, prompt, channel string) error

// Scheduler registers one cron entry per enabled schedule.
type Scheduler struct {
	cron  *cron.Cron
	store *memory.Store
	run   RunFunc

	mu      sync.Mutex
	jobs    map[string]func()       // schedule name -> registered job func (for tests + reload)
	entries map[string]cron.EntryID // schedule name -> cron entry id (for reload removal)
}

// slogCronLogger adapts log/slog to cron.Logger (Info/Error), so cron's internal
// logging — including recovered panics — flows through structured slog output.
type slogCronLogger struct{ l *slog.Logger }

func (c slogCronLogger) Info(msg string, kv ...interface{}) { c.l.Info("cron: "+msg, kv...) }
func (c slogCronLogger) Error(err error, msg string, kv ...interface{}) {
	c.l.Error("cron: "+msg, append([]interface{}{"error", err}, kv...)...)
}

// New constructs a Scheduler. The cron instance recovers job panics and bridges
// cron's logger to log/slog.
func New(store *memory.Store, run RunFunc) *Scheduler {
	logger := slogCronLogger{l: slog.Default()}
	c := cron.New(
		cron.WithLogger(logger),
		cron.WithChain(cron.Recover(logger)),
	)
	return &Scheduler{
		cron:    c,
		store:   store,
		run:     run,
		jobs:    make(map[string]func()),
		entries: make(map[string]cron.EntryID),
	}
}

// Start runs the cron scheduler in its own goroutine (no-op if already started).
func (s *Scheduler) Start() { s.cron.Start() }

// Stop stops the cron scheduler and waits for in-flight jobs to finish.
func (s *Scheduler) Stop() {
	ctx := s.cron.Stop()
	<-ctx.Done()
}

// Load reads enabled schedules from the store and registers a cron entry per
// schedule. Invalid cron expressions are logged and skipped (never fatal).
func (s *Scheduler) Load(ctx context.Context) error {
	scheds, err := s.store.ListSchedules(ctx)
	if err != nil {
		return fmt.Errorf("list schedules: %w", err)
	}
	for _, sc := range scheds {
		if !sc.Enabled {
			continue
		}
		s.register(sc)
	}
	return nil
}

// Reload clears all registered cron entries and re-reads the schedules table.
// Safe to call while the scheduler is running; used after /schedule CRUD.
func (s *Scheduler) Reload(ctx context.Context) error {
	s.clear()
	return s.Load(ctx)
}

// clear removes every registered entry from cron and empties the bookkeeping maps.
func (s *Scheduler) clear() {
	s.mu.Lock()
	ids := make([]cron.EntryID, 0, len(s.entries))
	for _, id := range s.entries {
		ids = append(ids, id)
	}
	s.jobs = make(map[string]func())
	s.entries = make(map[string]cron.EntryID)
	s.mu.Unlock()

	for _, id := range ids {
		s.cron.Remove(id)
	}
}

// register validates and registers a single schedule. A bad cron spec is logged
// and skipped; registration is idempotent per name (caller clears old entries).
func (s *Scheduler) register(sc config.ScheduleConfig) {
	if _, err := cron.ParseStandard(sc.Cron); err != nil {
		slog.Warn("scheduler: skipping invalid cron expression",
			"schedule", sc.Name, "cron", sc.Cron, "error", err)
		return
	}

	job := s.makeJob(sc)

	id, err := s.cron.AddFunc(sc.Cron, job)
	if err != nil {
		// Should not happen after ParseStandard succeeds, but stay defensive.
		slog.Warn("scheduler: failed to add schedule", "schedule", sc.Name, "error", err)
		return
	}

	s.mu.Lock()
	s.jobs[sc.Name] = job
	s.entries[sc.Name] = id
	s.mu.Unlock()
}

// makeJob builds the func cron runs: execute the prompt via RunFunc, record
// last-run time, and recover panics so a single job never kills the scheduler.
// The recover here makes direct invocation (tests) panic-safe too; the cron
// chain's Recover wrapper covers the scheduled path identically.
func (s *Scheduler) makeJob(sc config.ScheduleConfig) func() {
	return func() {
		defer func() {
			if r := recover(); r != nil {
				slog.Error("scheduler: job panicked",
					"schedule", sc.Name, "channel", sc.Channel, "panic", r)
			}
		}()

		ctx := context.Background()
		if err := s.run(ctx, sc.Prompt, sc.Channel); err != nil {
			slog.Error("scheduler: run failed",
				"schedule", sc.Name, "channel", sc.Channel, "error", err)
			return // skip SetLastRun on failure so a retry/inspection is possible
		}
		if s.store != nil {
			if err := s.store.SetLastRun(ctx, sc.Name, time.Now()); err != nil {
				slog.Error("scheduler: set last run failed", "schedule", sc.Name, "error", err)
			}
		}
	}
}
