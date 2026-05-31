package memory

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/caxqueiroz/cax/internal/memory/memorydb"
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
	if err := s.queries.RecordUsage(ctx, memorydb.RecordUsageParams{
		Ts:           time.Now().UTC(),
		Provider:     sql.NullString{String: provider, Valid: provider != ""},
		Model:        sql.NullString{String: model, Valid: model != ""},
		InputTokens:  int64(in),
		OutputTokens: int64(out),
		Kind:         string(kind),
	}); err != nil {
		return fmt.Errorf("insert usage: %w", err)
	}
	return nil
}

// UsageRollups sums input/output tokens over the last 1 day, 7 days, and 30 days.
func (s *Store) UsageRollups(ctx context.Context) (UsageRollup, error) {
	now := time.Now().UTC()
	sumSince := func(since time.Time) (UsageTotals, error) {
		row, err := s.queries.UsageRollup(ctx, since)
		if err != nil {
			return UsageTotals{}, fmt.Errorf("sum usage since %s: %w", since, err)
		}
		// sqlc infers int64 for both, but the COALESCE expression returns
		// an INT64 in modernc.org/sqlite; safe to int() truncate for the
		// public surface.
		return UsageTotals{InputTokens: int(row.InputTokens), OutputTokens: int(row.OutputTokens)}, nil
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
