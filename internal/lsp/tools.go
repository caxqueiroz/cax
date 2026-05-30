package lsp

import (
	"context"
	"encoding/json"
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
func (m *Manager) hoverTool() dive.Tool {
	return dive.FuncTool("lsp_hover",
		"Show hover documentation for the symbol at file:line:character.",
		func(ctx context.Context, args *positionArgs) (*dive.ToolResult, error) {
			s, lang, ok := m.routeServer(args.File)
			if !ok {
				return dive.NewToolResultText(noServerMessage(args.File, lang)), nil
			}
			rctx, cancel := context.WithTimeout(ctx, requestTimeout)
			defer cancel()
			if err := m.ensureOpen(rctx, s, args.File); err != nil {
				return dive.NewToolResultText(fmt.Sprintf("lsp_hover: open: %v", err)), nil
			}
			var raw json.RawMessage
			if _, err := s.conn.Call(rctx, protocol.MethodTextDocumentHover, &protocol.HoverParams{
				TextDocumentPositionParams: protocol.TextDocumentPositionParams{
					TextDocument: protocol.TextDocumentIdentifier{URI: uri.File(args.File)},
					Position:     protocol.Position{Line: args.Line, Character: args.Character},
				},
			}, &raw); err != nil {
				return dive.NewToolResultText(fmt.Sprintf("lsp_hover: call: %v", err)), nil
			}
			return dive.NewToolResultText(formatHover(raw)), nil
		},
	)
}

// formatHover renders the LSP Hover result. In go.lsp.dev/protocol v0.12.0
// Hover.Contents is typed as MarkupContent (not a union), so we decode through
// json.RawMessage to tolerate alternate server shapes and fall back to the raw
// payload as a last resort.
func formatHover(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return "hover: no documentation"
	}
	var h protocol.Hover
	if err := json.Unmarshal(raw, &h); err == nil && h.Contents.Value != "" {
		return "hover:\n" + h.Contents.Value
	}
	return "hover: " + string(raw)
}

func (m *Manager) documentSymbolsTool() dive.Tool {
	return dive.FuncTool("lsp_document_symbols",
		"List symbols defined in a file (functions, types, vars).",
		func(ctx context.Context, args *fileArgs) (*dive.ToolResult, error) {
			s, lang, ok := m.routeServer(args.File)
			if !ok {
				return dive.NewToolResultText(noServerMessage(args.File, lang)), nil
			}
			rctx, cancel := context.WithTimeout(ctx, requestTimeout)
			defer cancel()
			if err := m.ensureOpen(rctx, s, args.File); err != nil {
				return dive.NewToolResultText(fmt.Sprintf("lsp_document_symbols: open: %v", err)), nil
			}
			var raw json.RawMessage
			if _, err := s.conn.Call(rctx, protocol.MethodTextDocumentDocumentSymbol, &protocol.DocumentSymbolParams{
				TextDocument: protocol.TextDocumentIdentifier{URI: uri.File(args.File)},
			}, &raw); err != nil {
				return dive.NewToolResultText(fmt.Sprintf("lsp_document_symbols: call: %v", err)), nil
			}
			return dive.NewToolResultText(formatDocumentSymbols(raw)), nil
		},
	)
}

// formatDocumentSymbols handles BOTH shapes the LSP spec allows: []DocumentSymbol
// or []SymbolInformation. We try DocumentSymbol first (richer); fall back.
func formatDocumentSymbols(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return "document_symbols: no symbols"
	}
	var docSyms []protocol.DocumentSymbol
	if err := json.Unmarshal(raw, &docSyms); err == nil && len(docSyms) > 0 {
		var b strings.Builder
		fmt.Fprintf(&b, "document_symbols (%d):\n", len(docSyms))
		for _, s := range docSyms {
			fmt.Fprintf(&b, "  %s (%s)\n", s.Name, symbolKindString(s.Kind))
		}
		return strings.TrimRight(b.String(), "\n")
	}
	var infoSyms []protocol.SymbolInformation
	if err := json.Unmarshal(raw, &infoSyms); err == nil && len(infoSyms) > 0 {
		var b strings.Builder
		fmt.Fprintf(&b, "document_symbols (%d):\n", len(infoSyms))
		for _, s := range infoSyms {
			fmt.Fprintf(&b, "  %s (%s) in %s\n", s.Name, symbolKindString(s.Kind), string(s.Location.URI))
		}
		return strings.TrimRight(b.String(), "\n")
	}
	return "document_symbols: no symbols"
}

