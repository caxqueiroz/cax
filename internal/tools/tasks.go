package tools

import (
	"context"
	"fmt"
	"strings"

	"github.com/caxqueiroz/cax/internal/tasks"
	"github.com/deepnoodle-ai/dive"
)

// taskItem is one entry in the tasks_set input.
type taskItem struct {
	Title  string `json:"title" description:"What the task is, short and actionable."`
	Status string `json:"status" description:"One of: pending, in_progress, completed, failed."`
}

// tasksSetInput replaces the entire task list (TodoWrite semantics — simpler
// than separate create/update calls; the model just re-submits the full list
// every time anything changes).
type tasksSetInput struct {
	Tasks []taskItem `json:"tasks" description:"The complete current task list. Replaces whatever was there before."`
}

// TasksSetTool returns the tasks_set FuncTool wired to b. The model calls this
// whenever the to-do list changes; the CLI renders the live result in a
// sticky panel above the input.
func TasksSetTool(b *tasks.Board) dive.Tool {
	return dive.FuncTool(
		"tasks_set",
		"Maintain a visible to-do list for the current task. Call this to publish the FULL current task list — the previous list is replaced wholesale. Use exactly one in_progress task at a time. Valid statuses: pending, in_progress, completed, failed.",
		func(ctx context.Context, in *tasksSetInput) (*dive.ToolResult, error) {
			if b == nil {
				return dive.NewToolResultError("tasks_set: task board not configured"), nil
			}
			normalized := make([]tasks.Task, 0, len(in.Tasks))
			for i, t := range in.Tasks {
				title := strings.TrimSpace(t.Title)
				if title == "" {
					return dive.NewToolResultError(fmt.Sprintf("tasks_set: task #%d has empty title", i+1)), nil
				}
				st := tasks.Status(strings.TrimSpace(t.Status))
				switch st {
				case tasks.StatusPending, tasks.StatusInProgress, tasks.StatusCompleted, tasks.StatusFailed:
				case "":
					st = tasks.StatusPending
				default:
					return dive.NewToolResultError(fmt.Sprintf("tasks_set: task #%d has invalid status %q (allowed: pending, in_progress, completed, failed)", i+1, t.Status)), nil
				}
				normalized = append(normalized, tasks.Task{Title: title, Status: st})
			}
			b.Set(normalized)
			total, done, _ := b.Count()
			return dive.NewToolResultText(fmt.Sprintf("Tasks updated: %d total, %d completed.", total, done)), nil
		},
	)
}
