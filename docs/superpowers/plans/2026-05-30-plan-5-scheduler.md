# Plan 5: Scheduler — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build `internal/scheduler`, a `robfig/cron/v3`-backed scheduler that loads enabled schedules from `memory.Store`, runs each stored prompt through the agent on its cron expression (routing output to a named channel and recording last-run time), and wire it into `cmd/czcli/main.go` with `/schedule` CRUD support.

**Architecture:** `scheduler.Scheduler` wraps a `*cron.Cron` and a `memory.Store`. `Load`/`Reload` read enabled `config.ScheduleConfig` rows, validate each cron spec (skip+log invalid ones, never crash), and register one cron entry per schedule whose job calls the injected `RunFunc(ctx, prompt, channel)` then `store.SetLastRun(name, now)`. The cron instance uses a panic-recovering job-wrapper chain bridged to `log/slog`, so a panicking or erroring job never kills the cron goroutine and never stops the scheduler. `cmd/czcli/main.go` constructs a `RunFunc` adapter over `agent.Assistant.Handle` (synthesizing a `channel.Message{SessionID: "scheduler:<name>", Text: prompt}` and routing the reply to the configured channel — MVP prints/logs), starts the scheduler on launch, and stops it on shutdown. `/schedule` slash-commands (parsed in the Plan-4 CLI) call store-backed CRUD helpers then `Scheduler.Reload`.

**Tech Stack:** Go 1.24+ (toolchain present: 1.26), `github.com/robfig/cron/v3 v3.0.1`, `log/slog`, `internal/memory` (Plan 2), `internal/config` (Plan 1), `internal/channel` (Plan 1), `internal/agent` (Plan 3).

---

## Research notes (authoritative — verified 2026-05-30)

- **Version:** `github.com/robfig/cron/v3` latest = **v3.0.1** (only tagged v3 releases: rc1, v3.0.0, v3.0.1). Pin `v3.0.1`.
- **API (from `go doc` on the installed v3.0.1):**
  - `cron.New(opts ...Option) *Cron`. Default chain already "recovers panics and logs them to stderr"; we override the logger to a slog bridge via `cron.WithChain(cron.Recover(logger))` + `cron.WithLogger(logger)` for deterministic, structured logging.
  - `(*Cron).AddFunc(spec string, cmd func()) (EntryID, error)` — registers a job; returns an opaque `EntryID` (`type EntryID int`).
  - `(*Cron).Start()` — runs scheduler in its own goroutine; no-op if already started.
  - `(*Cron).Stop() context.Context` — stops the scheduler; returns a ctx that is Done once running jobs finish. Does **not** stop in-flight jobs.
  - `(*Cron).Remove(id EntryID)` — removes a future entry (used by `Reload` to clear old entries).
  - `cron.ParseStandard(spec string) (Schedule, error)` — validates a 5-field standard spec (and descriptors like `@every 1h30m`, `@daily`). We use this to validate-before-register so a bad spec is logged + skipped instead of failing the whole load.
  - `cron.Recover(logger Logger) JobWrapper` and `cron.WithChain(...)` — wrap every job to recover panics; `cron.Logger` is a 2-method interface (`Info`, `Error`) — we adapt slog to it.
  - `cron.DiscardLogger` — silent logger, handy in tests.
- **Seconds field:** NOT enabled. We use the default 5-field standard parser (minute-resolution), matching the spec's `schedules(cron_expr)` examples. (If seconds are ever needed, switch to `cron.New(cron.WithSeconds())` + `cron.NewParser(cron.SecondOptional|...)` for validation.)
- **Deterministic testing approach (no real-time waits):** the scheduler keeps an internal `map[string]func()` of the registered job funcs keyed by schedule name. Registration both calls `AddFunc(spec, job)` (so the cron entry exists / spec is validated) **and** stores `job` in the map. Tests then **invoke the stored job func directly** (`s.jobFor("nightly")()`) and assert that the fake `RunFunc` was called with the right prompt/channel and that `store.SetLastRun` was recorded — with **zero** clock dependency, so tests complete in microseconds. Invalid-cron handling is tested by registering a schedule with a garbage spec and asserting `Load` returns no error, logs a skip, and registers no job. Panic recovery is tested by injecting a `RunFunc` that panics and asserting the job func does not propagate the panic (recovered by the cron chain wrapper, which we also apply when invoking directly — see Task 4). This is preferred over a fake clock because robfig/cron's clock is unexported and not injectable in v3; direct job invocation is both simpler and exact.

