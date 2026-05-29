package memory

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// Summarizer condenses old messages into a single summary string. Implemented in
// Plan 3 by the agent's model; tests use a fake.
type Summarizer interface {
	Summarize(ctx context.Context, msgs []Message) (string, error)
}

// MaybeSummarize checks whether the session's working window exceeds tokenBudget.
// If so, it summarizes the oldest chunk (messages that fall outside the newest
// budget-fitting window and are not already covered by a prior summary) into a new
// summaries row. Raw messages are retained; the window is conceptually trimmed
// because LoadWindow injects the latest summary at the top of context.
func (s *Store) MaybeSummarize(ctx context.Context, sessionID string, sum Summarizer, tokenBudget int) error {
	// Highest message id already covered by an existing summary (0 if none).
	var coveredUpTo int64
	err := s.db.QueryRowContext(ctx,
		`SELECT COALESCE(MAX(covers_up_to_msg_id), 0) FROM summaries WHERE session_id = ?`, sessionID).Scan(&coveredUpTo)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("read summary coverage: %w", err)
	}

	// Load all uncovered messages, newest-first, to compute the budget split.
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, session_id, role, content, token_count, created_at
		   FROM messages WHERE session_id = ? AND id > ? ORDER BY id DESC`, sessionID, coveredUpTo)
	if err != nil {
		return fmt.Errorf("query uncovered messages: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var newestFirst []Message
	total := 0
	for rows.Next() {
		var m Message
		var role string
		if err := rows.Scan(&m.ID, &m.SessionID, &role, &m.Content, &m.Tokens, &m.CreatedAt); err != nil {
			return fmt.Errorf("scan uncovered: %w", err)
		}
		m.Role = Role(role)
		newestFirst = append(newestFirst, m)
		total += m.Tokens
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("uncovered rows: %w", err)
	}

	if total <= tokenBudget {
		return nil // window fits; nothing to summarize
	}

	// Walk newest-first accumulating up to budget; the remainder (older) is the chunk.
	kept := 0
	acc := 0
	for _, m := range newestFirst {
		if acc+m.Tokens > tokenBudget && kept > 0 {
			break
		}
		acc += m.Tokens
		kept++
	}
	if kept >= len(newestFirst) {
		return nil // everything fit after all
	}
	// Oldest chunk = entries after the kept newest ones; convert to chronological.
	chunkNewestFirst := newestFirst[kept:]
	chunk := make([]Message, len(chunkNewestFirst))
	for i, m := range chunkNewestFirst {
		chunk[len(chunkNewestFirst)-1-i] = m
	}
	if len(chunk) == 0 {
		return nil
	}

	text, err := sum.Summarize(ctx, chunk)
	if err != nil {
		return fmt.Errorf("summarize chunk: %w", err)
	}
	coversUpTo := chunk[len(chunk)-1].ID // highest id in the summarized chunk
	if _, err := s.db.ExecContext(ctx,
		`INSERT INTO summaries(session_id, summary_text, covers_up_to_msg_id, created_at) VALUES (?, ?, ?, ?)`,
		sessionID, text, coversUpTo, time.Now().UTC()); err != nil {
		return fmt.Errorf("insert summary: %w", err)
	}
	return nil
}