func symbolKindString(k protocol.SymbolKind) string {
	switch k {
	case protocol.SymbolKindFile:
		return "file"
	case protocol.SymbolKindModule:
		return "module"
	case protocol.SymbolKindNamespace:
		return "namespace"
	case protocol.SymbolKindPackage:
		return "package"
	case protocol.SymbolKindClass:
		return "class"
	case protocol.SymbolKindMethod:
		return "method"
	case protocol.SymbolKindProperty:
		return "property"
	case protocol.SymbolKindField:
		return "field"
	case protocol.SymbolKindConstructor:
		return "constructor"
	case protocol.SymbolKindEnum:
		return "enum"
	case protocol.SymbolKindInterface:
		return "interface"
	case protocol.SymbolKindFunction:
		return "function"
	case protocol.SymbolKindVariable:
		return "variable"
	case protocol.SymbolKindConstant:
		return "constant"
	case protocol.SymbolKindString:
		return "string"
	case protocol.SymbolKindNumber:
		return "number"
	case protocol.SymbolKindBoolean:
		return "boolean"
	case protocol.SymbolKindArray:
		return "array"
	case protocol.SymbolKindObject:
		return "object"
	case protocol.SymbolKindKey:
		return "key"
	case protocol.SymbolKindNull:
		return "null"
	case protocol.SymbolKindEnumMember:
		return "enum_member"
	case protocol.SymbolKindStruct:
		return "struct"
	case protocol.SymbolKindEvent:
		return "event"
	case protocol.SymbolKindOperator:
		return "operator"
	case protocol.SymbolKindTypeParameter:
		return "type_parameter"
	}
	return fmt.Sprintf("kind(%d)", int(k))
}
func (m *Manager) workspaceSymbolsTool() dive.Tool {
	return dive.FuncTool("lsp_workspace_symbols",
		"Search the workspace for symbols matching a query string across all running language servers.",
		func(ctx context.Context, args *queryArgs) (*dive.ToolResult, error) {
			rctx, cancel := context.WithTimeout(ctx, requestTimeout)
			defer cancel()

			// De-duplicate servers (a server can be registered under multiple
			// languages); query each exactly once.
			m.mu.Lock()
			servers := make([]*server, 0, len(m.all))
			servers = append(servers, m.all...)
			m.mu.Unlock()

			if len(servers) == 0 {
				return dive.NewToolResultText("workspace_symbols: no LSP servers running"), nil
			}
			var combined []protocol.SymbolInformation
			for _, s := range servers {
				var syms []protocol.SymbolInformation
				if _, err := s.conn.Call(rctx, protocol.MethodWorkspaceSymbol, &protocol.WorkspaceSymbolParams{
					Query: args.Query,
				}, &syms); err != nil {
					// Skip a single server's failure; others may answer.
					continue
				}
				combined = append(combined, syms...)
			}
			return dive.NewToolResultText(formatSymbolInformation(combined)), nil
		},
	)
}

func formatSymbolInformation(syms []protocol.SymbolInformation) string {
	if len(syms) == 0 {
		return "workspace_symbols: no results"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "workspace_symbols (%d):\n", len(syms))
	for _, s := range syms {
		fmt.Fprintf(&b, "  %s (%s) %s %d:%d\n",
			s.Name, symbolKindString(s.Kind), string(s.Location.URI),
			s.Location.Range.Start.Line, s.Location.Range.Start.Character)
	}
	return strings.TrimRight(b.String(), "\n")
}
func (m *Manager) diagnosticsTool() dive.Tool { return placeholderTool("lsp_diagnostics") }

func placeholderTool(name string) dive.Tool {
	return dive.FuncTool(name, "placeholder; implemented in a follow-up task.",
		func(_ context.Context, _ *noopArgs) (*dive.ToolResult, error) {
			return dive.NewToolResultText(name + ": not yet implemented"), nil
		},
	)
}
