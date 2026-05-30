package lsp

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/deepnoodle-ai/dive"
	"go.lsp.dev/protocol"
	"go.lsp.dev/uri"
)

const requestTimeout = 10 * time.Second

// Tools returns the six generic LSP tools. Names are contract-fixed; bodies
// for unimplemented tools are placeholders that return a clear message so the
// model adapts.
func (m *Manager) Tools() []dive.Tool {
	return []dive.Tool{
		m.definitionTool(),
		m.referencesTool(),
		m.hoverTool(),
		m.documentSymbolsTool(),
		m.workspaceSymbolsTool(),
		m.diagnosticsTool(),
	}
}

// positionArgs is the {file, line, character} input for definition/references/hover.
type positionArgs struct {
	File      string `json:"file" description:"Absolute path to the file."`
	Line      uint32 `json:"line" description:"Zero-based line number."`
	Character uint32 `json:"character" description:"Zero-based character offset within the line."`
}

// fileArgs is the {file} input for document_symbols/diagnostics.
type fileArgs struct {
	File string `json:"file" description:"Absolute path to the file."`
}

// queryArgs is the {query} input for workspace_symbols.
type queryArgs struct {
	Query string `json:"query" description:"Symbol query string."`
}

// noopArgs is the catch-all input used by placeholder tools.
type noopArgs struct{}

// noServerMessage explains to the model why a tool returned no LSP data so it
// can adapt (try a different tool, or proceed without LSP context).
func noServerMessage(file, lang string) string {
	if lang == "" {
		return fmt.Sprintf("no LSP server for file %q (extension not mapped)", file)
	}
	return fmt.Sprintf("no LSP server for language %q", lang)
}

func formatLocations(label string, locs []protocol.Location) string {
	if len(locs) == 0 {
		return fmt.Sprintf("%s: no results", label)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s (%d):\n", label, len(locs))
	for _, l := range locs {
		fmt.Fprintf(&b, "  %s %d:%d-%d:%d\n",
			string(l.URI),
			l.Range.Start.Line, l.Range.Start.Character,
			l.Range.End.Line, l.Range.End.Character)
	}
	return strings.TrimRight(b.String(), "\n")
}

func (m *Manager) definitionTool() dive.Tool {
	return dive.FuncTool("lsp_definition",
		"Go to definition for the symbol at file:line:character.",
		func(ctx context.Context, args *positionArgs) (*dive.ToolResult, error) {
			s, lang, ok := m.routeServer(args.File)
			if !ok {
				return dive.NewToolResultText(noServerMessage(args.File, lang)), nil
			}
			rctx, cancel := context.WithTimeout(ctx, requestTimeout)
			defer cancel()
			if err := m.ensureOpen(rctx, s, args.File); err != nil {
				return dive.NewToolResultText(fmt.Sprintf("lsp_definition: open %s: %v", args.File, err)), nil
			}
			var locs []protocol.Location
			if _, err := s.conn.Call(rctx, protocol.MethodTextDocumentDefinition, &protocol.DefinitionParams{
				TextDocumentPositionParams: protocol.TextDocumentPositionParams{
					TextDocument: protocol.TextDocumentIdentifier{URI: uri.File(args.File)},
					Position:     protocol.Position{Line: args.Line, Character: args.Character},
				},
			}, &locs); err != nil {
				return dive.NewToolResultText(fmt.Sprintf("lsp_definition: call: %v", err)), nil
			}
			return dive.NewToolResultText(formatLocations("definition", locs)), nil
		},
	)
}

func (m *Manager) referencesTool() dive.Tool {
	return dive.FuncTool("lsp_references",
		"List references for the symbol at file:line:character (includes declaration).",
		func(ctx context.Context, args *positionArgs) (*dive.ToolResult, error) {
			s, lang, ok := m.routeServer(args.File)
			if !ok {
				return dive.NewToolResultText(noServerMessage(args.File, lang)), nil
			}
			rctx, cancel := context.WithTimeout(ctx, requestTimeout)
			defer cancel()
			if err := m.ensureOpen(rctx, s, args.File); err != nil {
				return dive.NewToolResultText(fmt.Sprintf("lsp_references: open: %v", err)), nil
			}
			var locs []protocol.Location
			if _, err := s.conn.Call(rctx, protocol.MethodTextDocumentReferences, &protocol.ReferenceParams{
				TextDocumentPositionParams: protocol.TextDocumentPositionParams{
					TextDocument: protocol.TextDocumentIdentifier{URI: uri.File(args.File)},
					Position:     protocol.Position{Line: args.Line, Character: args.Character},
				},
				Context: protocol.ReferenceContext{IncludeDeclaration: true},
			}, &locs); err != nil {
				return dive.NewToolResultText(fmt.Sprintf("lsp_references: call: %v", err)), nil
			}
			return dive.NewToolResultText(formatLocations("references", locs)), nil
		},
	)
}

// Placeholders for the remaining tools; real bodies arrive in Tasks 7-10.
func (m *Manager) hoverTool() dive.Tool           { return placeholderTool("lsp_hover") }
func (m *Manager) documentSymbolsTool() dive.Tool { return placeholderTool("lsp_document_symbols") }
func (m *Manager) workspaceSymbolsTool() dive.Tool {
	return placeholderTool("lsp_workspace_symbols")
}
func (m *Manager) diagnosticsTool() dive.Tool { return placeholderTool("lsp_diagnostics") }

func placeholderTool(name string) dive.Tool {
	return dive.FuncTool(name, "placeholder; implemented in a follow-up task.",
		func(_ context.Context, _ *noopArgs) (*dive.ToolResult, error) {
			return dive.NewToolResultText(name + ": not yet implemented"), nil
		},
	)
}
