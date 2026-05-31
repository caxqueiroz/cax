package scheduler

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/caxqueiroz/cax/internal/config"
	"github.com/caxqueiroz/cax/internal/memory"
)

func TestJobRecordsLastRun(t *testing.T) {
	ctx := t.Context()
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
