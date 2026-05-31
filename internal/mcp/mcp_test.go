package mcp

import (
	"path/filepath"
	"testing"

	"github.com/caxqueiroz/cax/internal/config"
)

// TestConnectEmptyReturnsNoTools is the trivial path — no servers, no work.
func TestConnectEmptyReturnsNoTools(t *testing.T) {
	tools, infos, err := Connect(t.Context(), nil, filepath.Join(t.TempDir(), "tokens.json"))
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	if len(tools) != 0 || len(infos) != 0 {
		t.Errorf("tools=%d infos=%d, want both 0", len(tools), len(infos))
	}
}

// TestConnectStdioFailureRecorded confirms a bogus stdio server is reported
// as a not-connected ServerInfo with a LastError, never aborts other servers.
func TestConnectStdioFailureRecorded(t *testing.T) {
	servers := []config.MCPServerConfig{
		{Name: "bogus", Command: "/nonexistent/binary-xyz", Args: []string{"--help"}},
	}
	tokens := filepath.Join(t.TempDir(), "tokens.json")
	tools, infos, err := Connect(t.Context(), servers, tokens)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	if len(tools) != 0 {
		t.Errorf("tools = %d, want 0 (server should fail to start)", len(tools))
	}
	if len(infos) != 1 {
		t.Fatalf("infos len = %d, want 1", len(infos))
	}
	got := infos[0]
	if got.Name != "bogus" || got.Transport != "stdio" || got.Connected {
		t.Errorf("got %+v, want Name=bogus Transport=stdio Connected=false", got)
	}
	if got.LastError == "" {
		t.Errorf("LastError empty, want non-empty failure reason")
	}
}

// TestConnectUnknownTransport rejects a server with neither Command nor URL.
func TestConnectUnknownTransport(t *testing.T) {
	servers := []config.MCPServerConfig{{Name: "blank"}}
	tokens := filepath.Join(t.TempDir(), "tokens.json")
	_, infos, err := Connect(t.Context(), servers, tokens)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	if len(infos) != 1 || infos[0].Connected || infos[0].LastError == "" {
		t.Fatalf("got %+v, want one failed ServerInfo with LastError set", infos)
	}
}
