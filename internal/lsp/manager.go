package lsp

import (
	"context"
	"sync"

	"github.com/caxqueiroz/czcli/internal/config"
	"go.lsp.dev/jsonrpc2"
	"go.lsp.dev/protocol"
)

// ServerInfo is the per-server snapshot reported by New and surfaced via /lsp +
// channel.Status. Names + fields are contract-fixed.
type ServerInfo struct {
	Name      string
	Languages []string
	Running   bool
	LastError string
}

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

	mu      sync.Mutex
	servers map[string]*server // language -> server
	all     []*server          // for Close (each server appears once)
}

// New spawns one LSP child per server config, performs the initialize/
// initialized handshake, and registers each by its claimed languages. Errors
// per server are logged + recorded in ServerInfo.LastError; others continue.
// Task 1 returns an empty manager + no infos so the contract surface is in
// place; Task 3 fills in the real spawn + handshake path.
func New(_ context.Context, servers []config.LSPServerConfig, rootDir string) (*Manager, []ServerInfo, error) {
	m := &Manager{
		rootDir: rootDir,
		servers: make(map[string]*server),
	}
	infos := make([]ServerInfo, 0, len(servers))
	for _, sc := range servers {
		infos = append(infos, ServerInfo{Name: sc.Name, Languages: sc.Languages})
	}
	return m, infos, nil
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
