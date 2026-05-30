// Package plugins implements Claude Code-compatible plugin discovery, parsing,
// install, and hot-reload for czcli. A plugin is a directory under one of
// cfg.Plugins.Dirs containing .claude-plugin/plugin.json plus optional
// commands/, .mcp.json, .claude-plugin/lsp.json, .claude-plugin/hooks.json,
// skills/, and agents/.
package plugins

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/caxqueiroz/cax/internal/config"
)

// Manifest is the parsed .claude-plugin/plugin.json. Only `name` is required;
// every other field is optional. Unknown JSON fields are tolerated (Claude
// Code's documented behavior).
type Manifest struct {
	Name        string `json:"name"`
	Version     string `json:"version"`
	Description string `json:"description"`
	Author      Author `json:"author"`
	Homepage    string `json:"homepage"`
	Repository  string `json:"repository"`
	License     string `json:"license"`
}

// Author is the plugin author metadata block.
type Author struct {
	Name  string `json:"name"`
	Email string `json:"email"`
	URL   string `json:"url"`
}

// ReadManifest reads and validates .claude-plugin/plugin.json under pluginDir.
// Returns an error if the file is missing, unparseable, or the required `name`
// field is empty.
func ReadManifest(pluginDir string) (Manifest, error) {
	path := filepath.Join(pluginDir, ".claude-plugin", "plugin.json")
	data, err := os.ReadFile(path)
	if err != nil {
		return Manifest{}, fmt.Errorf("read %s: %w", path, err)
	}
	var m Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return Manifest{}, fmt.Errorf("parse %s: %w", path, err)
	}
	if m.Name == "" {
		return Manifest{}, fmt.Errorf("manifest %s: missing required field %q", path, "name")
	}
	return m, nil
}

