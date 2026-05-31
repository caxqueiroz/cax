package tools

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/caxqueiroz/cax/internal/bgproc"
	"github.com/deepnoodle-ai/dive"
)

// bashBgInput is the bash_bg tool input.
type bashBgInput struct {
	Command    string `json:"command" description:"The shell command to run. Executed via bash -c."`
	WorkingDir string `json:"working_dir,omitempty" description:"Optional cwd. Must be inside the configured workspace."`
}

// BashBgTool returns the bash_bg tool. The command is started in the
// background, the model gets back a task_id immediately, and can poll it with
// bash_status. Completion is also injected as a system notice on the next turn
// so the model doesn't have to poll a watch loop.
func BashBgTool(reg *bgproc.Registry) dive.Tool {
	return dive.FuncTool(
		"bash_bg",
		"Run a shell command in the BACKGROUND. Returns a task_id immediately — use bash_status(task_id) to poll, or wait for the auto-injected completion notice on the next turn. Use this for: long builds, test suites, watchers, anything that would otherwise block the turn. For quick commands, use Bash instead.",
		func(ctx context.Context, in *bashBgInput) (*dive.ToolResult, error) {
			if reg == nil {
				return dive.NewToolResultError("bash_bg: background registry not configured"), nil
			}
			cmd := strings.TrimSpace(in.Command)
			if cmd == "" {
				return dive.NewToolResultError("bash_bg: 'command' is required"), nil
			}
			id, err := reg.Start(cmd, strings.TrimSpace(in.WorkingDir))
			if err != nil {
				return dive.NewToolResultError(fmt.Sprintf("bash_bg: %s", err.Error())), nil
			}
			return dive.NewToolResultText(fmt.Sprintf("Started %s. Poll with bash_status(task_id=%q).", id, id)), nil
		},
	)
}

// bashStatusInput is the bash_status tool input.
type bashStatusInput struct {
	TaskID string `json:"task_id" description:"The id returned by bash_bg."`
	Tail   int    `json:"tail,omitempty" description:"Max bytes of stdout/stderr to include (default 4096)."`
}

// BashStatusTool returns the bash_status tool. Reports lifecycle + a tail
// of stdout/stderr. Safe to call repeatedly.
func BashStatusTool(reg *bgproc.Registry) dive.Tool {
	return dive.FuncTool(
		"bash_status",
		"Poll a background task started with bash_bg. Returns status (running/completed/failed), exit code if finished, and a tail of stdout/stderr.",
		func(ctx context.Context, in *bashStatusInput) (*dive.ToolResult, error) {
			if reg == nil {
				return dive.NewToolResultError("bash_status: background registry not configured"), nil
			}
			id := strings.TrimSpace(in.TaskID)
			if id == "" {
				return dive.NewToolResultError("bash_status: 'task_id' is required"), nil
			}
			tail := in.Tail
			if tail <= 0 {
				tail = 4096
			}
			p, ok := reg.Status(id)
			if !ok {
				return dive.NewToolResultError(fmt.Sprintf("bash_status: unknown task id %q", id)), nil
			}
			var sb strings.Builder
			fmt.Fprintf(&sb, "task %s\n", p.ID)
			fmt.Fprintf(&sb, "command: %s\n", p.Command)
			if p.WorkingDir != "" {
				fmt.Fprintf(&sb, "cwd: %s\n", p.WorkingDir)
			}
			fmt.Fprintf(&sb, "status: %s\n", p.Status)
			if p.Status != bgproc.StatusRunning {
				fmt.Fprintf(&sb, "exit_code: %d\n", p.ExitCode)
				fmt.Fprintf(&sb, "duration: %s\n", p.Finished.Sub(p.Started).Round(time.Millisecond))
			} else {
				fmt.Fprintf(&sb, "running for: %s\n", time.Since(p.Started).Round(time.Millisecond))
			}
			if p.Err != nil {
				fmt.Fprintf(&sb, "error: %s\n", p.Err.Error())
			}
			if len(p.Stdout) > 0 {
				fmt.Fprintf(&sb, "stdout (last %d bytes):\n%s\n", min(tail, len(p.Stdout)), tailBytes(p.Stdout, tail))
			}
			if len(p.Stderr) > 0 {
				fmt.Fprintf(&sb, "stderr (last %d bytes):\n%s\n", min(tail, len(p.Stderr)), tailBytes(p.Stderr, tail))
			}
			return dive.NewToolResultText(sb.String()), nil
		},
	)
}

func tailBytes(b []byte, n int) string {
	if len(b) <= n {
		return string(b)
	}
	return string(b[len(b)-n:])
}
