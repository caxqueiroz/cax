package lsp

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os/exec"
	"sync"

	"github.com/caxqueiroz/cax/internal/config"
	"go.lsp.dev/jsonrpc2"
	"go.lsp.dev/protocol"
	"go.lsp.dev/uri"
)

// ServerInfo is the per-server snapshot reported by New and surfaced via /lsp +
// channel.Status. Names + fields are contract-fixed.
type ServerInfo struct {
	Name      string
	Languages []string
	Running   bool
	LastError string
}

// dialFunc opens a jsonrpc2.Conn to a configured server and returns it plus a
// closeFn that closes the conn and reaps the child. Tests inject a fake
// dialFunc that returns an in-process net.Pipe Conn.
type dialFunc func(ctx context.Context, sc config.LSPServerConfig) (jsonrpc2.Conn, func() error, error)

// server is the live state for one spawned LSP child.
type server struct {
	name      string
	languages []string
	conn      jsonrpc2.Conn

	mu          sync.Mutex
	openFiles   map[string]bool
	diagnostics map[string][]protocol.Diagnostic

	closeFn func() error
}

// Manager owns the per-language server map and exposes generic LSP tools.
type Manager struct {
	rootDir string
	dialFn  dialFunc

	mu      sync.Mutex
	servers map[string]*server // language -> server
	all     []*server          // for Close (each server appears once)
}

// New spawns one LSP child per server config, performs the initialize/
// initialized handshake, and registers each by its claimed languages. Errors
// per server are logged + recorded in ServerInfo.LastError; others continue.
func New(ctx context.Context, servers []config.LSPServerConfig, rootDir string) (*Manager, []ServerInfo, error) {
	m := &Manager{
		rootDir: rootDir,
		dialFn:  dialStdio,
		servers: make(map[string]*server),
	}
	infos, err := m.bringUp(ctx, servers)
	if err != nil {
		return nil, nil, err
	}
	return m, infos, nil
}

// bringUp dials, handshakes, and registers each configured server. Per-server
// errors are recorded in the returned ServerInfo; bringUp itself returns nil
// unless something catastrophic (nil dialFn) prevents any attempt.
func (m *Manager) bringUp(ctx context.Context, servers []config.LSPServerConfig) ([]ServerInfo, error) {
	if m.dialFn == nil {
		return nil, fmt.Errorf("lsp: dialFn is nil")
	}
	infos := make([]ServerInfo, 0, len(servers))
	for _, sc := range servers {
		info := ServerInfo{Name: sc.Name, Languages: sc.Languages}
		conn, closeFn, err := m.dialFn(ctx, sc)
		if err != nil {
			info.LastError = err.Error()
			slog.Warn("lsp: dial failed", "server", sc.Name, "error", err)
			infos = append(infos, info)
			continue
		}
		s := &server{
			name:        sc.Name,
			languages:   sc.Languages,
			conn:        conn,
			openFiles:   make(map[string]bool),
			diagnostics: make(map[string][]protocol.Diagnostic),
			closeFn:     closeFn,
		}
		// Install the notification handler BEFORE initialize, so that
		// publishDiagnostics arriving immediately after `initialized` are
		// captured. The handler captures s by reference.
		conn.Go(ctx, m.notifyHandler(s))
		if err := m.handshake(ctx, s); err != nil {
			info.LastError = err.Error()
			slog.Warn("lsp: handshake failed", "server", sc.Name, "error", err)
			_ = closeFn()
			infos = append(infos, info)
			continue
		}
		info.Running = true
		m.mu.Lock()
		m.all = append(m.all, s)
		for _, lang := range sc.Languages {
			if _, exists := m.servers[lang]; exists {
				slog.Warn("lsp: duplicate language; ignoring later server",
					"language", lang, "server", sc.Name)
				continue
			}
			m.servers[lang] = s
		}
		m.mu.Unlock()
		infos = append(infos, info)
	}
	return infos, nil
}

// handshake performs initialize + initialized. WorkspaceFolders carries the
// configured rootDir as a file:// URI.
func (m *Manager) handshake(ctx context.Context, s *server) error {
	rootURI := uri.File(m.rootDir)
	var result protocol.InitializeResult
	params := &protocol.InitializeParams{
		RootURI: rootURI,
		WorkspaceFolders: []protocol.WorkspaceFolder{{
			URI:  string(rootURI),
			Name: "cax-root",
		}},
		Capabilities: protocol.ClientCapabilities{},
	}
	if _, err := s.conn.Call(ctx, protocol.MethodInitialize, params, &result); err != nil {
		return fmt.Errorf("initialize: %w", err)
	}
	if err := s.conn.Notify(ctx, protocol.MethodInitialized, &protocol.InitializedParams{}); err != nil {
		return fmt.Errorf("initialized: %w", err)
	}
	return nil
}

// notifyHandler is the jsonrpc2 Handler installed on each server connection:
// it captures publishDiagnostics into the server's cache and ignores other
// server-initiated messages.
func (m *Manager) notifyHandler(s *server) jsonrpc2.Handler {
	return func(ctx context.Context, reply jsonrpc2.Replier, req jsonrpc2.Request) error {
		if req.Method() == protocol.MethodTextDocumentPublishDiagnostics {
			var p protocol.PublishDiagnosticsParams
			if err := json.Unmarshal(req.Params(), &p); err == nil {
				s.mu.Lock()
				s.diagnostics[string(p.URI)] = p.Diagnostics
				s.mu.Unlock()
			}
		}
		return reply(ctx, nil, nil)
	}
}

// Close shuts down each server (best-effort). The first error encountered is
// returned; subsequent errors are logged inside the closeFn.
func (m *Manager) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	var firstErr error
	for _, s := range m.all {
		if s.closeFn == nil {
			continue
		}
		if err := s.closeFn(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	m.servers = nil
	m.all = nil
	return firstErr
}

// dialStdio is the production dialFunc: spawn command+args, wire stdio to a
// jsonrpc2 Stream/Conn, and return a closeFn that closes the conn and reaps
// the child.
func dialStdio(ctx context.Context, sc config.LSPServerConfig) (jsonrpc2.Conn, func() error, error) {
	cmd := exec.CommandContext(ctx, sc.Command, sc.Args...)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, nil, fmt.Errorf("stdin pipe: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, nil, fmt.Errorf("stdout pipe: %w", err)
	}
	// Stderr is intentionally discarded; real wiring would stream to slog.
	cmd.Stderr = io.Discard
	if err := cmd.Start(); err != nil {
		return nil, nil, fmt.Errorf("start %s: %w", sc.Command, err)
	}
	rwc := &stdioRWC{r: stdout, w: stdin}
	conn := jsonrpc2.NewConn(jsonrpc2.NewStream(rwc))
	closeFn := func() error {
		_ = conn.Close()
		_ = rwc.Close()
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
			_, _ = cmd.Process.Wait()
		}
		return nil
	}
	return conn, closeFn, nil
}

// stdioRWC bridges separate Reader/Writer pipes into an io.ReadWriteCloser
// for jsonrpc2.NewStream.
type stdioRWC struct {
	r io.ReadCloser
	w io.WriteCloser
}

func (s *stdioRWC) Read(p []byte) (int, error)  { return s.r.Read(p) }
func (s *stdioRWC) Write(p []byte) (int, error) { return s.w.Write(p) }
func (s *stdioRWC) Close() error {
	_ = s.r.Close()
	_ = s.w.Close()
	return nil
}