---

## File Structure

```
internal/scheduler/
├── scheduler.go        # Scheduler, New, RunFunc, Load, Reload, Start, Stop, slog bridge (Tasks 1–6)
└── scheduler_test.go   # table/behavioral tests (Tasks 1–6)

cmd/czcli/main.go       # construct RunFunc adapter, start on launch, stop on shutdown (Task 7)
```

Dependencies assumed to already exist (do NOT define here):
- Plan 2: `memory.Store` with `ListSchedules(ctx) ([]config.ScheduleConfig, error)`, `UpsertSchedule(ctx, config.ScheduleConfig) error`, `SetLastRun(ctx, name string, t time.Time) error`; `memory.Open(...)`.
- Plan 1: `config.ScheduleConfig{Name, Cron, Prompt, Channel string; Enabled bool}`.
- Plan 3: `agent.Assistant.Handle(ctx, channel.Message, channel.EventSink) (channel.Reply, error)`; Plan 1: `channel.Message{SessionID, Text string}`, `channel.Reply{Text string}`, `channel.EventSink`, `channel.StreamEvent`.

Contract signatures (verbatim from `00-shared-contracts.md` §Scheduler):
```go
type RunFunc func(ctx context.Context, prompt, channel string) error
type Scheduler struct { /* cron *cron.Cron, store *memory.Store, run RunFunc */ }
func New(store *memory.Store, run RunFunc) *Scheduler
func (s *Scheduler) Load(ctx context.Context) error
func (s *Scheduler) Start()
func (s *Scheduler) Stop()
```
This plan adds (not in contracts, additive only): `func (s *Scheduler) Reload(ctx context.Context) error` and an internal job map. These do not change any contract signature.

---

### Task 1 — Package skeleton, `New`, `RunFunc`, no-op `Start`/`Stop`

**Files:** `internal/scheduler/scheduler.go`, `internal/scheduler/scheduler_test.go`

- [ ] Add the dependency: `go get github.com/robfig/cron/v3@v3.0.1`.

- [ ] Write the FAILING test `scheduler_test.go` (constructs a `Scheduler` with a nil store and a recording `RunFunc`, asserts `New` returns non-nil and that `Start`/`Stop` are safe to call without registered jobs):

```go
package scheduler

import (
	"context"
	"testing"
)

func TestNewStartStop(t *testing.T) {
	called := 0
	run := func(ctx context.Context, prompt, ch string) error {
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
```

- [ ] Run `go test ./internal/scheduler/...` → FAIL (package/symbols do not exist).

- [ ] Minimal impl `scheduler.go`:

```go
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
	jobs    map[string]func()        // schedule name -> registered job func (for tests + reload)
	entries map[string]cron.EntryID  // schedule name -> cron entry id (for reload removal)
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
```

- [ ] Run `go test ./internal/scheduler/...` → PASS.

- [ ] Commit: `feat(scheduler): add package skeleton with New/Start/Stop`.

---

### Task 2 — `Load`: register enabled schedules and wire the job (run + SetLastRun)

**Files:** `internal/scheduler/scheduler.go`, `internal/scheduler/scheduler_test.go`

This task introduces a tiny in-test fake `memory.Store`-shaped dependency. Because `memory.Store` is a concrete struct (not an interface) in the contracts, the scheduler holds `*memory.Store` directly. For instant unit tests we therefore use a **real** `memory.Store` opened against an in-memory/temp DB via the Plan-2 `memory.Open` helper with a deterministic fake embedder. The test seeds schedules with `UpsertSchedule` and asserts behavior through `ListSchedules`/`SetLastRun` via `store`. (The Plan-2 test helper `memory.OpenTestStore(t)` is assumed available; if not, open a temp DB with `memory.Open(config.MemoryConfig{DBPath: filepath.Join(t.TempDir(), "m.db"), ...}, fakeEmbedder)`.)