// ReadMCPServers parses <pluginDir>/.mcp.json and returns one
// config.MCPServerConfig per entry under "mcpServers". Missing file -> nil
// (silent). Bad JSON -> nil + slog.Warn. Per-server names sorted for
// deterministic ordering across runs. Env/headers pass through into the
// MCPServerConfig (Plan 6 widened the type to carry them).
func ReadMCPServers(pluginDir, pluginName string) []config.MCPServerConfig {
	path := filepath.Join(pluginDir, ".mcp.json")
	data, err := os.ReadFile(path)
	if err != nil {
		if !os.IsNotExist(err) {
			slog.Warn("plugins: read .mcp.json", "plugin", pluginName, "error", err)
		}
		return nil
	}
	var raw struct {
		MCPServers map[string]struct {
			Command string            `json:"command"`
			Args    []string          `json:"args"`
			URL     string            `json:"url"`
			Type    string            `json:"type"`
			Env     map[string]string `json:"env"`
			Headers map[string]string `json:"headers"`
		} `json:"mcpServers"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		slog.Warn("plugins: parse .mcp.json", "plugin", pluginName, "error", err)
		return nil
	}
	names := make([]string, 0, len(raw.MCPServers))
	for n := range raw.MCPServers {
		names = append(names, n)
	}
	sort.Strings(names)
	out := make([]config.MCPServerConfig, 0, len(names))
	for _, n := range names {
		s := raw.MCPServers[n]
		out = append(out, config.MCPServerConfig{
			Name:    n,
			Command: s.Command,
			Args:    s.Args,
			URL:     s.URL,
			Env:     s.Env,
			Headers: s.Headers,
		})
	}
	return out
}

// rawLSPSrv is the shared shape both lsp.json variants decode into.
type rawLSPSrv struct {
	Command             string            `json:"command"`
	Args                []string          `json:"args"`
	Languages           []string          `json:"languages"`
	RootPatterns        []string          `json:"rootPatterns"`
	ExtensionToLanguage map[string]string `json:"extensionToLanguage"`
}

// ReadLSPServers parses <pluginDir>/.claude-plugin/lsp.json. Accepts two
// shapes: czcli-native `{"servers": {"<name>": {...}}}` and Claude Code's
// `{"<lang>": {"command":...,"extensionToLanguage":{".go":"go"}}}`. Returns
// nil if file missing; logs + returns nil on parse error.
func ReadLSPServers(pluginDir, pluginName string) []config.LSPServerConfig {
	path := filepath.Join(pluginDir, ".claude-plugin", "lsp.json")
	data, err := os.ReadFile(path)
	if err != nil {
		if !os.IsNotExist(err) {
			slog.Warn("plugins: read lsp.json", "plugin", pluginName, "error", err)
		}
		return nil
	}
	// Try czcli-native shape first.
	var native struct {
		Servers map[string]rawLSPSrv `json:"servers"`
	}
	if err := json.Unmarshal(data, &native); err == nil && native.Servers != nil {
		return lspServersToConfig(native.Servers)
	}
	// Fall back to Claude Code shape: top-level is the server map.
	var claude map[string]rawLSPSrv
	if err := json.Unmarshal(data, &claude); err != nil {
		slog.Warn("plugins: parse lsp.json", "plugin", pluginName, "error", err)
		return nil
	}
	return lspServersToConfig(claude)
}

func lspServersToConfig(srvs map[string]rawLSPSrv) []config.LSPServerConfig {
	names := make([]string, 0, len(srvs))
	for n := range srvs {
		names = append(names, n)
	}
	sort.Strings(names)
	out := make([]config.LSPServerConfig, 0, len(names))
	for _, n := range names {
		s := srvs[n]
		langs := append([]string(nil), s.Languages...)
		if len(langs) == 0 && len(s.ExtensionToLanguage) > 0 {
			seen := make(map[string]bool, len(s.ExtensionToLanguage))
			for _, lang := range s.ExtensionToLanguage {
				if !seen[lang] {
					seen[lang] = true
					langs = append(langs, lang)
				}
			}
			sort.Strings(langs)
		}
		out = append(out, config.LSPServerConfig{
			Name:         n,
			Command:      s.Command,
			Args:         s.Args,
			Languages:    langs,
			RootPatterns: s.RootPatterns,
		})
	}
	return out
}

// HookEntry mirrors hooks.Entry per 01-extensibility-contracts.md but lives in
// the plugins package so plugins does not import hooks (avoids a Plan 9 cycle).
// Plan 9's hook dispatcher consumes Contributions.Hooks and converts to its
// internal hooks.Entry type.
type HookEntry struct {
	Event          string            // "UserPromptSubmit"|"PreToolUse"|"PostToolUse"|"Stop"
	Matcher        map[string]string // {"tool":"Bash"} | {"command":"rm"} | empty for match-all
	Command        []string          // argv (shell-split with shlexLite)
	TimeoutSeconds int               // default 5
	Source         string            // plugin name
}

var allowedHookEvents = map[string]bool{
	"UserPromptSubmit": true,
	"PreToolUse":       true,
	"PostToolUse":      true,
	"Stop":             true,
}

// ReadHooks parses <pluginDir>/.claude-plugin/hooks.json into a flat
// []HookEntry. Each innermost {type:"command", command, timeout} produces one
// entry; non-"command" types and unknown events are dropped with a warning.
// Matchers "Bash|Write" emit one HookEntry per pipe-separated token; "*"
// produces an empty Matcher (match-all).
func ReadHooks(pluginDir, pluginName string) []HookEntry {
	path := filepath.Join(pluginDir, ".claude-plugin", "hooks.json")
	data, err := os.ReadFile(path)
	if err != nil {
		if !os.IsNotExist(err) {
			slog.Warn("plugins: read hooks.json", "plugin", pluginName, "error", err)
		}
		return nil
	}
	var raw struct {
		Hooks map[string][]struct {
			Matcher string `json:"matcher"`
			Hooks   []struct {
				Type    string `json:"type"`
				Command string `json:"command"`
				Timeout int    `json:"timeout"`
			} `json:"hooks"`
		} `json:"hooks"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		slog.Warn("plugins: parse hooks.json", "plugin", pluginName, "error", err)
		return nil
	}
	events := make([]string, 0, len(raw.Hooks))
	for ev := range raw.Hooks {
		events = append(events, ev)
	}
	sort.Strings(events)
	var out []HookEntry
	for _, ev := range events {
		if !allowedHookEvents[ev] {
			slog.Warn("plugins: unsupported hook event", "plugin", pluginName, "event", ev)
			continue
		}
		for _, group := range raw.Hooks[ev] {
			matchers := expandMatchers(group.Matcher)
			for _, h := range group.Hooks {
				if h.Type != "" && h.Type != "command" {
					slog.Warn("plugins: unsupported hook type", "plugin", pluginName, "event", ev, "type", h.Type)
					continue
				}
				argv := shlexLite(h.Command)
				if len(argv) == 0 {
					continue
				}
				timeout := h.Timeout
				if timeout <= 0 {
					timeout = 5
				}
				for _, m := range matchers {
					out = append(out, HookEntry{
						Event:          ev,
						Matcher:        m,
						Command:        argv,
						TimeoutSeconds: timeout,
						Source:         pluginName,
					})
				}
			}
		}
	}
	return out
}

// expandMatchers turns Claude Code's matcher string into one or more
// HookEntry.Matcher maps. "" or "*" => one empty matcher (match-all). "Bash"
// => one {"tool":"Bash"}. "Bash|Write" => two entries. We treat the matcher
// as a tool-name pattern; Plan 9 may add command-substring matching later.
func expandMatchers(s string) []map[string]string {
	if s == "" || s == "*" {
		return []map[string]string{{}}
	}
	parts := splitPipe(s)
	out := make([]map[string]string, 0, len(parts))
	for _, p := range parts {
		out = append(out, map[string]string{"tool": p})
	}
	return out
}

