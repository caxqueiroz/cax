package lsp

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/caxqueiroz/cax/internal/config"
	"go.lsp.dev/jsonrpc2"
	"go.lsp.dev/protocol"
)

func TestLifecycleDidOpenAndClose(t *testing.T) {
	opens := make(chan protocol.DidOpenTextDocumentParams, 4)
	closes := make(chan protocol.DidCloseTextDocumentParams, 4)
	handler := func(ctx context.Context, reply jsonrpc2.Replier, req jsonrpc2.Request) error {
		switch req.Method() {
		case protocol.MethodInitialize:
			return reply(ctx, &protocol.InitializeResult{}, nil)
		case protocol.MethodInitialized:
			return reply(ctx, nil, nil)
		case protocol.MethodTextDocumentDidOpen:
			var p protocol.DidOpenTextDocumentParams
			_ = json.Unmarshal(req.Params(), &p)
			opens <- p
			return reply(ctx, nil, nil)
		case protocol.MethodTextDocumentDidClose:
			var p protocol.DidCloseTextDocumentParams
			_ = json.Unmarshal(req.Params(), &p)
			closes <- p
			return reply(ctx, nil, nil)
		}
		return reply(ctx, nil, nil)
	}
	fs := newFakeServer(t, handler)

	dir := t.TempDir()
	file := filepath.Join(dir, "main.go")
	if err := os.WriteFile(file, []byte("package main\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	m := &Manager{
		rootDir: dir,
		servers: map[string]*server{},
		dialFn: func(_ context.Context, _ config.LSPServerConfig) (jsonrpc2.Conn, func() error, error) {
			return fs.clientConn, func() error { return nil }, nil
		},
	}
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	if _, err := m.bringUp(ctx, []config.LSPServerConfig{{
		Name: "fakegopls", Command: "/x", Languages: []string{"go"},
	}}); err != nil {
		t.Fatal(err)
	}

	s := m.servers["go"]
	if s == nil {
		t.Fatal("server for go not registered")
	}
	if err := m.ensureOpen(ctx, s, file); err != nil {
		t.Fatalf("ensureOpen: %v", err)
	}
	if err := m.ensureOpen(ctx, s, file); err != nil { // idempotent
		t.Fatalf("ensureOpen (2nd): %v", err)
	}
	select {
	case p := <-opens:
		if string(p.TextDocument.LanguageID) != "go" {
			t.Errorf("LanguageID = %q, want go", p.TextDocument.LanguageID)
		}
		if p.TextDocument.Text != "package main\n" {
			t.Errorf("Text = %q", p.TextDocument.Text)
		}
	case <-ctx.Done():
		t.Fatal("didOpen not received")
	}
	select {
	case extra := <-opens:
		t.Fatalf("unexpected 2nd didOpen: %+v", extra)
	case <-time.After(50 * time.Millisecond):
		// good: ensureOpen was idempotent
	}

	if err := m.closeFile(ctx, s, file); err != nil {
		t.Fatalf("closeFile: %v", err)
	}
	select {
	case <-closes:
	case <-ctx.Done():
		t.Fatal("didClose not received")
	}
}
