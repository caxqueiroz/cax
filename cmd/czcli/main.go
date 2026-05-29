// Command czcli runs the assistant as a Bubble Tea TUI: it loads config, wires
// the memory store, multi-provider model, and dive agent, then launches the CLI
// channel which renders the dashboard and streams replies live.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/caxqueiroz/czcli/internal/agent"
	"github.com/caxqueiroz/czcli/internal/channel"
	"github.com/caxqueiroz/czcli/internal/channel/cli"
	"github.com/caxqueiroz/czcli/internal/config"
	"github.com/caxqueiroz/czcli/internal/memory"
	"github.com/caxqueiroz/czcli/internal/scheduler"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "czcli: %v\n", err)
		os.Exit(1)
	}
}

// run loads config, wires dependencies, and launches the TUI channel.
func run() error {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	path := os.Getenv("CZCLI_CONFIG")
	if path == "" {
		path = "config.yaml"
	}
	cfg, err := config.Load(path)
	if err != nil {
		return fmt.Errorf("load config from %q (set CZCLI_CONFIG to override): %w", path, err)
	}

	embedder, err := memory.NewEmbedder(cfg.Embeddings)
	if err != nil {
		return fmt.Errorf("build embedder: %w", err)
	}

	store, err := memory.Open(cfg.Memory, embedder)
	if err != nil {
		return fmt.Errorf("open memory store: %w", err)
	}
	defer func() {
		if cerr := store.Close(); cerr != nil {
			slog.Warn("close store", "err", cerr)
		}
	}()

	model, err := agent.BuildModel(cfg)
	if err != nil {
		return fmt.Errorf("build model: %w", err)
	}

	assistant, err := agent.Build(ctx, cfg, store, model)
	if err != nil {
		return fmt.Errorf("build assistant: %w", err)
	}

	// Seed config-defined schedules into the store (idempotent) so they
	// participate in the scheduler's Load/Reload alongside CLI-added ones.
	for _, sc := range cfg.Schedules {
		if err := store.UpsertSchedule(ctx, sc); err != nil {
			slog.Warn("scheduler: failed to seed schedule from config", "schedule", sc.Name, "error", err)
		}
	}

	sched := scheduler.New(store, schedulerRunFunc(assistant))
	if err := sched.Load(ctx); err != nil {
		slog.Error("scheduler: initial load failed", "error", err)
		// Non-fatal: continue without schedules rather than aborting startup.
	} else {
		sched.Start()
		slog.Info("scheduler started")
	}
	defer sched.Stop()

	ch := cli.New(
		cli.WithSessionID("cli"),
		cli.WithScheduler(scheduleAdapter{store: store, sched: sched}),
	)
	if err := ch.Start(ctx, assistant.Handle, assistant.Status); err != nil {
		return fmt.Errorf("run cli channel: %w", err)
	}
	return nil
}

// schedulerRunFunc builds a scheduler.RunFunc that runs a stored prompt through
// the agent under a synthetic per-channel session and routes the reply to the
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

// scheduleAdapter satisfies the CLI's /schedule CRUD backend over the memory
// store and the live scheduler. List/Upsert hit the store; Reload re-registers
// cron entries so changes take effect immediately.
type scheduleAdapter struct {
	store *memory.Store
	sched *scheduler.Scheduler
}

func (a scheduleAdapter) List(ctx context.Context) ([]config.ScheduleConfig, error) {
	return a.store.ListSchedules(ctx)
}

func (a scheduleAdapter) Upsert(ctx context.Context, sc config.ScheduleConfig) error {
	return a.store.UpsertSchedule(ctx, sc)
}

func (a scheduleAdapter) Reload(ctx context.Context) error {
	return a.sched.Reload(ctx)
}
