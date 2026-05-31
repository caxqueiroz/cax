package memory

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// Fact is a mem0-style atom — a single declarative statement extracted from
// conversation ("user prefers TypeScript over JavaScript"). Stored in the
// facts table; embeddings live in vec_facts. Soft-deleted via deleted_at so
// the extractor can supersede outdated facts without losing audit history.
type Fact struct {
	ID          int64
	SessionID   string
	UserID      string
	Text        string
	Kind        string
	SourceMsgID int64
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// RecalledFact mirrors Recalled but for facts. Distance is cosine distance —
// lower is closer.
type RecalledFact struct {
	ID        int64
	Text      string
	Kind      string
	Distance  float64
	CreatedAt time.Time
	UpdatedAt time.Time
}

// AddFact embeds text and writes a new row to facts + vec_facts.
func (s *Store) AddFact(ctx context.Context, f Fact) (int64, error) {
	if f.Text == "" {
		return 0, fmt.Errorf("addfact: empty text")
	}
	vecs, err := s.embedder.Embed(ctx, []string{f.Text})
	if err != nil {
		return 0, fmt.Errorf("embed fact: %w", err)
	}
	if len(vecs) != 1 || len(vecs[0]) != s.dim {
		return 0, fmt.Errorf("embed fact: bad shape len=%d dim=%d", len(vecs), s.dim)
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("begin fact tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	var srcArg any
	if f.SourceMsgID != 0 {
		srcArg = f.SourceMsgID
	}
	now := time.Now().UTC()
	res, err := tx.ExecContext(ctx,
		`INSERT INTO facts(session_id, user_id, text, kind, source_msg_id, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		f.SessionID, f.UserID, f.Text, f.Kind, srcArg, now, now)
	if err != nil {
		return 0, fmt.Errorf("insert fact: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("fact last insert: %w", err)
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO vec_facts(fact_id, embedding) VALUES (?, vec_f32(?))`,
		id, vectorString(vecs[0])); err != nil {
		return 0, fmt.Errorf("insert fact vector: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("commit fact tx: %w", err)
	}
	return id, nil
}

// UpdateFact replaces the text of an existing fact and re-embeds. Used by
// the extractor when it supersedes an old fact ("changed mind") without
// losing the row's identity.
func (s *Store) UpdateFact(ctx context.Context, id int64, newText string) error {
	if newText == "" {
		return fmt.Errorf("updatefact: empty text")
	}
	vecs, err := s.embedder.Embed(ctx, []string{newText})
	if err != nil {
		return fmt.Errorf("embed fact: %w", err)
	}
	if len(vecs) != 1 || len(vecs[0]) != s.dim {
		return fmt.Errorf("embed fact: bad shape")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin update tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	res, err := tx.ExecContext(ctx,
		`UPDATE facts SET text = ?, updated_at = ? WHERE id = ? AND deleted_at IS NULL`,
		newText, time.Now().UTC(), id)
	if err != nil {
		return fmt.Errorf("update fact: %w", err)
	}
	rows, _ := res.RowsAffected()
	if rows == 0 {
		return sql.ErrNoRows
	}
	// vec0 doesn't support UPDATE on the embedding column — DELETE + INSERT.
	if _, err := tx.ExecContext(ctx, `DELETE FROM vec_facts WHERE fact_id = ?`, id); err != nil {
		return fmt.Errorf("delete old vec: %w", err)
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO vec_facts(fact_id, embedding) VALUES (?, vec_f32(?))`,
		id, vectorString(vecs[0])); err != nil {
		return fmt.Errorf("insert new vec: %w", err)
	}
	return tx.Commit()
}

// DeleteFact soft-deletes a fact (sets deleted_at). vec_facts is purged so
// the row no longer surfaces in recall queries.
func (s *Store) DeleteFact(ctx context.Context, id int64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin delete tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx,
		`UPDATE facts SET deleted_at = ? WHERE id = ? AND deleted_at IS NULL`,
		time.Now().UTC(), id); err != nil {
		return fmt.Errorf("soft-delete fact: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM vec_facts WHERE fact_id = ?`, id); err != nil {
		return fmt.Errorf("purge fact vec: %w", err)
	}
	return tx.Commit()
}

// RecallFacts returns the top-k closest facts for the session by cosine
// distance. Soft-deleted rows are excluded (their vec_facts entry has already
// been purged, so this is just defense-in-depth via the JOIN filter).
func (s *Store) RecallFacts(ctx context.Context, sessionID, query string, k int) ([]RecalledFact, error) {
	if k <= 0 {
		k = 5
	}
	vecs, err := s.embedder.Embed(ctx, []string{query})
	if err != nil {
		return nil, fmt.Errorf("embed fact query: %w", err)
	}
	if len(vecs) != 1 || len(vecs[0]) != s.dim {
		return nil, fmt.Errorf("embed fact query: bad shape")
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT f.id, f.text, f.kind, vec_distance_cosine(v.embedding, vec_f32(?)) AS distance,
		        f.created_at, f.updated_at
		   FROM vec_facts v
		   JOIN facts f ON f.id = v.fact_id
		  WHERE f.session_id = ? AND f.deleted_at IS NULL
		  ORDER BY distance
		  LIMIT ?`,
		vectorString(vecs[0]), sessionID, k)
	if err != nil {
		return nil, fmt.Errorf("recall facts: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []RecalledFact
	for rows.Next() {
		var r RecalledFact
		if err := rows.Scan(&r.ID, &r.Text, &r.Kind, &r.Distance, &r.CreatedAt, &r.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scan fact row: %w", err)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// ListFacts returns all live facts for a session (most recently updated
// first). Used by /facts inspection commands; not on the hot path.
func (s *Store) ListFacts(ctx context.Context, sessionID string, limit int) ([]Fact, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, session_id, COALESCE(user_id, ''), text, kind,
		        COALESCE(source_msg_id, 0), created_at, updated_at
		   FROM facts
		  WHERE session_id = ? AND deleted_at IS NULL
		  ORDER BY updated_at DESC
		  LIMIT ?`, sessionID, limit)
	if err != nil {
		return nil, fmt.Errorf("list facts: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []Fact
	for rows.Next() {
		var f Fact
		if err := rows.Scan(&f.ID, &f.SessionID, &f.UserID, &f.Text, &f.Kind,
			&f.SourceMsgID, &f.CreatedAt, &f.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scan fact: %w", err)
		}
		out = append(out, f)
	}
	return out, rows.Err()
}
