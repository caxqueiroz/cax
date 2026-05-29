package tools

import (
	"context"
	"fmt"
	"strings"

	"github.com/caxqueiroz/czcli/internal/memory"
	"github.com/deepnoodle-ai/dive"
)

// recallInput is the search_memory tool input.
type recallInput struct {
	Query     string `json:"query" description:"What to search long-term memory for."`
	K         int    `json:"k,omitempty" description:"Max number of memories to return (default 5)."`
	SessionID string `json:"session_id,omitempty" description:"Optional session to scope the search to."`
}

const defaultRecallK = 5

// RecallTool is the search_memory FuncTool: it embeds the query, runs a KNN
// search over stored memories, and returns formatted snippets. Recall failures
// are reported as an error ToolResult (not a Go error) so the model can adapt.
func RecallTool(store *memory.Store) dive.Tool {
	return dive.FuncTool(
		"search_memory",
		"Search long-term memory for relevant past information. Use this for explicit deep recall beyond the context automatically provided each turn.",
		func(ctx context.Context, in *recallInput) (*dive.ToolResult, error) {
			query := strings.TrimSpace(in.Query)
			if query == "" {
				return dive.NewToolResultText("No query provided; nothing to search."), nil
			}
			k := in.K
			if k <= 0 {
				k = defaultRecallK
			}
			results, err := store.Recall(ctx, in.SessionID, query, k)
			if err != nil {
				return dive.NewToolResultError(fmt.Sprintf("memory recall failed: %s", err.Error())), nil
			}
			if len(results) == 0 {
				return dive.NewToolResultText("No relevant memories found."), nil
			}
			var sb strings.Builder
			sb.WriteString(fmt.Sprintf("Found %d relevant memories:\n", len(results)))
			for i, r := range results {
				sb.WriteString(fmt.Sprintf("%d. (distance %.4f) %s\n", i+1, r.Distance, strings.TrimSpace(r.Text)))
			}
			return dive.NewToolResultText(sb.String()), nil
		},
	)
}