- [ ] Write the FAILING test (seed one enabled + one disabled schedule, call `Load`, invoke the enabled job func directly, assert the `RunFunc` got the right prompt/channel and that `SetLastRun` updated `last_run`; assert the disabled schedule registered no job):

```go
package scheduler

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/caxqueiroz/czcli/internal/config"
	"github.com/caxqueiroz/czcli/internal/memory"
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

func TestLoadRegistersEnabledAndRunsJob(t *testing.T) {
	ctx := context.Background()
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

	before := time.Now()
	job() // invoke the registered job func directly — deterministic, no clock wait

	if len(got) != 1 || got[0].prompt != "daily report" || got[0].channel != "cli" {
		t.Fatalf("RunFunc not invoked correctly: %+v", got)
	}

	// SetLastRun must have been recorded (>= before).
	scheds, err := st.ListSchedules(ctx)
	if err != nil {
		t.Fatalf("ListSchedules: %v", err)
	}
	var lastRunSet bool
	for _, sc := range scheds {
		if sc.Name == "nightly" {
			// ListSchedules returns config.ScheduleConfig which has no last_run field;
			// verify via store state instead: re-running SetLastRun semantics is covered
			// by Plan 2. Here we assert the job did not error and was registered.
			lastRunSet = true
		}
	}
	if !lastRunSet {
		t.Fatal("nightly schedule missing after load")
	}
	_ = before
}
```

> Note: `config.ScheduleConfig` carries no `last_run` field, so the test asserts `SetLastRun` was *called* by spying through the store is not directly observable via `ListSchedules`. Task 3 adds a dedicated, observable last-run assertion using a thin wrapper. For Task 2, asserting the `RunFunc` invocation + successful registration is sufficient and keeps the step bite-sized.

- [ ] Run `go test ./internal/scheduler/...` → FAIL (`Load` undefined).

- [ ] Minimal impl — add `Load` and the registration helper to `scheduler.go`:

```go
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
```

> Remove the now-unused `_ = before` placeholder from the test if `go vet` complains; keep imports tidy.

- [ ] Run `go test ./internal/scheduler/...` → PASS.

- [ ] Commit: `feat(scheduler): load enabled schedules and run prompts via RunFunc`.

---

### Task 3 — Verify `SetLastRun` is recorded (observable last-run assertion)

**Files:** `internal/scheduler/scheduler_test.go`

Because `config.ScheduleConfig` has no `last_run` field, we make the last-run recording observable by spying on the store through a small *seam*: the scheduler already calls `s.store.SetLastRun`. We assert it indirectly by querying the `schedules.last_run` column via the store's DB is not exported; instead, assert through a **counting RunFunc + a second invocation** is insufficient. The robust, contract-only approach: add a Plan-2-provided read path if available; otherwise assert via a fake that wraps the real store is impossible (concrete type). Therefore this task asserts last-run by querying through the store's `ListSchedules` ONLY if Plan 2 extends `ScheduleConfig` — which it does not. **Resolution:** assert last-run by opening the same DB file read-only in the test and selecting `last_run`.

