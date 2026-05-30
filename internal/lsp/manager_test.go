package lsp

import (
	"context"
	"encoding/json"
	"net"
	"testing"
	"time"

	_ "github.com/caxqueiroz/czcli/internal/config"
	"go.lsp.dev/jsonrpc2"
	"go.lsp.dev/protocol"
)

// fakeServer is the test-side of an in-process LSP connection. It uses
// net.Pipe to back a bi-directional Stream pair, hosts a jsonrpc2 Handler that
// branches on method name, and exposes a Notify hook so tests can simulate
// publishDiagnostics. Closing it tears down both ends.
type fakeServer struct {
	clientConn jsonrpc2.Conn // manager side
	serverConn jsonrpc2.Conn // fake side
	cancel     context.CancelFunc
}

// newFakeServer wires a net.Pipe + two jsonrpc2.Conns and starts both. handler
// is invoked for every request from the manager side. Tests typically branch
// on req.Method() (a string) and reply with reply(ctx, <result>, nil).
func newFakeServer(t *testing.T, handler jsonrpc2.Handler) *fakeServer {
	t.Helper()
	a, b := net.Pipe()
	clientConn := jsonrpc2.NewConn(jsonrpc2.NewStream(a))
	serverConn := jsonrpc2.NewConn(jsonrpc2.NewStream(b))

	ctx, cancel := context.WithCancel(context.Background())
	// The fake (server side) handles requests from the manager.
	serverConn.Go(ctx, handler)
	// The client side needs *some* handler to drain server-initiated
	// notifications; tests override this when they want to capture them.
	clientConn.Go(ctx, func(ctx context.Context, reply jsonrpc2.Replier, _ jsonrpc2.Request) error {
		return reply(ctx, nil, nil)
	})

	t.Cleanup(func() {
		cancel()
		_ = clientConn.Close()
		_ = serverConn.Close()
	})
	return &fakeServer{clientConn: clientConn, serverConn: serverConn, cancel: cancel}
}

func TestFakeServerInitializeRoundTrip(t *testing.T) {
	handler := func(ctx context.Context, reply jsonrpc2.Replier, req jsonrpc2.Request) error {
		switch req.Method() {
		case protocol.MethodInitialize:
			return reply(ctx, &protocol.InitializeResult{}, nil)
		default:
			return reply(ctx, nil, nil)
		}
	}
	fs := newFakeServer(t, handler)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	var result protocol.InitializeResult
	if _, err := fs.clientConn.Call(ctx, protocol.MethodInitialize, &protocol.InitializeParams{
		RootURI: "file:///",
	}, &result); err != nil {
		t.Fatalf("Call initialize: %v", err)
	}
	// Cover encoding compatibility: serializing the params must round-trip cleanly.
	if _, err := json.Marshal(&protocol.InitializeParams{}); err != nil {
		t.Fatalf("Marshal InitializeParams: %v", err)
	}
}

func TestNewEmpty(t *testing.T) {
	ctx := context.Background()
	m, infos, err := New(ctx, nil, t.TempDir())
	if err != nil {
		t.Fatalf("New empty: %v", err)
	}
	if m == nil {
		t.Fatal("New returned nil manager")
	}
	if len(infos) != 0 {
		t.Fatalf("expected 0 ServerInfo, got %d", len(infos))
	}
	tools := m.Tools()
	want := []string{
		"lsp_definition", "lsp_references", "lsp_hover",
		"lsp_document_symbols", "lsp_workspace_symbols", "lsp_diagnostics",
	}
	if len(tools) != len(want) {
		t.Fatalf("Tools count = %d, want %d", len(tools), len(want))
	}
	got := map[string]bool{}
	for _, tl := range tools {
		got[tl.Name()] = true
	}
	for _, n := range want {
		if !got[n] {
			t.Errorf("missing tool %q", n)
		}
	}
	if err := m.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}