func splitPipe(s string) []string {
	var out []string
	cur := ""
	for _, r := range s {
		if r == '|' {
			if cur != "" {
				out = append(out, cur)
			}
			cur = ""
			continue
		}
		cur += string(r)
	}
	if cur != "" {
		out = append(out, cur)
	}
	return out
}

// shlexLite is a minimal POSIX-ish shell splitter sufficient for hook command
// strings: whitespace separates tokens, double-quoted segments preserve spaces
// (and strip the outer quotes), backslash-escaping is NOT supported (rare in
// hook configs; Plan 9 can upgrade to a real shlex if a real plugin needs it).
func shlexLite(s string) []string {
	var out []string
	var cur []rune
	inQuote := false
	flush := func() {
		if len(cur) > 0 {
			out = append(out, string(cur))
			cur = cur[:0]
		}
	}
	for _, r := range s {
		switch {
		case r == '"':
			inQuote = !inQuote
		case (r == ' ' || r == '\t') && !inQuote:
			flush()
		default:
			cur = append(cur, r)
		}
	}
	flush()
	return out
}

// PluginCommand is one parsed commands/<name>.md. Name is the basename without
// .md (used as the slash-command name, namespaced by plugin at dispatch time).
// Prompt is the markdown body with $ARGUMENTS still present; the CLI expands it
// at invocation. ArgumentHint is cosmetic (autocomplete UI follow-up).
type PluginCommand struct {
	Name         string
	Description  string
	ArgumentHint string
	Prompt       string
	Source       string
}

type cmdFrontmatter struct {
	Description  string `yaml:"description"`
	ArgumentHint string `yaml:"argument-hint"`
}

// ReadCommands parses every *.md file under <pluginDir>/commands/ as a slash
// command. Missing dir => nil. Per-file parse errors logged + skipped.
func ReadCommands(pluginDir, pluginName string) []PluginCommand {
	cmdDir := filepath.Join(pluginDir, "commands")
	entries, err := os.ReadDir(cmdDir)
	if err != nil {
		if !os.IsNotExist(err) {
			slog.Warn("plugins: read commands/", "plugin", pluginName, "error", err)
		}
		return nil
	}
	var out []PluginCommand
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		path := filepath.Join(cmdDir, e.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			slog.Warn("plugins: read command file", "plugin", pluginName, "file", e.Name(), "error", err)
			continue
		}
		fm, body := splitFrontmatter(data)
		var meta cmdFrontmatter
		if len(fm) > 0 {
			if err := yaml.Unmarshal(fm, &meta); err != nil {
				slog.Warn("plugins: parse command frontmatter",
					"plugin", pluginName, "file", e.Name(), "error", err)
			}
		}
		name := strings.TrimSuffix(e.Name(), ".md")
		out = append(out, PluginCommand{
			Name:         name,
			Description:  meta.Description,
			ArgumentHint: meta.ArgumentHint,
			Prompt:       string(body),
			Source:       pluginName,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// splitFrontmatter returns the YAML between leading "---" markers and the
// remaining markdown body. If no leading "---\n" is found, returns nil, data.
func splitFrontmatter(data []byte) (yamlSrc, body []byte) {
	const sep = "---\n"
	if !bytes.HasPrefix(data, []byte(sep)) {
		return nil, data
	}
	rest := data[len(sep):]
	// Look for the closing fence on its own line: "\n---\n" or trailing "\n---".
	end := bytes.Index(rest, []byte("\n---\n"))
	if end < 0 {
		end = bytes.Index(rest, []byte("\n---"))
		if end < 0 {
			return nil, data
		}
	}
	fm := rest[:end]
	tail := rest[end:]
	tail = bytes.TrimPrefix(tail, []byte("\n---\n"))
	tail = bytes.TrimPrefix(tail, []byte("\n---"))
	tail = bytes.TrimPrefix(tail, []byte("\n"))
	return fm, tail
}

// ExpandArguments substitutes Claude Code's $ARGUMENTS placeholder. If the
// body contains $ARGUMENTS, every occurrence is replaced with args. Otherwise,
// if args is non-empty, "\n\nARGUMENTS: <args>" is appended (Claude Code's
// documented fallback so command authors who forgot the placeholder still see
// the user input).
func ExpandArguments(body, args string) string {
	if strings.Contains(body, "$ARGUMENTS") {
		return strings.ReplaceAll(body, "$ARGUMENTS", args)
	}
	if args == "" {
		return body
	}
	return body + "\n\nARGUMENTS: " + args
}
