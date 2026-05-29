package memory

import (
	"context"
	"fmt"
	"time"
)

// UsageKind classifies a usage row.
type UsageKind string

const (
	// UsageChat is token usage from a chat/generation call.
	UsageChat UsageKind = "chat"
	// UsageEmbedding is token usage from an embedding call.
	UsageEmbedding UsageKind = "embedding"
	// UsageSummary is token usage from a summarization call.
	UsageSummary UsageKind = "summary"
	// UsageSubagent is token usage attributed to a sub-agent run.
	UsageSubagent UsageKind = "subagent"
)

// UsageTotals are summed input/output token counts.
type UsageTotals struct {
	InputTokens  int
	OutputTokens int
}

// UsageRollup holds 1d/1w/1m totals.
type UsageRollup struct {
	Day   UsageTotals
	Week  UsageTotals
	Month UsageTotals
}

// RecordUsage appends a usage row stamped at now (UTC).
func (s *Store) RecordUsage(ctx context.Context, provider, model string, in, out int, kind UsageKind) error {
	if _, err := s.db.ExecContext(ctx,
		`INSERT INTO usage(ts, provider, model, input_tokens, output_tokens, kind) VALUES (?, ?, ?, ?, ?, ?)`,
		time.Now().UTC(), provider, model, in, out, string(kind)); err != nil {
		return fmt.Errorf("insert usage: %w", err)
	}
	return nil
}

// UsageRollups sums input/output tokens over the last 1 day, 7 days, and 30 days.
func (s *Store) UsageRollups(ctx context.Context) (UsageRollup, error) {
	now := time.Now().UTC()
	sumSince := func(since time.Time) (UsageTotals, error) {
		var tot UsageTotals
		err := s.db.QueryRowContext(ctx,
			`SELECT COALESCE(SUM(input_tokens), 0), COALESCE(SUM(output_tokens), 0)
			   FROM usage WHERE ts >= ?`, since).Scan(&tot.InputTokens, &tot.OutputTokens)
		if err != nil {
			return UsageTotals{}, fmt.Errorf("sum usage since %s: %w", since, err)
		}
		return tot, nil
	}
	day, err := sumSince(now.Add(-24 * time.Hour))
	if err != nil {
		return UsageRollup{}, err
	}
	week, err := sumSince(now.Add(-7 * 24 * time.Hour))
	if err != nil {
		return UsageRollup{}, err
	}
	month, err := sumSince(now.Add(-30 * 24 * time.Hour))
	if err != nil {
		return UsageRollup{}, err
	}
	return UsageRollup{Day: day, Week: week, Month: month}, nil
}