- [ ] Write the FAILING/observability test (open the store's DB file directly to read `last_run` after a job run):

```go
package scheduler

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/caxqueiroz/czcli/internal/config"
	"github.com/caxqueiroz/czcli/internal/memory"
)

func TestJobRecordsLastRun(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "mem.db")

	st, err := memory.Open(config.MemoryConfig{
		DBPath: dbPath, TokenBudget: 8000, RecallK: 5,
	}, fakeEmbedder{dim: 8})
	if err != nil {
		t.Fatalf("memory.Open: %v", err)
	}
	defer func() { _ = st.Close() }()

	if err := st.UpsertSchedule(ctx, config.ScheduleConfig{
		Name: "nightly", Cron: "0 0 * * *", Prompt: "p", Channel: "cli", Enabled: true,
	}); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	s := New(st, func(context.Context, string, string) error { return nil })
	if err := s.Load(ctx); err != nil {
		t.Fatalf("Load: %v", err)
	}

	before := time.Now().Add(-time.Second)
	s.jobs["nightly"]() // direct invoke

	// Read last_run directly from the DB (read-only) to verify SetLastRun ran.
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer func() { _ = db.Close() }()

	var lastRun sql.NullTime
	if err := db.QueryRowContext(ctx,
		`SELECT last_run FROM schedules WHERE name = ?`, "nightly").Scan(&lastRun); err != nil {
		t.Fatalf("query last_run: %v", err)
	}
	if !lastRun.Valid {
		t.Fatal("last_run not set after job run")
	}
	if lastRun.Time.Before(before) {
		t.Fatalf("last_run %v is before job start %v", lastRun.Time, before)
	}
}
```

> If the Plan-2 schema stores `last_run` as TEXT rather than a TIMESTAMP scannable into `sql.NullTime`, scan into `sql.NullString` and assert non-empty instead. The schema in 00-shared-contracts.md declares `last_run TIMESTAMP`, so `sql.NullTime` is the primary path.

- [ ] Run `go test ./internal/scheduler/...` → PASS (the impl from Task 2 already records last-run; this test makes it observable). If it FAILS because last-run isn't recorded, fix `makeJob` (it should already call `SetLastRun`).

- [ ] Commit: `test(scheduler): assert last_run is recorded after a job run`.

---

### Task 4 — Panic recovery and structured logging via slog-bridged cron logger

**Files:** `internal/scheduler/scheduler.go`, `internal/scheduler/scheduler_test.go`

Cron's default chain recovers panics to stderr; we make recovery explicit and route it through `log/slog`. We also ensure a job func that panics, when run through the cron chain, does not propagate. Since tests invoke the stored job func **directly** (bypassing cron's chain), we wrap each stored job func in our own recover so both the cron path and the direct-invoke path are panic-safe and identical.

- [ ] Write the FAILING test (RunFunc panics; the registered job func must not propagate the panic, and last-run/run-error handling must continue gracefully):

```go
func TestJobRecoversPanic(t *testing.T) {
	ctx := context.Background()
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
```

- [ ] Run `go test ./internal/scheduler/...` → FAIL (panic propagates: `makeJob` has no recover yet).

- [ ] Minimal impl — (a) add a slog→cron logger bridge, (b) configure `cron.New` with it + `cron.Recover`, (c) wrap the stored job func with an in-func recover so direct invocation is also panic-safe. Update `New` and `makeJob`:

```go
// slogCronLogger adapts log/slog to cron.Logger (Info/Error).
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
```

```go
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
```

> Design choice: on RunFunc error or panic we **do not** call `SetLastRun` (last-run reflects successful runs). This matches "job failures are logged and do not stop the scheduler." Update `TestJobRecordsLastRun` only if you'd previously asserted last-run on failure (it asserts on success, so no change needed). `TestLoadRegistersEnabledAndRunsJob` still passes (RunFunc returns nil there).

- [ ] Run `go test ./internal/scheduler/...` → PASS (panic recovered, no propagation).

- [ ] Commit: `feat(scheduler): recover job panics and bridge cron logging to slog`.

---

### Task 5 — Invalid cron expression is logged and skipped (no crash, no job)

**Files:** `internal/scheduler/scheduler_test.go`

The skip-on-invalid logic was added in Task 2 (`register` calls `cron.ParseStandard` first). This task adds the explicit regression test.

- [ ] Write the FAILING/regression test (one valid + one invalid spec; `Load` returns nil, registers only the valid one):

```go
func TestLoadSkipsInvalidCron(t *testing.T) {
	ctx := context.Background()
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
```

- [ ] Run `go test ./internal/scheduler/...` → PASS (logic already present from Task 2).

- [ ] Commit: `test(scheduler): skip invalid cron expressions without failing load`.

---

### Task 6 — `Reload`: clear and re-register from the store (for `/schedule` CRUD)

**Files:** `internal/scheduler/scheduler.go`, `internal/scheduler/scheduler_test.go`

`/schedule` CRUD mutates the `schedules` table via the store, then the CLI calls `Reload` to apply changes live. `Reload` removes all current cron entries (and clears the job/entry maps) then re-runs `Load`. Safe to call while the scheduler is running (cron supports add/remove on a running instance).

- [ ] Write the FAILING test (load one schedule, then change the store and `Reload`, asserting the new set is registered and the old one is gone):

