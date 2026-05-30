package tools

import (
	"github.com/caxqueiroz/cax/internal/config"
	"github.com/caxqueiroz/cax/internal/memory"
	"github.com/deepnoodle-ai/dive"
	"github.com/deepnoodle-ai/dive/toolkit"
)

// Registry assembles the dive built-in tools cax exposes per config, plus the
// search_memory recall tool. Bash/Write/Edit are returned as-is; gating happens
// in the agent's PreToolUse hook via a dive.Dialog (see permission.go).
//
// WebSearch is intentionally omitted: it requires a web.Searcher that
// config.ToolsConfig does not provide yet. WebFetch (self-sufficient) covers
// the web case for the MVP.
func Registry(cfg config.ToolsConfig, store *memory.Store) ([]dive.Tool, error) {
	tools := []dive.Tool{RecallTool(store)}

	if cfg.FilesEnabled {
		tools = append(tools,
			toolkit.NewReadFileTool(),
			toolkit.NewWriteFileTool(),
			toolkit.NewEditTool(),
			toolkit.NewGlobTool(),
			toolkit.NewGrepTool(),
		)
	}
	if cfg.WebEnabled {
		tools = append(tools, toolkit.NewFetchTool())
	}
	if cfg.BashEnabled {
		tools = append(tools, toolkit.NewBashTool())
	}
	return tools, nil
}
