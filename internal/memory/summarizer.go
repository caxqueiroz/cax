package memory

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/caxqueiroz/cax/internal/memory/memorydb"
)

// Summarizer condenses old messages into a single summary string. priorSummary
// carries the latest existing summary (empty if none) so the new summary can
// fold it in — without this each summarization would discard older context.
type Summarizer interface {
	Summarize(ctx context.Context, priorSummary string, msgs []Message) (string, error)
}

// SummarizeReport describes the work MaybeSummarize did. Zero values when no
// summarization fired. Surfaced to callers so the UI can render a notice.
type SummarizeReport struct {
	ChunkTokens   int   // sum of tokens in the chunk that was summarized
	ChunkMessages int   // number of messages in the chunk
	CoversUpToID  int64 // highest message id now covered by a summary
}

// MaybeSummarize checks whether the session's working window exceeds tokenBudget.
// If so, it summarizes the oldest chunk (messages that fall outside the newest
// budget-fitting window and are not already covered by a prior summary) into a new
// summaries row, FOLDING IN the previous summary so chained summarizations keep
// older context alive. Raw messages are retained; the window is conceptually
// trimmed because LoadWindow injects the latest summary at the top of context.
func (s *Store) MaybeSummarize(ctx context.Context, sessionID string, sum Summarizer, tokenBudget int) (SummarizeReport, error) {
	// Latest existing summary for this session (its covers_up_to bound + text).
	var coveredUpTo int64
	var priorSummary string
	prior, err := s.queries.LatestSummary(ctx, sessionID)
	if errors.Is(err, sql.ErrNoRows) {
		coveredUpTo = 0
		priorSummary = ""
	} else if err != nil {
		return SummarizeReport{}, fmt.Errorf("read prior summary: %w", err)
	} else {
		coveredUpTo = prior.CoversUpToMsgID
		priorSummary = prior.SummaryText
	}

	// Load all uncovered messages, newest-first, to compute the budget split.
	rows, err := s.queries.MessagesAfter(ctx, memorydb.MessagesAfterParams{
		SessionID: sessionID,
		ID:        coveredUpTo,
	})
	if err != nil {
		return SummarizeReport{}, fmt.Errorf("query uncovered messages: %w", err)
	}

	newestFirst := make([]Message, 0, len(rows))
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
		newestFirst = append(newestFirst, m)
		total += m.Tokens
	}

	if total <= tokenBudget {
		return SummarizeReport{}, nil // window fits; nothing to summarize
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
		return SummarizeReport{}, nil // everything fit after all
	}
	// Oldest chunk = entries after the kept newest ones; convert to chronological.
	chunkNewestFirst := newestFirst[kept:]
	chunk := make([]Message, len(chunkNewestFirst))
	chunkTokens := 0
	for i, m := range chunkNewestFirst {
		chunk[len(chunkNewestFirst)-1-i] = m
		chunkTokens += m.Tokens
	}
	if len(chunk) == 0 {
		return SummarizeReport{}, nil
	}

	text, err := sum.Summarize(ctx, priorSummary, chunk)
	if err != nil {
		return SummarizeReport{}, fmt.Errorf("summarize chunk: %w", err)
	}
	coveredUpTo = chunk[len(chunk)-1].ID // highest id in the summarized chunk
	if err := s.queries.InsertSummary(ctx, memorydb.InsertSummaryParams{
		SessionID:       sessionID,
		SummaryText:     text,
		CoversUpToMsgID: coveredUpTo,
		CreatedAt:       time.Now().UTC(),
	}); err != nil {
		return SummarizeReport{}, fmt.Errorf("insert summary: %w", err)
	}
	return SummarizeReport{ChunkTokens: chunkTokens, ChunkMessages: len(chunk), CoversUpToID: coveredUpTo}, nil
}