```go
func TestReloadReflectsStoreChanges(t *testing.T) {
	ctx := context.Background()
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
```

- [ ] Run `go test ./internal/scheduler/...` → FAIL (`Reload` undefined).

- [ ] Minimal impl — add `Reload` and a `clear` helper to `scheduler.go`:

```go
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
```

- [ ] Run `go test ./internal/scheduler/...` → PASS.

- [ ] Run `golangci-lint run ./internal/scheduler/...` → clean (fix any unused imports / naming).

- [ ] Commit: `feat(scheduler): add Reload to re-register schedules after CRUD`.

---

### Task 7 — Wire scheduler into `cmd/czcli/main.go` (RunFunc adapter, start/stop) + `/schedule` CRUD seam

**Files:** `cmd/czcli/main.go`

This task constructs the `RunFunc` adapter from the agent, starts the scheduler on launch, stops it on shutdown, and documents the `/schedule` CRUD integration point. Since `cmd/czcli/main.go` is shared (Plan 3 basic → Plan 4 TUI → Plan 5 scheduler), only **add** the scheduler wiring; do not rewrite existing wiring.

- [ ] Add a `schedulerRunFunc` adapter near the other wiring in `main.go`. It synthesizes a `channel.Message`, calls `assistant.Handle`, and routes the reply to the configured channel. For MVP, all channels collapse to log/print, but the signature is channel-aware so other channels can subscribe later:

```go
// schedulerRunFunc builds a scheduler.RunFunc that runs a stored prompt through
// the agent under a synthetic per-schedule session and routes the reply to the
// named channel. MVP: replies are logged/printed; a channel registry can route
// to Telegram/Discord/etc. later without changing this signature.
func schedulerRunFunc(assistant *agent.Assistant) scheduler.RunFunc {
	return func(ctx context.Context, prompt, ch string) error {
		msg := channel.Message{
			SessionID: "scheduler:" + ch, // distinct session per scheduled channel
			Text:      prompt,
		}
		// Drain stream events to a no-op sink for MVP; a real channel would render.
		emit := func(channel.StreamEvent) {}

		reply, err := assistant.Handle(ctx, msg, emit)
		if err != nil {
			return fmt.Errorf("scheduled run (channel=%s): %w", ch, err)
		}

		// MVP routing: log + print. Replace with a channel registry lookup later.
		slog.Info("scheduled run completed", "channel", ch, "reply_len", len(reply.Text))
		fmt.Printf("[scheduled:%s] %s\n", ch, reply.Text)
		return nil
	}
}
```

> Session ID note: the contract for the adapter says `SessionID: "scheduler:<name>"`. Two viable keys exist — by schedule name or by target channel. RunFunc only receives `prompt, channel` (not the schedule name), so the adapter uses `"scheduler:"+ch`. If per-schedule sessions are required, pass the name by closing over it during registration instead; for MVP, per-channel sessions are sufficient and match the available RunFunc signature. **This is the one place the "<name>" wording in the task brief cannot be honored verbatim because RunFunc's contract does not pass the name** — documented here, not a contract change.

- [ ] In `main`, after building the `assistant` and `store`, construct, load, and start the scheduler; defer stop:

```go
sched := scheduler.New(store, schedulerRunFunc(assistant))
if err := sched.Load(ctx); err != nil {
	slog.Error("scheduler: initial load failed", "error", err)
	// Non-fatal: continue without schedules rather than aborting startup.
} else {
	sched.Start()
	slog.Info("scheduler started")
}
defer sched.Stop()
```

- [ ] Seed schedules from config into the store on startup so `config.Schedules` and the table stay in sync (idempotent via `UpsertSchedule`), then they participate in `Load`/`Reload`:

```go
for _, sc := range cfg.Schedules {
	if err := store.UpsertSchedule(ctx, sc); err != nil {
		slog.Warn("scheduler: failed to seed schedule from config", "schedule", sc.Name, "error", err)
	}
}
```
> Place this block **before** `sched.Load(ctx)` so config-defined schedules are registered on first launch.

