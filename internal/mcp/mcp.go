// Package mcp connects configured MCP servers and exposes their tools to the
// agent.
//
// The plan targeted github.com/deepnoodle-ai/dive/experimental/mcp, but the
// installed dive v1.5.0 does not ship an experimental/mcp package (its
// experimental tree contains compaction, sandbox, settings, subagent, todo, and
// toolkit only). Rather than pin an unavailable/unstable module and block the
// build, Connect is a thin best-effort no-op: it logs the configured servers
// and returns no tools. When dive exposes a stable MCP manager, wire it here —
// the Connect signature and call sites (agent.augmentTools) stay the same.
package mcp

import (
	"context"
	"log/slog"

	"github.com/caxqueiroz/czcli/internal/config"
	"github.com/deepnoodle-ai/dive"
)

// Connect initializes the configured MCP servers and returns their tools. It is
// best-effort: errors from individual servers are logged and the remaining
// tools are returned. Returns (nil, nil) when no servers are configured.
//
// TODO(mcp): wire a real MCP manager once dive ships a stable one; today this
// returns no tools regardless of configuration.
func Connect(_ context.Context, cfg config.MCPConfig) ([]dive.Tool, error) {
	if len(cfg.Servers) == 0 {
		return nil, nil
	}
	for _, s := range cfg.Servers {
		slog.Warn("mcp: server configured but MCP support is not yet wired; skipping",
			"name", s.Name, "command", s.Command, "url", s.URL)
	}
	return nil, nil
}
