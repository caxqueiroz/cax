package memory

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/caxqueiroz/cax/internal/memory/memorydb"
)

// AppendMessage persists a message and returns its new id. CreatedAt defaults to now.
func (s *Store) AppendMessage(ctx context.Context, m Message) (int64, error) {
	if m.CreatedAt.IsZero() {
		m.CreatedAt = time.Now().UTC()
	}
	id, err := s.queries.AppendMessage(ctx, memorydb.AppendMessageParams{
		SessionID:  m.SessionID,
		Role:       string(m.Role),
		Content:    m.Content,
		TokenCount: int64(m.Tokens),
		CreatedAt:  m.CreatedAt,
	})
	if err != nil {
		return 0, fmt.Errorf("insert message: %w", err)
	}
	return id, nil
}

// LoadWindow returns the latest summary text (if any) for the session and the most
// recent messages whose cumulative token_count fits within tokenBudget. Messages are
// selected newest-first to honor the budget, then returned in chronological order.
func (s *Store) LoadWindow(ctx context.Context, sessionID string, tokenBudget int) (string, []Message, error) {
	summary, err := s.queries.LatestSummaryText(ctx, sessionID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return "", nil, fmt.Errorf("query summary: %w", err)
	}
	if errors.Is(err, sql.ErrNoRows) {
		summary = ""
	}

	rows, err := s.queries.MessagesForSession(ctx, sessionID)
	if err != nil {
		return "", nil, fmt.Errorf("query window: %w", err)
	}

	var selected []Message
	total := 0
	for _, r := range rows {
		m := Message{
			ID:        r.ID,
			SessionID: r.SessionID,
			Role:      Role(r.Role),
			Content:   r.Content,
			Tokens:    int(r.TokenCount),
			CreatedAt: r.CreatedAt,
		}
		if total+m.Tokens > tokenBudget && len(selected) > 0 {
			break
		}
		selected = append(selected, m)
		total += m.Tokens
	}
	// selected is newest-first; reverse to chronological.
	slices.Reverse(selected)
	return summary, selected, nil
}
