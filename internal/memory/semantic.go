package memory

import (
	"context"
	"fmt"
	"time"
)

// Recalled is a semantic-recall hit.
type Recalled struct {
	Text      string
	Distance  float64
	CreatedAt time.Time
}

// AddMemory embeds text and stores it in memories + vec_memories. sourceMsgID may be 0.
func (s *Store) AddMemory(ctx context.Context, sessionID, text string, sourceMsgID int64) error {
	vecs, err := s.embedder.Embed(ctx, []string{text})
	if err != nil {
		return fmt.Errorf("embed memory: %w", err)
	}
	if len(vecs) != 1 || len(vecs[0]) != s.dim {
		return fmt.Errorf("embed memory: bad vector shape len=%d dim=%d", len(vecs), s.dim)
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin memory tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	var srcArg any
	if sourceMsgID != 0 {
		srcArg = sourceMsgID
	}
	res, err := tx.ExecContext(ctx,
		`INSERT INTO memories(session_id, text, source_msg_id, created_at) VALUES (?, ?, ?, ?)`,
		sessionID, text, srcArg, time.Now().UTC())
	if err != nil {
		return fmt.Errorf("insert memory: %w", err)
	}
	memID, err := res.LastInsertId()
	if err != nil {
		return fmt.Errorf("memory last insert id: %w", err)
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO vec_memories(memory_id, embedding) VALUES (?, vec_f32(?))`,
		memID, vectorString(vecs[0])); err != nil {
		return fmt.Errorf("insert vector: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit memory tx: %w", err)
	}
	return nil
}

// Recall embeds query and returns the top-k closest memories for the session,
// ordered by ascending cosine distance.
func (s *Store) Recall(ctx context.Context, sessionID, query string, k int) ([]Recalled, error) {
	if k <= 0 {
		k = 5
	}
	vecs, err := s.embedder.Embed(ctx, []string{query})
	if err != nil {
		return nil, fmt.Errorf("embed query: %w", err)
	}
	if len(vecs) != 1 || len(vecs[0]) != s.dim {
		return nil, fmt.Errorf("embed query: bad vector shape len=%d dim=%d", len(vecs), s.dim)
	}

	rows, err := s.db.QueryContext(ctx,
		`SELECT m.text, vec_distance_cosine(v.embedding, vec_f32(?)) AS distance, m.created_at
		   FROM vec_memories v
		   JOIN memories m ON m.id = v.memory_id
		  WHERE m.session_id = ?
		  ORDER BY distance
		  LIMIT ?`,
		vectorString(vecs[0]), sessionID, k)
	if err != nil {
		return nil, fmt.Errorf("recall query: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []Recalled
	for rows.Next() {
		var r Recalled
		if err := rows.Scan(&r.Text, &r.Distance, &r.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan recall row: %w", err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("recall rows: %w", err)
	}
	return out, nil
}
