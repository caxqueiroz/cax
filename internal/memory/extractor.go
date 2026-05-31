package memory

import (
	"context"
	"fmt"
	"log/slog"
)

// FactOp is one CRUD operation the extractor wants to apply to the facts
// table. Op is "add" | "update" | "delete".
type FactOp struct {
	Op   string `json:"op"`
	ID   int64  `json:"id,omitempty"`
	Text string `json:"text,omitempty"`
}

// FactExtractor pulls operations out of a just-completed turn. Implementations
// typically call an LLM; the Apply step is plain SQL.
type FactExtractor interface {
	Extract(ctx context.Context, userText, assistantText string, existing []Fact) ([]FactOp, error)
}

// ApplyFactOps walks ops in order, dispatching add/update/delete to the
// store. The session_id / source_msg_id come from scope. Per-op errors log
// and continue — fact extraction is best-effort, never blocks a turn.
//
// Returns a summary count (added, updated, deleted, failed) for logging.
func ApplyFactOps(ctx context.Context, st *Store, ops []FactOp, scope Fact) (added, updated, deleted, failed int) {
	for _, op := range ops {
		switch op.Op {
		case "add":
			f := scope
			f.Text = op.Text
			if _, err := st.AddFact(ctx, f); err != nil {
				slog.Warn("facts: add failed", "err", err, "text", op.Text)
				failed++
				continue
			}
			added++
		case "update":
			if op.ID == 0 || op.Text == "" {
				slog.Warn("facts: update missing id or text", "op", op)
				failed++
				continue
			}
			if err := st.UpdateFact(ctx, op.ID, op.Text); err != nil {
				slog.Warn("facts: update failed", "err", err, "id", op.ID)
				failed++
				continue
			}
			updated++
		case "delete":
			if op.ID == 0 {
				slog.Warn("facts: delete missing id", "op", op)
				failed++
				continue
			}
			if err := st.DeleteFact(ctx, op.ID); err != nil {
				slog.Warn("facts: delete failed", "err", err, "id", op.ID)
				failed++
				continue
			}
			deleted++
		default:
			slog.Warn("facts: unknown op", "op", op.Op)
			failed++
		}
	}
	return added, updated, deleted, failed
}

// Validate sanity-checks one op before calling the store. Returns nil if the
// op is well-formed.
func (op FactOp) Validate() error {
	switch op.Op {
	case "add":
		if op.Text == "" {
			return fmt.Errorf("add: empty text")
		}
	case "update":
		if op.ID == 0 || op.Text == "" {
			return fmt.Errorf("update: id and text required")
		}
	case "delete":
		if op.ID == 0 {
			return fmt.Errorf("delete: id required")
		}
	default:
		return fmt.Errorf("unknown op %q", op.Op)
	}
	return nil
}
