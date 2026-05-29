package memory

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// AppendMessage persists a message and returns its new id. CreatedAt defaults to now.
func (s *Store) AppendMessage(ctx context.Context, m Message) (int64, error) {
	if m.CreatedAt.IsZero() {
		m.CreatedAt = time.Now().UTC()
	}
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO messages(session_id, role, content, token_count, created_at) VALUES (?, ?, ?, ?, ?)`,
		m.SessionID, string(m.Role), m.Content, m.Tokens, m.CreatedAt)
	if err != nil {
		return 0, fmt.Errorf("insert message: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("message last insert id: %w", err)
	}
	return id, nil
}

// LoadWindow returns the latest summary text (if any) for the session and the most
// recent messages whose cumulative token_count fits within tokenBudget. Messages are
// selected newest-first to honor the budget, then returned in chronological order.
func (s *Store) LoadWindow(ctx context.Context, sessionID string, tokenBudget int) (string, []Message, error) {
	var summary string
	err := s.db.QueryRowContext(ctx,
		`SELECT summary_text FROM summaries WHERE session_id = ? ORDER BY id DESC LIMIT 1`, sessionID).Scan(&summary)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return "", nil, fmt.Errorf("query summary: %w", err)
	}
	if errors.Is(err, sql.ErrNoRows) {
		summary = ""
	}

	rows, err := s.db.QueryContext(ctx,
		`SELECT id, session_id, role, content, token_count, created_at
		   FROM messages WHERE session_id = ? ORDER BY id DESC`, sessionID)
	if err != nil {
		return "", nil, fmt.Errorf("query window: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var selected []Message
	total := 0
	for rows.Next() {
		var m Message
		var role string
		if err := rows.Scan(&m.ID, &m.SessionID, &role, &m.Content, &m.Tokens, &m.CreatedAt); err != nil {
			return "", nil, fmt.Errorf("scan window row: %w", err)
		}
		m.Role = Role(role)
		if total+m.Tokens > tokenBudget && len(selected) > 0 {
			break
		}
		selected = append(selected, m)
		total += m.Tokens
	}
	if err := rows.Err(); err != nil {
		return "", nil, fmt.Errorf("window rows: %w", err)
	}
	// selected is newest-first; reverse to chronological.
	for i, j := 0, len(selected)-1; i < j; i, j = i+1, j-1 {
		selected[i], selected[j] = selected[j], selected[i]
	}
	return summary, selected, nil
}
