// Package mcp connects configured MCP servers (stdio or HTTP) using dive's
// experimental/mcp module and exposes their tools as dive.Tool. OAuth tokens
// are persisted to a file-backed FileOAuthTokenStore so they survive across
// czcli runs.
package mcp

import (
	"context"
	"fmt"
	"log/slog"
	"sort"

	"github.com/caxqueiroz/czcli/internal/config"
	"github.com/deepnoodle-ai/dive"
	divemcp "github.com/deepnoodle-ai/dive/experimental/mcp"
)

// ServerInfo describes the runtime status of a configured MCP server. The
// CLI's /mcp slash command and channel.Status read this struct.
type ServerInfo struct {
	Name      string
	Transport string // "stdio" | "http"
	Connected bool
	ToolCount int
	LastError string
}

// Connect initializes every configured MCP server best-effort. Stdio servers
// require Command; HTTP servers require URL. Per-server errors are logged
// and surfaced via ServerInfo.LastError; the function only returns an error
// for configuration-layer failures (e.g. inability to ensure the token-store
// directory).
//
// tokenStorePath is the on-disk path used for any server that opts into
// OAuth (currently keyed via OAuth on the dive ServerConfig). Today no
// czcli MCPServerConfig fields surface OAuth, so the path is reserved for
// when plugins or per-server config expose it (Plan 7+).
func Connect(ctx context.Context, servers []config.MCPServerConfig, tokenStorePath string) ([]dive.Tool, []ServerInfo, error) {
	if len(servers) == 0 {
		return nil, nil, nil
	}

	// Eagerly construct the file token store so the directory exists before
	// any server starts an OAuth flow. We do not return its error: a missing
	// tokens dir is best-effort; servers that don't use OAuth continue.
	if _, terr := divemcp.NewFileOAuthTokenStore(tokenStorePath); terr != nil {
		slog.Warn("mcp: failed to prepare token store; OAuth servers will fall back to memory", "path", tokenStorePath, "err", terr)
	}

	manager := divemcp.NewManager()

	infos := make([]ServerInfo, 0, len(servers))
	configs := make(map[string]*divemcp.ServerConfig, len(servers))

	// Stage 1: translate czcli config → dive ServerConfig, dropping invalid
	// entries with a failed ServerInfo so callers see why each was skipped.
	for _, s := range servers {
		transport, dcfg, err := toServerConfig(s)
		info := ServerInfo{Name: s.Name, Transport: transport}
		if err != nil {
			info.LastError = err.Error()
			slog.Warn("mcp: invalid server config", "name", s.Name, "err", err)
			infos = append(infos, info)
			continue
		}
		configs[s.Name] = dcfg
		infos = append(infos, info)
	}

	// Stage 2: initialize one-by-one (Manager.InitializeServers aborts on
	// first error in v1.7; we want best-effort, so call per-server).
	for i := range infos {
		info := infos[i]
		if info.LastError != "" {
			continue
		}
		cfg := configs[info.Name]
		if cfg == nil {
			continue
		}
		if err := manager.InitializeServers(ctx, []*divemcp.ServerConfig{cfg}); err != nil {
			info.Connected = false
			info.LastError = err.Error()
			slog.Warn("mcp: server init failed", "name", info.Name, "err", err)
			infos[i] = info
			continue
		}
		serverTools := manager.GetToolsByServer(info.Name)
		info.Connected = manager.IsServerConnected(info.Name)
		info.ToolCount = len(serverTools)
		infos[i] = info
	}

	// Stage 3: collect adapted tools, sorted for determinism.
	toolMap := manager.GetAllTools()
	toolNames := make([]string, 0, len(toolMap))
	for n := range toolMap {
		toolNames = append(toolNames, n)
	}
	sort.Strings(toolNames)
	tools := make([]dive.Tool, 0, len(toolNames))
	for _, n := range toolNames {
		tools = append(tools, toolMap[n])
	}
	return tools, infos, nil
}

// toServerConfig maps czcli's MCPServerConfig into dive's ServerConfig and
// selects the transport. Stdio wins when both Command and URL are set; that
// matches the dive client's own dispatch.
func toServerConfig(s config.MCPServerConfig) (string, *divemcp.ServerConfig, error) {
	switch {
	case s.Command != "":
		return "stdio", &divemcp.ServerConfig{
			Name:    s.Name,
			Type:    "stdio",
			Command: s.Command,
			Args:    s.Args,
			Env:     s.Env,
		}, nil
	case s.URL != "":
		return "http", &divemcp.ServerConfig{
			Name:    s.Name,
			Type:    "http",
			URL:     s.URL,
			Headers: s.Headers,
		}, nil
	default:
		return "", nil, fmt.Errorf("mcp server %q: neither command nor url set", s.Name)
	}
}
