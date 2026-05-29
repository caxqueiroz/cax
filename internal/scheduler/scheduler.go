// Package scheduler runs stored prompts through the agent on cron schedules,
// loading schedules from the memory store and routing each run's output to a
// named channel. Invalid cron specs are skipped, job panics are recovered, and
// run errors are logged — none of which stop the scheduler.
package scheduler

import (
	"context"
	"sync"

	"github.com/robfig/cron/v3"

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
