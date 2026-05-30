package memory

import (
	"context"
	"fmt"
	"time"

	"github.com/caxqueiroz/cax/internal/config"
)

// ListSchedules returns all persisted schedules ordered by name.
func (s *Store) ListSchedules(ctx context.Context) ([]config.ScheduleConfig, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT name, cron_expr, prompt, channel, enabled FROM schedules ORDER BY name`)
	if err != nil {
		return nil, fmt.Errorf("query schedules: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []config.ScheduleConfig
	for rows.Next() {
		var sc config.ScheduleConfig
		var enabled int
		if err := rows.Scan(&sc.Name, &sc.Cron, &sc.Prompt, &sc.Channel, &enabled); err != nil {
			return nil, fmt.Errorf("scan schedule: %w", err)
		}
		sc.Enabled = enabled != 0
		out = append(out, sc)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("schedule rows: %w", err)
	}
	return out, nil
}

// UpsertSchedule inserts a schedule or updates it in place when the name exists.
func (s *Store) UpsertSchedule(ctx context.Context, sc config.ScheduleConfig) error {
	enabled := 0
	if sc.Enabled {
		enabled = 1
	}
	if _, err := s.db.ExecContext(ctx,
		`INSERT INTO schedules(name, cron_expr, prompt, channel, enabled)
		      VALUES (?, ?, ?, ?, ?)
		 ON CONFLICT(name) DO UPDATE SET
		      cron_expr = excluded.cron_expr,
		      prompt    = excluded.prompt,
		      channel   = excluded.channel,
		      enabled   = excluded.enabled`,
		sc.Name, sc.Cron, sc.Prompt, sc.Channel, enabled); err != nil {
		return fmt.Errorf("upsert schedule %q: %w", sc.Name, err)
	}
	return nil
}

// SetLastRun records the last-run timestamp for the named schedule.
func (s *Store) SetLastRun(ctx context.Context, name string, t time.Time) error {
	res, err := s.db.ExecContext(ctx,
		`UPDATE schedules SET last_run = ? WHERE name = ?`, t.UTC(), name)
	if err != nil {
		return fmt.Errorf("set last_run for %q: %w", name, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("set last_run rows affected: %w", err)
	}
	if n == 0 {
		return fmt.Errorf("set last_run: no schedule named %q", name)
	}
	return nil
}
