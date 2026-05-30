package lsp

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/caxqueiroz/czcli/internal/config"
	"github.com/deepnoodle-ai/dive"
	"go.lsp.dev/jsonrpc2"
	"go.lsp.dev/protocol"
	"go.lsp.dev/uri"
)

func setupManagerWithFake(t *testing.T, handler jsonrpc2.Handler) (*Manager, string) {
	t.Helper()
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
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if _, err := m.bringUp(ctx, []config.LSPServerConfig{{
		Name: "fakegopls", Command: "/x", Languages: []string{"go"},
	}}); err != nil {
		t.Fatal(err)
	}
	return m, file
}

func toolByName(tools []dive.Tool, name string) dive.Tool {
	for _, tl := range tools {
		if tl.Name() == name {
			return tl
		}
	}
	return nil
}

// callTool unmarshals a JSON-encoded args map into the tool's typed input by
// going through Tool.Call(ctx, input any); dive's TypedToolAdapter handles
// the JSON decode.
func callTool(t *testing.T, tool dive.Tool, args map[string]any) *dive.ToolResult {
	t.Helper()
	if tool == nil {
		t.Fatal("nil tool")
	}
	raw, err := json.Marshal(args)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	res, err := tool.Call(context.Background(), json.RawMessage(raw))
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	return res
}

func resultText(r *dive.ToolResult) string {
	if r == nil {
		return ""
	}
	var sb strings.Builder
	for _, c := range r.Content {
		sb.WriteString(c.Text)
	}
	return sb.String()
}

func TestLSPReferences(t *testing.T) {
	handler := func(ctx context.Context, reply jsonrpc2.Replier, req jsonrpc2.Request) error {
		switch req.Method() {
		case protocol.MethodInitialize:
			return reply(ctx, &protocol.InitializeResult{}, nil)
		case protocol.MethodInitialized, protocol.MethodTextDocumentDidOpen:
			return reply(ctx, nil, nil)
		case protocol.MethodTextDocumentReferences:
			var p protocol.ReferenceParams
			_ = json.Unmarshal(req.Params(), &p)
			if !p.Context.IncludeDeclaration {
				t.Errorf("expected IncludeDeclaration=true")
			}
			locs := []protocol.Location{
				{URI: uri.File("/tmp/a.go"), Range: protocol.Range{Start: protocol.Position{Line: 1}, End: protocol.Position{Line: 1, Character: 3}}},
				{URI: uri.File("/tmp/b.go"), Range: protocol.Range{Start: protocol.Position{Line: 5}, End: protocol.Position{Line: 5, Character: 7}}},
			}
			return reply(ctx, locs, nil)
		}
		return reply(ctx, nil, nil)
	}
	m, file := setupManagerWithFake(t, handler)
	tool := toolByName(m.Tools(), "lsp_references")
	res := callTool(t, tool, map[string]any{"file": file, "line": 0, "character": 0})
	text := resultText(res)
	if !strings.Contains(text, "/tmp/a.go") || !strings.Contains(text, "/tmp/b.go") {
		t.Fatalf("references text missing locations: %q", text)
	}
}

func TestLSPHover(t *testing.T) {
	handler := func(ctx context.Context, reply jsonrpc2.Replier, req jsonrpc2.Request) error {
		switch req.Method() {
		case protocol.MethodInitialize:
			return reply(ctx, &protocol.InitializeResult{}, nil)
		case protocol.MethodInitialized, protocol.MethodTextDocumentDidOpen:
			return reply(ctx, nil, nil)
		case protocol.MethodTextDocumentHover:
			h := protocol.Hover{Contents: protocol.MarkupContent{
				Kind:  protocol.Markdown,
				Value: "func main()\n\nentry point",
			}}
			return reply(ctx, h, nil)
		}
		return reply(ctx, nil, nil)
	}
	m, file := setupManagerWithFake(t, handler)
	tool := toolByName(m.Tools(), "lsp_hover")
	res := callTool(t, tool, map[string]any{"file": file, "line": 0, "character": 0})
	text := resultText(res)
	if !strings.Contains(text, "entry point") {
		t.Fatalf("hover text missing body: %q", text)
	}
}

func TestLSPDocumentSymbols(t *testing.T) {
	handler := func(ctx context.Context, reply jsonrpc2.Replier, req jsonrpc2.Request) error {
		switch req.Method() {
		case protocol.MethodInitialize:
			return reply(ctx, &protocol.InitializeResult{}, nil)
		case protocol.MethodInitialized, protocol.MethodTextDocumentDidOpen:
			return reply(ctx, nil, nil)
		case protocol.MethodTextDocumentDocumentSymbol:
			syms := []protocol.DocumentSymbol{
				{Name: "main", Kind: protocol.SymbolKindFunction},
				{Name: "Config", Kind: protocol.SymbolKindStruct},
			}
			return reply(ctx, syms, nil)
		}
		return reply(ctx, nil, nil)
	}
	m, file := setupManagerWithFake(t, handler)
	tool := toolByName(m.Tools(), "lsp_document_symbols")
	res := callTool(t, tool, map[string]any{"file": file})
	text := resultText(res)
	if !strings.Contains(text, "main") || !strings.Contains(text, "Config") {
		t.Fatalf("document_symbols missing names: %q", text)
	}
}

