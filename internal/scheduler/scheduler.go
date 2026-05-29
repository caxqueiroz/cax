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

// New constructs a Scheduler. The cron instance recovers job panics and bridges
// cron's logger to log/slog (configured in Task 4).
func New(store *memory.Store, run RunFunc) *Scheduler {
	return &Scheduler{
		cron:    cron.New(),
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

// makeJob builds the func cron runs: execute the prompt via RunFunc, then record
// last-run time. Run errors are logged and do not stop the scheduler.
func (s *Scheduler) makeJob(sc config.ScheduleConfig) func() {
	return func() {
		ctx := context.Background()
		if err := s.run(ctx, sc.Prompt, sc.Channel); err != nil {
			slog.Error("scheduler: run failed",
				"schedule", sc.Name, "channel", sc.Channel, "error", err)
		}
		if s.store != nil {
			if err := s.store.SetLastRun(ctx, sc.Name, time.Now()); err != nil {
				slog.Error("scheduler: set last run failed", "schedule", sc.Name, "error", err)
			}
		}
	}
}
