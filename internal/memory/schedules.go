package memory

import (
	"context"
	"fmt"
	"time"

	"github.com/caxqueiroz/cax/internal/config"
	"github.com/caxqueiroz/cax/internal/memory/memorydb"
)

// ListSchedules returns all persisted schedules ordered by name.
func (s *Store) ListSchedules(ctx context.Context) ([]config.ScheduleConfig, error) {
	rows, err := s.queries.ListSchedules(ctx)
	if err != nil {
		return nil, fmt.Errorf("query schedules: %w", err)
	}
	out := make([]config.ScheduleConfig, 0, len(rows))
	for _, r := range rows {
		out = append(out, config.ScheduleConfig{
			Name:    r.Name,
			Cron:    r.CronExpr,
			Prompt:  r.Prompt,
			Channel: r.Channel,
			Enabled: r.Enabled != 0,
		})
	}
	return out, nil
}

// UpsertSchedule inserts a schedule or updates it in place when the name exists.
func (s *Store) UpsertSchedule(ctx context.Context, sc config.ScheduleConfig) error {
	enabled := int64(0)
	if sc.Enabled {
		enabled = 1
	}
	if err := s.queries.UpsertSchedule(ctx, memorydb.UpsertScheduleParams{
		Name:     sc.Name,
		CronExpr: sc.Cron,
		Prompt:   sc.Prompt,
		Channel:  sc.Channel,
		Enabled:  enabled,
	}); err != nil {
		return fmt.Errorf("upsert schedule %q: %w", sc.Name, err)
	}
	return nil
}

// SetLastRun records the last-run timestamp for the named schedule.
func (s *Store) SetLastRun(ctx context.Context, name string, t time.Time) error {
	utc := t.UTC()
	n, err := s.queries.MarkScheduleRun(ctx, memorydb.MarkScheduleRunParams{
		LastRun: &utc,
		Name:    name,
	})
	if err != nil {
		return fmt.Errorf("set last_run for %q: %w", name, err)
	}
	if n == 0 {
		return fmt.Errorf("set last_run: no schedule named %q", name)
	}
	return nil
}
