package lsp

import (
	"context"

	"github.com/deepnoodle-ai/dive"
)

// stubArgs is a permissive shape the placeholder tools accept so the dive
// schema generator has something to work with prior to Tasks 5-10 swapping in
// the real implementations.
type stubArgs struct {
	File      string `json:"file,omitempty" description:"Absolute path to a file."`
	Query     string `json:"query,omitempty" description:"Query string."`
	Line      int    `json:"line,omitempty" description:"Zero-based line."`
	Character int    `json:"character,omitempty" description:"Zero-based character."`
}

// Tools returns the six generic LSP tools. Bodies are filled per Tasks 5-10;
// the stub returns "no LSP server available" for any input so callers behave
// reasonably even with zero servers configured.
func (m *Manager) Tools() []dive.Tool {
	noop := func(name, desc string) dive.Tool {
		return dive.FuncTool(name, desc,
			func(_ context.Context, _ *stubArgs) (*dive.ToolResult, error) {
				return dive.NewToolResultText("no LSP server available"), nil
			},
		)
	}
	return []dive.Tool{
		noop("lsp_definition", "Go to definition for a symbol at a file position."),
		noop("lsp_references", "List references for a symbol at a file position."),
		noop("lsp_hover", "Show hover documentation for a symbol at a file position."),
		noop("lsp_document_symbols", "List symbols defined in a file."),
		noop("lsp_workspace_symbols", "Search workspace for symbols matching a query."),
		noop("lsp_diagnostics", "Return cached diagnostics for a file."),
	}
}