func TestLSPWorkspaceSymbols(t *testing.T) {
	handler := func(ctx context.Context, reply jsonrpc2.Replier, req jsonrpc2.Request) error {
		switch req.Method() {
		case protocol.MethodInitialize:
			return reply(ctx, &protocol.InitializeResult{}, nil)
		case protocol.MethodInitialized:
			return reply(ctx, nil, nil)
		case protocol.MethodWorkspaceSymbol:
			var p protocol.WorkspaceSymbolParams
			_ = json.Unmarshal(req.Params(), &p)
			if p.Query != "Foo" {
				t.Errorf("query = %q want Foo", p.Query)
			}
			syms := []protocol.SymbolInformation{{
				Name: "FooBar",
				Kind: protocol.SymbolKindFunction,
				Location: protocol.Location{
					URI: uri.File("/tmp/foo.go"),
				},
			}}
			return reply(ctx, syms, nil)
		}
		return reply(ctx, nil, nil)
	}
	m, _ := setupManagerWithFake(t, handler)
	tool := toolByName(m.Tools(), "lsp_workspace_symbols")
	res := callTool(t, tool, map[string]any{"query": "Foo"})
	text := resultText(res)
	if !strings.Contains(text, "FooBar") || !strings.Contains(text, "/tmp/foo.go") {
		t.Fatalf("workspace_symbols missing fields: %q", text)
	}
}

func TestLSPDiagnosticsCachedFromNotification(t *testing.T) {
	handler := func(ctx context.Context, reply jsonrpc2.Replier, req jsonrpc2.Request) error {
		switch req.Method() {
		case protocol.MethodInitialize:
			return reply(ctx, &protocol.InitializeResult{}, nil)
		case protocol.MethodInitialized, protocol.MethodTextDocumentDidOpen:
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
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if _, err := m.bringUp(ctx, []config.LSPServerConfig{{
		Name: "fakegopls", Command: "/x", Languages: []string{"go"},
	}}); err != nil {
		t.Fatal(err)
	}
	// Make the file known to the manager via ensureOpen first, so the URI used
	// in the publishDiagnostics matches the URI the tool will look up.
	s := m.servers["go"]
	if err := m.ensureOpen(ctx, s, file); err != nil {
		t.Fatal(err)
	}

	// Push a publishDiagnostics notification from the fake server side. The
	// manager's notification handler caches it under server.diagnostics.
	diags := []protocol.Diagnostic{{
		Range: protocol.Range{
			Start: protocol.Position{Line: 2, Character: 0},
			End:   protocol.Position{Line: 2, Character: 5},
		},
		Severity: protocol.DiagnosticSeverityError,
		Message:  "undeclared name: fmt",
	}}
	if err := fs.serverConn.Notify(ctx, protocol.MethodTextDocumentPublishDiagnostics, &protocol.PublishDiagnosticsParams{
		URI:         uri.File(file),
		Diagnostics: diags,
	}); err != nil {
		t.Fatalf("server Notify: %v", err)
	}
	// net.Pipe is synchronous but the handler runs on the manager's read
	// goroutine; poll briefly until the cache is populated.
	deadline := time.Now().Add(2 * time.Second)
	for {
		s.mu.Lock()
		got := len(s.diagnostics[string(uri.File(file))])
		s.mu.Unlock()
		if got > 0 || time.Now().After(deadline) {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}

	tool := toolByName(m.Tools(), "lsp_diagnostics")
	res := callTool(t, tool, map[string]any{"file": file})
	text := resultText(res)
	if !strings.Contains(text, "undeclared name: fmt") {
		t.Fatalf("diagnostics text missing message: %q", text)
	}
}

func TestLSPDefinition(t *testing.T) {
	handler := func(ctx context.Context, reply jsonrpc2.Replier, req jsonrpc2.Request) error {
		switch req.Method() {
		case protocol.MethodInitialize:
			return reply(ctx, &protocol.InitializeResult{}, nil)
		case protocol.MethodInitialized, protocol.MethodTextDocumentDidOpen:
			return reply(ctx, nil, nil)
		case protocol.MethodTextDocumentDefinition:
			locs := []protocol.Location{{
				URI: uri.File("/tmp/target.go"),
				Range: protocol.Range{
					Start: protocol.Position{Line: 10, Character: 4},
					End:   protocol.Position{Line: 10, Character: 8},
				},
			}}
			return reply(ctx, locs, nil)
		}
		return reply(ctx, nil, nil)
	}
	m, file := setupManagerWithFake(t, handler)

	tool := toolByName(m.Tools(), "lsp_definition")
	res := callTool(t, tool, map[string]any{"file": file, "line": 0, "character": 8})
	text := resultText(res)
	if !strings.Contains(text, "/tmp/target.go") || !strings.Contains(text, "10:4") {
		t.Fatalf("definition text missing fields: %q", text)
	}
}
