package tools

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/caxqueiroz/cax/internal/bgproc"
	"github.com/caxqueiroz/cax/internal/config"
	"github.com/caxqueiroz/cax/internal/memory"
	"github.com/caxqueiroz/cax/internal/tasks"
	"github.com/deepnoodle-ai/dive"
	"github.com/deepnoodle-ai/dive/toolkit"
)

// Registry assembles the dive built-in tools cax exposes per config, plus the
// search_memory recall tool. Bash/Write/Edit are returned as-is; gating happens
// in the agent's PreToolUse hook via a dive.Dialog (see permission.go).
//
// All file/shell tools share one *toolkit.PathValidator built from
// cfg.WorkspaceDirs. The first entry is the writable workspace root; the rest
// are added as read-only allowed paths (dive's PathValidator only supports one
// writable root). Default when unset: [$HOME].
//
// WebSearch is intentionally omitted: it requires a web.Searcher that
// config.ToolsConfig does not provide yet. WebFetch (self-sufficient) covers
// the web case for the MVP.
func Registry(cfg config.ToolsConfig, store *memory.Store, board *tasks.Board, bgReg *bgproc.Registry) ([]dive.Tool, error) {
	tools := []dive.Tool{RecallTool(store)}
	if board != nil {
		tools = append(tools, TasksSetTool(board))
	}

	validator, err := buildPathValidator(cfg.WorkspaceDirs)
	if err != nil {
		return nil, fmt.Errorf("build path validator: %w", err)
	}

	if cfg.FilesEnabled {
		tools = append(tools,
			toolkit.NewReadFileTool(toolkit.ReadFileToolOptions{Validator: validator}),
			toolkit.NewWriteFileTool(toolkit.WriteFileToolOptions{Validator: validator}),
			toolkit.NewEditTool(toolkit.EditToolOptions{Validator: validator}),
			toolkit.NewGlobTool(toolkit.GlobToolOptions{Validator: validator}),
			toolkit.NewGrepTool(toolkit.GrepToolOptions{Validator: validator}),
		)
	}
	if cfg.WebEnabled {
		tools = append(tools, toolkit.NewFetchTool())
	}
	if cfg.BashEnabled {
		tools = append(tools, toolkit.NewBashTool(toolkit.BashToolOptions{Validator: validator}))
		if bgReg != nil {
			tools = append(tools, BashBgTool(bgReg), BashStatusTool(bgReg))
		}
	}
	return tools, nil
}

// BuildPathValidator is the exported alias for buildPathValidator so main.go
// can construct a shared validator (used by bgproc.Registry) before tools
// are assembled. Safe to call multiple times — the result is independent.
func BuildPathValidator(roots []string) (*toolkit.PathValidator, error) {
	return buildPathValidator(roots)
}

// buildPathValidator constructs the shared PathValidator from cfg roots.
// Empty roots default to [$HOME]; entries are tilde/env-expanded. The first
// entry is the writable workspace, additional entries become read-allowed.
func buildPathValidator(rawRoots []string) (*toolkit.PathValidator, error) {
	roots := expandRoots(rawRoots)
	if len(roots) == 0 {
		home, _ := os.UserHomeDir()
		if home == "" {
			home, _ = os.Getwd()
		}
		roots = []string{home}
	}
	v, err := toolkit.NewPathValidator(roots[0])
	if err != nil {
		return nil, fmt.Errorf("primary workspace %q: %w", roots[0], err)
	}
	for _, extra := range roots[1:] {
		if err := v.AllowReadPath(extra); err != nil {
			slog.Warn("tools: allow read path failed", "path", extra, "err", err)
		}
	}
	slog.Info("tools: workspace configured",
		"writable", v.WorkspaceDir,
		"read_extra", roots[1:],
	)
	return v, nil
}

func expandRoots(roots []string) []string {
	out := make([]string, 0, len(roots))
	home, _ := os.UserHomeDir()
	for _, r := range roots {
		r = strings.TrimSpace(os.ExpandEnv(r))
		if r == "" {
			continue
		}
		if strings.HasPrefix(r, "~/") && home != "" {
			r = filepath.Join(home, r[2:])
		} else if r == "~" && home != "" {
			r = home
		}
		out = append(out, r)
	}
	return out
}
