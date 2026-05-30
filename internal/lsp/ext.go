// Package lsp wires Language Server Protocol clients into cax as a set of
// generic dive FuncTools. Routing is file-extension → language → server; per
// the extensibility contracts the default extension map is fixed and may be
// extended (but not narrowed) per server via LSPServerConfig.Languages.
package lsp

import (
	"path/filepath"
	"strings"
)

// defaultExt maps a file extension (lowercase, with leading dot) to the LSP
// language ID a server typically advertises. Mirrors the contract verbatim.
var defaultExt = map[string]string{
	".go":   "go",
	".py":   "python",
	".ts":   "typescript",
	".tsx":  "typescript",
	".js":   "javascript",
	".jsx":  "javascript",
	".rs":   "rust",
	".rb":   "ruby",
	".java": "java",
	".c":    "c",
	".cpp":  "cpp",
	".h":    "c",
	".hpp":  "cpp",
	".cs":   "csharp",
	".php":  "php",
	".lua":  "lua",
	".yaml": "yaml",
	".yml":  "yaml",
	".json": "json",
	".md":   "markdown",
}

// languageFor returns the LSP language ID for a path's extension, or "" if the
// extension has no default mapping. Caller decides whether a server-level
// override (LSPServerConfig.Languages) widens this.
func languageFor(path string) string {
	return defaultExt[strings.ToLower(filepath.Ext(path))]
}