- [ ] Add the `/schedule` CRUD seam. The slash-command parser lives in the Plan-4 CLI; expose the store-backed operations + a reload callback to it. Pass these into the CLI channel constructor (the exact constructor field is Plan 4's; document the contract here). The CLI maps subcommands to store calls then `sched.Reload`:

```go
// scheduleCommands is the store-backed CRUD surface the CLI's /schedule
// subcommands call. Defined in main and injected into the CLI channel.
//
//   /schedule add <name> "<cron>" "<prompt>" [channel]  -> Upsert(enabled=true)  -> Reload
//   /schedule list                                       -> List
//   /schedule remove <name>                              -> Upsert(enabled=false) (soft) or delete
//   /schedule enable <name>                              -> Upsert(enabled=true)  -> Reload
//   /schedule disable <name>                             -> Upsert(enabled=false) -> Reload
//
// Each mutating subcommand calls sched.Reload(ctx) so changes take effect live.
```

  Concretely, the CLI handler implements:
  - **add / enable / disable:** read the current schedule (via `store.ListSchedules`), set `Enabled`/fields, `store.UpsertSchedule(ctx, sc)`, then `sched.Reload(ctx)`.
  - **list:** `store.ListSchedules(ctx)` and render name/cron/prompt/channel/enabled.
  - **remove:** disable (`Enabled=false` + `UpsertSchedule`) for a non-destructive MVP (a hard `DeleteSchedule` is out of scope — not in Plan 2 contracts), then `sched.Reload(ctx)`.

  > Integration point: the CLI channel must accept `*memory.Store` and a `reload func(context.Context) error` (= `sched.Reload`). Plan 4 owns the CLI struct; this plan only documents the required injection. If Plan 4's CLI constructor signature lacks these, extend it in Plan 4 (additive) — do not put command parsing in `internal/scheduler`.

- [ ] Build the whole binary to confirm wiring compiles: `go build ./...`.

- [ ] Run the full suite: `go test ./...` → PASS.

- [ ] Commit: `feat(czcli): wire scheduler start/stop and /schedule CRUD into main`.

---

## Verification checklist (run before declaring done)

- [ ] `go build ./...` succeeds.
- [ ] `go test ./internal/scheduler/...` and `go test ./...` pass.
- [ ] `golangci-lint run ./...` is clean.
- [ ] Invalid cron specs are logged + skipped (TestLoadSkipsInvalidCron).
- [ ] Job panics are recovered, scheduler keeps running (TestJobRecoversPanic).
- [ ] Run errors are logged and do not stop the scheduler (covered by makeJob; no test asserts non-stop because Stop isn't reached — the error path returns and the cron goroutine survives).
- [ ] Scheduler starts on launch and stops on shutdown (manual: start binary, Ctrl-C; observe "scheduler started" then clean stop).
- [ ] `/schedule add/list/enable/disable/remove` mutate the store and call `Reload` (manual via CLI).
- [ ] Contract signatures (`RunFunc`, `New`, `Load`, `Start`, `Stop`) match `00-shared-contracts.md` verbatim; `Reload` is additive.

## Assumptions & contract notes

1. **cron version:** pin `github.com/robfig/cron/v3 v3.0.1` (latest as of 2026-05-30). 5-field standard parser (no seconds).
2. **Testing without real time:** registered job funcs are stored in an internal `map[string]func()` and invoked directly in tests — instant and deterministic; no fake clock (cron v3's clock is unexported/non-injectable). Spec validation uses `cron.ParseStandard`.
3. **`memory.Store` is concrete** (not an interface), so scheduler tests use a real temp-DB store via `memory.Open` + a deterministic fake embedder; last-run is verified by reading the `schedules.last_run` column directly (read-only `sql.Open`).
4. **RunFunc cannot receive the schedule name** (contract passes only `prompt, channel`); the main.go adapter therefore uses `SessionID: "scheduler:"+channel`. The brief's `"scheduler:<name>"` wording is honored as best the contract allows — documented, not a contract change.
5. **`remove` is soft** (disable) since Plan 2 exposes no `DeleteSchedule`; a hard delete would require a Plan-2 contract addition.
6. **CLI injection point:** Plan 4's CLI channel must accept `*memory.Store` and `sched.Reload`; command parsing stays in the CLI, store ops + Reload are the scheduler's contribution.
