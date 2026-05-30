package lsp

import (
	"context"
	"fmt"
	"os"

	"go.lsp.dev/protocol"
	"go.lsp.dev/uri"
)

// ensureOpen sends a textDocument/didOpen notification for path on s, unless
// the file is already tracked as open. It always uses the file's CURRENT
// on-disk text (czcli does not buffer edits between turns).
func (m *Manager) ensureOpen(ctx context.Context, s *server, path string) error {
	u := string(uri.File(path))
	s.mu.Lock()
	if s.openFiles[u] {
		s.mu.Unlock()
		return nil
	}
	s.openFiles[u] = true
	s.mu.Unlock()

	text, err := os.ReadFile(path)
	if err != nil {
		s.mu.Lock()
		delete(s.openFiles, u)
		s.mu.Unlock()
		return fmt.Errorf("read %s: %w", path, err)
	}
	lang := languageFor(path)
	return s.conn.Notify(ctx, protocol.MethodTextDocumentDidOpen, &protocol.DidOpenTextDocumentParams{
		TextDocument: protocol.TextDocumentItem{
			URI:        uri.File(path),
			LanguageID: protocol.LanguageIdentifier(lang),
			Version:    1,
			Text:       string(text),
		},
	})
}

// closeFile sends textDocument/didClose and clears the open-file flag.
func (m *Manager) closeFile(ctx context.Context, s *server, path string) error {
	u := string(uri.File(path))
	s.mu.Lock()
	if !s.openFiles[u] {
		s.mu.Unlock()
		return nil
	}
	delete(s.openFiles, u)
	s.mu.Unlock()
	return s.conn.Notify(ctx, protocol.MethodTextDocumentDidClose, &protocol.DidCloseTextDocumentParams{
		TextDocument: protocol.TextDocumentIdentifier{URI: uri.File(path)},
	})
}

// routeServer picks the server registered for the file's language, or returns
// (nil, lang, false) if no server is available. lang is "" if the extension
// has no default mapping.
func (m *Manager) routeServer(path string) (*server, string, bool) {
	lang := languageFor(path)
	if lang == "" {
		return nil, "", false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.servers[lang]
	return s, lang, ok
}
