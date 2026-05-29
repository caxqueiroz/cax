package mcp

import (
	"context"
	"testing"

	"github.com/caxqueiroz/czcli/internal/config"
)

func TestConnect_NoServersReturnsNil(t *testing.T) {
	got, err := Connect(context.Background(), config.MCPConfig{})
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("expected no tools, got %d", len(got))
	}
}

func TestConnect_BadServerDegradesGracefully(t *testing.T) {
	// A configured server must not fail Connect; the best-effort connector
	// returns whatever connected (here, nothing) and logs.
	got, err := Connect(context.Background(), config.MCPConfig{
		Servers: []config.MCPServerConfig{
			{Name: "broken", Command: "/nonexistent/binary-xyz"},
		},
	})
	if err != nil {
		t.Fatalf("connect should be best-effort, got err: %v", err)
	}
	_ = got // may be empty; the point is no error
}
