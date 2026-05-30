# Plan 7: Plugins (Claude Code-compatible) — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add Claude Code-compatible plugin bundles to czcli — drop-folder discovery under `~/.czcli/plugins/<name>/` and `.czcli/plugins/<name>/`, parsing of `.claude-plugin/plugin.json` + `commands/*.md` + `.mcp.json` + `.claude-plugin/lsp.json` + `.claude-plugin/hooks.json`, an injectable `git clone`-based `Install`, atomic state in `~/.czcli/plugins.json`, the `/plugin (list|install|enable|disable|remove)` slash commands, and a `Rebuild` callback that hot-reloads the agent after every mutation.

**Architecture:** A single `plugins.Manager` owns discovery + state + mutation. `Load(ctx)` walks `cfg.Plugins.Dirs`, parses every plugin's manifests with per-file error tolerance, applies the enabled/disabled state, and returns a flat `Contributions` struct (skill dirs, agent dirs, MCP servers, LSP servers, hook entries, slash commands) plus a `[]PluginInfo` for `/plugin list` and `channel.Status`. `Install` calls an injected `CloneFunc(ctx, url, dest)` (the tests pass a fake; production passes `exec.CommandContext("git","clone",...)`); `Enable/Disable/Remove` mutate the state file. The CLI gets a thin `pluginBackend` interface mirroring Plan 5's `scheduleBackend`, wired through `cli.WithPlugins(backend)`. Every mutation calls a `Rebuild` callback supplied at construction time; `cmd/czcli/main.go` provides one that re-runs `plugins.Manager.Load` + `skills.Load(extraDirs=contrib.SkillDirs)` + `mcp.Connect(merge(user, contrib.MCPServers), …)` + `assistant.Rebuild(...)`.

**Tech Stack:** Go 1.25, stdlib only inside `internal/plugins` (`encoding/json`, `os`, `path/filepath`, `os/exec`, `log/slog`, `sync`), `gopkg.in/yaml.v3` for command frontmatter (already in go.mod via Plan 1), existing `internal/config` + `internal/channel/cli` packages.

---

## Research notes (authoritative — verified 2026-05-30 against `code.claude.com/docs/en/plugins-reference`)

- **Manifest location:** `.claude-plugin/plugin.json` at plugin root. Only `name` is required; everything else optional. Plan 7 parses these top-level fields: `name` (string, kebab-case), `version` (string), `description` (string), `author` (object `{name, email, url}`), `homepage` (string), `repository` (string), `license` (string), `keywords` ([]string). Unknown fields are ignored at runtime (Claude Code documents this verbatim). We **do not** read inline `commands` / `agents` / `hooks` / `mcpServers` / `lspServers` path-override fields in v1 — sticking to the default file locations keeps the parser tiny and matches the on-disk layout the spec promises (`commands/`, `.mcp.json`, `.claude-plugin/hooks.json`, `.claude-plugin/lsp.json`). Path-override support is a YAGNI follow-up if a real plugin needs it.
- **`commands/*.md` frontmatter:** YAML between `---` markers. Fields we surface: `description` (string — shown in `/help` / `/plugin list`), `argument-hint` (string — cosmetic placeholder, surfaced in `PluginCommand` for future autocomplete). We **ignore** `allowed-tools`, `disallowed-tools`, `model` in v1 (out of scope: czcli does not yet have per-command tool restriction; logged at parse time as `[unsupported]`).
- **`$ARGUMENTS` expansion (decision):** v1 ships a **whole-string substitution** of the literal token `$ARGUMENTS` in the prompt body with whatever follows `/<plugin-name>:<command>` on the slash line. We do **not** implement `$ARGUMENTS[N]` / `$N` / named `arguments` frontmatter in v1 — those are listed as a YAGNI follow-up. Rationale: matches Claude Code's documented baseline (`$ARGUMENTS` is "the entire trailing string"), is one line of `strings.ReplaceAll`, covers ~all real-world plugin commands, and the indexed/named forms can land later without changing the `PluginCommand` shape. If `$ARGUMENTS` is absent in the body and args are non-empty, we append `\n\nARGUMENTS: <value>` (Claude Code's documented fallback) so command authors who forgot it still see input.
- **`.mcp.json` shape:** top-level `{"mcpServers": { "<name>": { … } } }`. Per-server fields we read: `command` (string, stdio) | `url` (string, HTTP) | `type` (string, optional — `"stdio"` or `"http"`; transport is otherwise inferred from which of `command`/`url` is set), `args` ([]string), `env` (map[string]string), `headers` (map[string]string, HTTP only). We pass these straight into `config.MCPServerConfig` (which already carries `Name`, `Command`, `Args`, `URL`); `env` and `headers` are stored on a new optional `Env map[string]string` / `Headers map[string]string` field added by Plan 6 to `config.MCPServerConfig` — Plan 7 does NOT widen `MCPServerConfig`; we drop `env`/`headers` with a `[mcp-extension]` warning log and let Plan 6 widen the type if/when it lands. The current `MCPServerConfig{Name, Command, Args, URL}` (00-shared-contracts.md) is sufficient for v1 of Plan 7.
- **`.claude-plugin/hooks.json` shape:** Claude Code's canonical structure is `{"hooks": {"<EventName>": [{ "matcher": "<pattern>", "hooks": [{ "type": "command", "command": "<argv-or-shell>", "timeout": <int> }] }]}}`. Plan 7's `HookEntry` (per 01-extensibility-contracts.md `§HookEntry`) flattens that: one `HookEntry` per innermost `{type, command}` pair, with `Event` lifted from the outer key, `Matcher` parsed from the string (`"Bash"` → `{"tool":"Bash"}`; `"Bash|Write"` → emitted as two entries; `"*"` → empty matcher = match-all), `Command` split via shell-words for `type:"command"` (other `type` values produce an `[unsupported-hook-type]` warning and are skipped), `TimeoutSeconds` from `timeout` (default 5). Source = plugin name. Event whitelist for v1: `UserPromptSubmit`, `PreToolUse`, `PostToolUse`, `Stop` — every other event Claude Code documents (32+) is dropped with a `[unsupported-event]` warning and is a YAGNI follow-up.
- **`.claude-plugin/lsp.json` shape:** the spec ships `{"servers": { "<name>": {"command": "...", "args": [...], "languages": [...], "rootPatterns": [...]}}}`. Claude Code's actual format is `{"<lang>": {"command":"...", "args":[...], "extensionToLanguage": {".go":"go"}}}` (no top-level `servers` key, no `languages` array — extensions live in `extensionToLanguage`). Plan 7 reads **both shapes** (probes for top-level `servers` key; if absent treats the whole object as the server map) and translates Claude Code's `extensionToLanguage` keys' values into a deduped `Languages` list (deterministic order). This keeps drop-in compatibility with both czcli-native and Claude Code plugins. Output: `[]config.LSPServerConfig` (verbatim from 01-extensibility-contracts.md).
- **`git clone` invocation pattern (production `CloneFunc`):** `exec.CommandContext(ctx, "git", "clone", "--depth=1", gitURL, dest)`. We pin `--depth=1` (no history needed for runtime). Tests inject a fake that just `os.MkdirAll(dest, 0o755)` + writes a stub `.claude-plugin/plugin.json` so no real `git` runs.
- **Repo pattern to mirror (Plan 5 `scheduleBackend`):** `internal/channel/cli/cli.go` defines `WithScheduler(b scheduleBackend) Option`; `internal/channel/cli/model.go` defines the `scheduleBackend` interface and a `sched` field on `model`; `internal/channel/cli/commands.go` dispatches `/schedule` to `m.sched` methods; `internal/channel/cli/schedule_test.go` defines a `fakeSchedules` in-memory backend for unit tests. Plan 7 follows this exact shape for `pluginBackend` + `WithPlugins` + `fakePlugins`.

---

## File Structure

```
internal/plugins/
├── manifest.go        # parsers: plugin.json, .mcp.json, lsp.json, hooks.json, commands/*.md
├── manifest_test.go   # per-parser unit tests with t.TempDir() fixtures
├── state.go           # atomic load/save of ~/.czcli/plugins.json; corruption tolerance
├── state_test.go      # round-trip + corruption-tolerance tests
├── manager.go         # Manager{}, New, Load, Install, Enable, Disable, Remove
└── manager_test.go    # end-to-end tests with mock CloneFunc; no real git

internal/config/config.go            # MODIFY: add PluginsConfig + defaults + tilde expansion
internal/channel/cli/cli.go          # MODIFY: WithPlugins(backend) option
internal/channel/cli/model.go        # MODIFY: pluginBackend interface + plugins field
internal/channel/cli/commands.go     # MODIFY: /plugin dispatcher + subcommand handlers
internal/channel/cli/plugin_test.go  # NEW: fakePlugins + /plugin command tests
internal/channel/channel.go          # MODIFY: ensure Status.PluginCount/PluginNames are populated (wired)
cmd/czcli/main.go                    # MODIFY: build Manager, wire pluginBackend, define Rebuild callback
config.example.yaml                  # MODIFY: add plugins: section
```

Dependencies assumed (do NOT define here):
- Plan 1: `config.Config`, `config.MCPServerConfig{Name,Command,Args,URL}`, `expandHome` (private helper — Plan 7 promotes it to package-internal use via a new exported helper added in Task 1).
- 01-extensibility-contracts.md (this plan owns these — defined here, verbatim):
  - `config.PluginsConfig{Enabled bool; Dirs []string}` (Task 1).
  - `plugins.Manifest`, `plugins.Author`, `plugins.Contributions`, `plugins.HookEntry`, `plugins.PluginCommand`, `plugins.PluginInfo`, `plugins.CloneFunc`, `plugins.Manager`, `plugins.New`, `plugins.Manager.Load`, `plugins.Manager.Install`, `plugins.Manager.Enable`, `plugins.Manager.Disable`, `plugins.Manager.Remove`.
- 01-extensibility-contracts.md (assumed landed via Plan 6 or stubbed if not — Plan 7 still compiles if `channel.Status` does not yet carry `PluginCount/PluginNames`; the Task 7 wiring is a no-op then, and lands cleanly once Plan 6 widens `channel.Status`):
  - `channel.Status.PluginCount int`, `channel.Status.PluginNames []string`.
- `config.LSPServerConfig` (01-extensibility-contracts.md §LSPConfig) — referenced by `Contributions.LSPServers`. If Plan 8 has not landed at integration time, Plan 7 declares a minimal local `type LSPServerConfig struct { Name, Command string; Args, Languages, RootPatterns []string }` inside `internal/config/lsp.go` (Task 1, additive — no breakage if Plan 8 then re-declares the same shape since Go forbids duplicate declarations only across files in the same package: Plan 8 deletes the stub at integration). Plan 7's tests don't depend on `LSPServerConfig` field names beyond what 01-extensibility-contracts.md pins.

---

### Task 1 — Config: add `PluginsConfig`, defaults, tilde expansion

**Files:** `internal/config/config.go`, `internal/config/config_test.go`, `config.example.yaml`

- [ ] Write FAILING test `internal/config/config_test.go` (append at end of file; if not present, create with the existing package-level `package config` and table-driven shape):

```go
package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadPluginsDefaultsAndTildeExpansion(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	yaml := `
providers:
  - name: openai
    model: gpt-5.4
    api_key_env: OPENAI_API_KEY
embeddings:
  provider: openai
  model: text-embedding-3-small
  dim: 1536
  api_key_env: OPENAI_API_KEY
memory:
  db_path: /tmp/x.db
`
	if err := os.WriteFile(path, []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !cfg.Plugins.Enabled {
		t.Fatalf("Plugins.Enabled default = false, want true")
	}
	if len(cfg.Plugins.Dirs) != 2 {
		t.Fatalf("Plugins.Dirs len = %d, want 2 (%v)", len(cfg.Plugins.Dirs), cfg.Plugins.Dirs)
	}
	home, _ := os.UserHomeDir()
	wantUser := filepath.Join(home, ".czcli", "plugins")
	if cfg.Plugins.Dirs[0] != wantUser {
		t.Errorf("Plugins.Dirs[0] = %q, want %q", cfg.Plugins.Dirs[0], wantUser)
	}
	if !strings.HasSuffix(cfg.Plugins.Dirs[1], ".czcli/plugins") {
		t.Errorf("Plugins.Dirs[1] = %q, want suffix .czcli/plugins", cfg.Plugins.Dirs[1])
	}
}

func TestLoadPluginsOverride(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	yaml := `
providers:
  - name: openai
    model: gpt-5.4
    api_key_env: OPENAI_API_KEY
embeddings:
  provider: openai
  model: text-embedding-3-small
  dim: 1536
  api_key_env: OPENAI_API_KEY
memory:
  db_path: /tmp/x.db
plugins:
  enabled: false
  dirs: [~/my-plugins, ./pkg/plugins]
`
	if err := os.WriteFile(path, []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Plugins.Enabled {
		t.Errorf("Plugins.Enabled = true, want false (override)")
	}
	if len(cfg.Plugins.Dirs) != 2 {
		t.Fatalf("Plugins.Dirs len = %d, want 2", len(cfg.Plugins.Dirs))
	}
	home, _ := os.UserHomeDir()
	if cfg.Plugins.Dirs[0] != filepath.Join(home, "my-plugins") {
		t.Errorf("Plugins.Dirs[0] = %q, want home-expanded", cfg.Plugins.Dirs[0])
	}
	if cfg.Plugins.Dirs[1] != "./pkg/plugins" {
		t.Errorf("Plugins.Dirs[1] = %q, want unchanged", cfg.Plugins.Dirs[1])
	}
}
```

- [ ] Run `go test ./internal/config/...` → FAIL (`cfg.Plugins` undefined).

- [ ] Minimal impl — edit `internal/config/config.go`:

  a) Add field to `Config` (insert after the existing `MCP MCPConfig` line):

  ```go
  	MCP        MCPConfig        `yaml:"mcp"`
  	Plugins    PluginsConfig    `yaml:"plugins"`
  	Schedules  []ScheduleConfig `yaml:"schedules"`
  ```

  b) Add new type at end of the type block (just before `// Load`):

  ```go
  // PluginsConfig configures Claude Code-compatible plugin discovery.
  // Defaults: enabled, dirs=[~/.czcli/plugins, .czcli/plugins]. ~/ is expanded.
  type PluginsConfig struct {
  	Enabled bool     `yaml:"enabled"`
  	Dirs    []string `yaml:"dirs"`
  }
  ```

  c) Extend `applyDefaults` (append before its closing brace):

  ```go
  	if cfg.Plugins.Dirs == nil {
  		cfg.Plugins.Enabled = true
  		cfg.Plugins.Dirs = []string{"~/.czcli/plugins", ".czcli/plugins"}
  	} else if !cfg.Plugins.Enabled && len(cfg.Plugins.Dirs) == 0 {
  		cfg.Plugins.Enabled = true
  	}
  ```

  Subtlety: yaml decodes `plugins:` with explicit `enabled: false` and explicit `dirs:` into a non-nil slice; only a fully omitted `plugins:` (slice == nil) gets defaulted to enabled. The `else if` branch keeps the existing behavior of treating a present-but-empty `dirs:` as "use no dirs but stay enabled" only if the user also did not set enabled. Plan 7 keeps this minimal: if `plugins:` is present in YAML, whatever was set wins; only full omission triggers defaults.

  d) Extend `Load` (insert immediately after the existing `cfg.Memory.DBPath = expanded` line and before `validate(&cfg)`):

  ```go
  	for i, d := range cfg.Plugins.Dirs {
  		ed, err := expandHome(d)
  		if err != nil {
  			return nil, fmt.Errorf("expand plugins.dirs[%d]: %w", i, err)
  		}
  		cfg.Plugins.Dirs[i] = ed
  	}
  ```

- [ ] Run `go test ./internal/config/...` → PASS.

- [ ] Append to `config.example.yaml` (before the trailing newline):

  ```yaml
  plugins:
    enabled: true
    dirs:
      - ~/.czcli/plugins
      - .czcli/plugins
  ```

- [ ] Commit:

  ```bash
  git add internal/config/config.go internal/config/config_test.go config.example.yaml
  git commit -m "$(cat <<'EOF'
  feat(config): add PluginsConfig with drop-folder defaults

  Plan 7 prep: PluginsConfig{Enabled, Dirs} with defaults
  [~/.czcli/plugins, .czcli/plugins] and tilde expansion on Dirs.

  Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
  EOF
  )"
  ```

---

### Task 2 — `plugins.Manifest` + `plugin.json` parser

**Files:** `internal/plugins/manifest.go`, `internal/plugins/manifest_test.go`

- [ ] Write FAILING test `internal/plugins/manifest_test.go`:

```go
package plugins

import (
	"os"
	"path/filepath"
	"testing"
)

func writePlugin(t *testing.T, root, name, manifestJSON string) string {
	t.Helper()
	dir := filepath.Join(root, name)
	if err := os.MkdirAll(filepath.Join(dir, ".claude-plugin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if manifestJSON != "" {
		if err := os.WriteFile(filepath.Join(dir, ".claude-plugin", "plugin.json"), []byte(manifestJSON), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestReadManifestHappy(t *testing.T) {
	dir := writePlugin(t, t.TempDir(), "my-plugin", `{
		"name": "my-plugin",
		"version": "1.2.3",
		"description": "A nice plugin",
		"author": {"name":"Alice","email":"a@x","url":"https://a"},
		"homepage": "https://example.com",
		"unknownField": 42
	}`)

	m, err := ReadManifest(dir)
	if err != nil {
		t.Fatalf("ReadManifest: %v", err)
	}
	if m.Name != "my-plugin" || m.Version != "1.2.3" || m.Description != "A nice plugin" {
		t.Errorf("manifest fields = %+v", m)
	}
	if m.Author.Name != "Alice" || m.Author.URL != "https://a" {
		t.Errorf("author = %+v", m.Author)
	}
	if m.Homepage != "https://example.com" {
		t.Errorf("homepage = %q", m.Homepage)
	}
}

func TestReadManifestMissingFile(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "no-manifest")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadManifest(dir); err == nil {
		t.Fatal("expected error for missing manifest")
	}
}

func TestReadManifestBadJSON(t *testing.T) {
	dir := writePlugin(t, t.TempDir(), "bad", `{not json`)
	if _, err := ReadManifest(dir); err == nil {
		t.Fatal("expected error for invalid JSON")
	}
}

func TestReadManifestRequiresName(t *testing.T) {
	dir := writePlugin(t, t.TempDir(), "nameless", `{"version":"1.0.0"}`)
	if _, err := ReadManifest(dir); err == nil {
		t.Fatal("expected error when name is missing")
	}
}
```

- [ ] Run `go test ./internal/plugins/...` → FAIL (package does not exist).

- [ ] Minimal impl `internal/plugins/manifest.go`:

```go
// Package plugins implements Claude Code-compatible plugin discovery, parsing,
// install, and hot-reload for czcli. A plugin is a directory under one of
// cfg.Plugins.Dirs containing .claude-plugin/plugin.json plus optional
// commands/, .mcp.json, .claude-plugin/lsp.json, .claude-plugin/hooks.json.
package plugins

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
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
		return Manifest{}, fmt.Errorf("manifest %s: missing required field \"name\"", path)
	}
	return m, nil
}
```

- [ ] Run `go test ./internal/plugins/...` → PASS.

- [ ] Commit:

  ```bash
  git add internal/plugins/manifest.go internal/plugins/manifest_test.go
  git commit -m "$(cat <<'EOF'
  feat(plugins): parse Claude Code .claude-plugin/plugin.json

  Manifest{Name, Version, Description, Author{Name,Email,URL},
  Homepage, Repository, License}. Unknown fields tolerated.

  Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
  EOF
  )"
  ```

---

### Task 3 — `.mcp.json` parser → `[]config.MCPServerConfig`

**Files:** `internal/plugins/manifest.go`, `internal/plugins/manifest_test.go`

- [ ] Append FAILING test to `internal/plugins/manifest_test.go`:

```go
func TestReadMCPServersHappy(t *testing.T) {
	root := t.TempDir()
	dir := writePlugin(t, root, "p", `{"name":"p"}`)
	if err := os.WriteFile(filepath.Join(dir, ".mcp.json"), []byte(`{
		"mcpServers": {
			"local-db": {"command":"./bin/db","args":["--port=5432"]},
			"remote-api": {"url":"https://api.example.com/mcp"}
		}
	}`), 0o644); err != nil {
		t.Fatal(err)
	}
	srvs := ReadMCPServers(dir, "p")
	if len(srvs) != 2 {
		t.Fatalf("len = %d, want 2 (%+v)", len(srvs), srvs)
	}
	byName := map[string]int{srvs[0].Name: 0, srvs[1].Name: 1}
	db := srvs[byName["local-db"]]
	if db.Command != "./bin/db" || len(db.Args) != 1 || db.Args[0] != "--port=5432" {
		t.Errorf("local-db = %+v", db)
	}
	api := srvs[byName["remote-api"]]
	if api.URL != "https://api.example.com/mcp" {
		t.Errorf("remote-api = %+v", api)
	}
}

func TestReadMCPServersMissing(t *testing.T) {
	root := t.TempDir()
	dir := writePlugin(t, root, "p", `{"name":"p"}`)
	if got := ReadMCPServers(dir, "p"); got != nil {
		t.Errorf("missing .mcp.json should return nil, got %+v", got)
	}
}

func TestReadMCPServersBadJSON(t *testing.T) {
	root := t.TempDir()
	dir := writePlugin(t, root, "p", `{"name":"p"}`)
	if err := os.WriteFile(filepath.Join(dir, ".mcp.json"), []byte(`{bogus`), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := ReadMCPServers(dir, "p"); got != nil {
		t.Errorf("bad JSON should log+return nil, got %+v", got)
	}
}
```

- [ ] Run `go test ./internal/plugins/...` → FAIL.

- [ ] Minimal impl — append to `internal/plugins/manifest.go`:

```go
import (
	// existing
	"log/slog"
	"sort"

	"github.com/caxqueiroz/czcli/internal/config"
)
```

(Adjust existing import block accordingly; keep stdlib then third-party then internal grouping.)

```go
// ReadMCPServers parses <pluginDir>/.mcp.json and returns one
// config.MCPServerConfig per entry under "mcpServers". Missing file -> nil
// (silent). Bad JSON -> nil + slog.Warn. Per-server names sorted for
// deterministic ordering across runs.
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
		if len(s.Env) > 0 || len(s.Headers) > 0 {
			slog.Warn("plugins: mcp env/headers not yet plumbed; dropping",
				"plugin", pluginName, "server", n)
		}
		out = append(out, config.MCPServerConfig{
			Name:    n,
			Command: s.Command,
			Args:    s.Args,
			URL:     s.URL,
		})
	}
	return out
}
```

- [ ] Run `go test ./internal/plugins/...` → PASS.

- [ ] Commit:

  ```bash
  git add internal/plugins/manifest.go internal/plugins/manifest_test.go
  git commit -m "$(cat <<'EOF'
  feat(plugins): parse .mcp.json into config.MCPServerConfig

  Supports stdio (command/args) and HTTP (url) servers. env/headers
  logged as not-yet-plumbed and dropped (Plan 6 widens MCPServerConfig).

  Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
  EOF
  )"
  ```

---

### Task 4 — `.claude-plugin/lsp.json` parser → `[]config.LSPServerConfig`

**Files:** `internal/plugins/manifest.go`, `internal/plugins/manifest_test.go`, `internal/config/lsp.go` (NEW stub if Plan 8 hasn't landed)

- [ ] Add `internal/config/lsp.go` ONLY if `config.LSPServerConfig` is not already defined in the package (check with `grep -rn "type LSPServerConfig" internal/config/`). If absent, write the minimal stub:

```go
package config

// LSPServerConfig configures a Language Server Protocol server. Stubbed by
// Plan 7 if Plan 8 has not yet landed; Plan 8 owns the full LSP runtime.
type LSPServerConfig struct {
	Name         string   `yaml:"name"`
	Command      string   `yaml:"command"`
	Args         []string `yaml:"args"`
	Languages    []string `yaml:"languages"`
	RootPatterns []string `yaml:"root_patterns"`
}
```

- [ ] Append FAILING test to `internal/plugins/manifest_test.go`:

```go
func TestReadLSPServersServersShape(t *testing.T) {
	root := t.TempDir()
	dir := writePlugin(t, root, "p", `{"name":"p"}`)
	if err := os.MkdirAll(filepath.Join(dir, ".claude-plugin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".claude-plugin", "lsp.json"), []byte(`{
		"servers": {
			"gopls": {"command":"gopls","args":["serve"],"languages":["go"],"rootPatterns":["go.mod"]}
		}
	}`), 0o644); err != nil {
		t.Fatal(err)
	}
	srvs := ReadLSPServers(dir, "p")
	if len(srvs) != 1 || srvs[0].Name != "gopls" || srvs[0].Command != "gopls" {
		t.Fatalf("got %+v", srvs)
	}
	if len(srvs[0].Languages) != 1 || srvs[0].Languages[0] != "go" {
		t.Errorf("languages = %v", srvs[0].Languages)
	}
}

func TestReadLSPServersClaudeShape(t *testing.T) {
	root := t.TempDir()
	dir := writePlugin(t, root, "p", `{"name":"p"}`)
	if err := os.MkdirAll(filepath.Join(dir, ".claude-plugin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".claude-plugin", "lsp.json"), []byte(`{
		"go": {"command":"gopls","args":["serve"],"extensionToLanguage":{".go":"go"}}
	}`), 0o644); err != nil {
		t.Fatal(err)
	}
	srvs := ReadLSPServers(dir, "p")
	if len(srvs) != 1 || srvs[0].Name != "go" || srvs[0].Command != "gopls" {
		t.Fatalf("got %+v", srvs)
	}
	if len(srvs[0].Languages) != 1 || srvs[0].Languages[0] != "go" {
		t.Errorf("languages = %v", srvs[0].Languages)
	}
}

func TestReadLSPServersMissing(t *testing.T) {
	root := t.TempDir()
	dir := writePlugin(t, root, "p", `{"name":"p"}`)
	if got := ReadLSPServers(dir, "p"); got != nil {
		t.Errorf("missing lsp.json should return nil, got %+v", got)
	}
}
```

- [ ] Run `go test ./internal/plugins/...` → FAIL.

- [ ] Minimal impl — append to `internal/plugins/manifest.go`:

```go
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
	type rawSrv struct {
		Command             string            `json:"command"`
		Args                []string          `json:"args"`
		Languages           []string          `json:"languages"`
		RootPatterns        []string          `json:"rootPatterns"`
		ExtensionToLanguage map[string]string `json:"extensionToLanguage"`
	}
	// Try czcli-native shape first.
	var native struct {
		Servers map[string]rawSrv `json:"servers"`
	}
	if err := json.Unmarshal(data, &native); err == nil && native.Servers != nil {
		return lspServersToConfig(native.Servers)
	}
	// Fall back to Claude Code shape: top-level is the server map.
	var claude map[string]rawSrv
	if err := json.Unmarshal(data, &claude); err != nil {
		slog.Warn("plugins: parse lsp.json", "plugin", pluginName, "error", err)
		return nil
	}
	return lspServersToConfig(claude)
}

func lspServersToConfig(srvs map[string]struct {
	Command             string            `json:"command"`
	Args                []string          `json:"args"`
	Languages           []string          `json:"languages"`
	RootPatterns        []string          `json:"rootPatterns"`
	ExtensionToLanguage map[string]string `json:"extensionToLanguage"`
}) []config.LSPServerConfig {
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
```

Note: Go does not allow an inline-struct type literal to be reused as a function parameter type identically; pull the inline shape into a named `type rawLSPSrv struct{…}` at file level if duplicated literals get unwieldy. v1 keeps both literals identical-by-shape (Go accepts that for typed map values used via the same anonymous struct) — if the compiler rejects the inline parameter, hoist `rawLSPSrv` to a named type defined once at the top of the function block and reference it in both call sites.

- [ ] Run `go test ./internal/plugins/...` → PASS.

- [ ] Commit:

  ```bash
  git add internal/plugins/manifest.go internal/plugins/manifest_test.go internal/config/lsp.go
  git commit -m "$(cat <<'EOF'
  feat(plugins): parse lsp.json (czcli-native + Claude Code shapes)

  ReadLSPServers reads .claude-plugin/lsp.json in both
  {"servers":{...}} and {"<lang>":{...,"extensionToLanguage":{...}}}
  shapes, derives Languages from extensionToLanguage when absent.

  Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
  EOF
  )"
  ```

---

### Task 5 — `.claude-plugin/hooks.json` parser → `[]HookEntry`

**Files:** `internal/plugins/manifest.go`, `internal/plugins/manifest_test.go`

- [ ] Append FAILING test:

```go
func TestReadHooksHappy(t *testing.T) {
	root := t.TempDir()
	dir := writePlugin(t, root, "p", `{"name":"p"}`)
	if err := os.MkdirAll(filepath.Join(dir, ".claude-plugin"), 0o755); err != nil {
		t.Fatal(err)
	}
	body := `{"hooks": {
		"PreToolUse": [
			{"matcher":"Bash","hooks":[{"type":"command","command":"/bin/sh -c \"policy.sh\"","timeout":7}]}
		],
		"Stop": [
			{"matcher":"*","hooks":[{"type":"command","command":"/bin/echo done"}]}
		],
		"NotificationEvent": [
			{"hooks":[{"type":"command","command":"/bin/true"}]}
		],
		"PostToolUse": [
			{"matcher":"Bash|Write","hooks":[{"type":"prompt","command":"unused"}]}
		]
	}}`
	if err := os.WriteFile(filepath.Join(dir, ".claude-plugin", "hooks.json"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	hs := ReadHooks(dir, "p")
	// 1 PreToolUse + 1 Stop = 2 (NotificationEvent dropped; PostToolUse "prompt" type dropped).
	if len(hs) != 2 {
		t.Fatalf("len = %d, want 2 (%+v)", len(hs), hs)
	}
	got := map[string]HookEntry{}
	for _, h := range hs {
		got[h.Event] = h
	}
	if pre := got["PreToolUse"]; pre.Matcher["tool"] != "Bash" || pre.TimeoutSeconds != 7 || pre.Source != "p" {
		t.Errorf("PreToolUse = %+v", pre)
	}
	if stop := got["Stop"]; len(stop.Matcher) != 0 || stop.TimeoutSeconds != 5 {
		t.Errorf("Stop = %+v (matcher should be empty for *, timeout default 5)", stop)
	}
	if len(got["PreToolUse"].Command) < 1 || got["PreToolUse"].Command[0] != "/bin/sh" {
		t.Errorf("PreToolUse.Command not shell-split: %+v", got["PreToolUse"].Command)
	}
}

func TestReadHooksMissing(t *testing.T) {
	root := t.TempDir()
	dir := writePlugin(t, root, "p", `{"name":"p"}`)
	if got := ReadHooks(dir, "p"); got != nil {
		t.Errorf("missing hooks.json should return nil, got %+v", got)
	}
}
```

- [ ] Run `go test ./internal/plugins/...` → FAIL.

- [ ] Minimal impl — append to `internal/plugins/manifest.go`:

```go
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
```

- [ ] Run `go test ./internal/plugins/...` → PASS.

- [ ] Commit:

  ```bash
  git add internal/plugins/manifest.go internal/plugins/manifest_test.go
  git commit -m "$(cat <<'EOF'
  feat(plugins): parse .claude-plugin/hooks.json into HookEntry slice

  Flattens Claude Code's {hooks:{Event:[{matcher,hooks:[...]}]}} into a
  flat HookEntry list. Whitelists UserPromptSubmit/PreToolUse/PostToolUse/
  Stop; drops non-"command" hook types with a warning. Expands "Bash|Write"
  to two entries; "*" matcher => empty matcher (match-all).

  Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
  EOF
  )"
  ```

---

### Task 6 — `commands/*.md` parser → `[]PluginCommand`

**Files:** `internal/plugins/manifest.go`, `internal/plugins/manifest_test.go`

- [ ] Append FAILING test:

```go
func TestReadCommandsHappy(t *testing.T) {
	root := t.TempDir()
	dir := writePlugin(t, root, "p", `{"name":"p"}`)
	cmdDir := filepath.Join(dir, "commands")
	if err := os.MkdirAll(cmdDir, 0o755); err != nil {
		t.Fatal(err)
	}
	deploy := `---
description: Deploy to prod
argument-hint: [env]
allowed-tools: Bash
---
Deploy $ARGUMENTS following our checklist.`
	if err := os.WriteFile(filepath.Join(cmdDir, "deploy.md"), []byte(deploy), 0o644); err != nil {
		t.Fatal(err)
	}
	bare := `Plain body, no frontmatter.`
	if err := os.WriteFile(filepath.Join(cmdDir, "bare.md"), []byte(bare), 0o644); err != nil {
		t.Fatal(err)
	}
	cmds := ReadCommands(dir, "p")
	if len(cmds) != 2 {
		t.Fatalf("len = %d, want 2 (%+v)", len(cmds), cmds)
	}
	byName := map[string]PluginCommand{cmds[0].Name: cmds[0], cmds[1].Name: cmds[1]}
	dep := byName["deploy"]
	if dep.Description != "Deploy to prod" || dep.Source != "p" {
		t.Errorf("deploy = %+v", dep)
	}
	if !strings.Contains(dep.Prompt, "Deploy $ARGUMENTS") {
		t.Errorf("deploy.Prompt missing body: %q", dep.Prompt)
	}
	if byName["bare"].Prompt != "Plain body, no frontmatter." {
		t.Errorf("bare body = %q", byName["bare"].Prompt)
	}
}

func TestReadCommandsMissingDir(t *testing.T) {
	root := t.TempDir()
	dir := writePlugin(t, root, "p", `{"name":"p"}`)
	if got := ReadCommands(dir, "p"); got != nil {
		t.Errorf("missing commands/ should return nil, got %+v", got)
	}
}
```

Add `"strings"` to the test file imports.

- [ ] Run `go test ./internal/plugins/...` → FAIL.

- [ ] Minimal impl — append to `internal/plugins/manifest.go`:

Add `"strings"` to the manifest.go imports, plus `"gopkg.in/yaml.v3"`.

```go
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
func splitFrontmatter(data []byte) (yaml, body []byte) {
	const sep = "---\n"
	if !strings.HasPrefix(string(data), sep) {
		return nil, data
	}
	rest := data[len(sep):]
	end := strings.Index(string(rest), "\n"+sep)
	if end < 0 {
		// Try "\n---" at end (no trailing newline).
		end = strings.Index(string(rest), "\n---")
		if end < 0 {
			return nil, data
		}
	}
	fm := rest[:end]
	tail := rest[end:]
	// Skip the newline + closing "---" + optional newline.
	tail = bytes.TrimPrefix(tail, []byte("\n---"))
	tail = bytes.TrimPrefix(tail, []byte("\n"))
	return fm, tail
}
```

Add `"bytes"` to manifest.go imports.

- [ ] Run `go test ./internal/plugins/...` → PASS.

- [ ] Commit:

  ```bash
  git add internal/plugins/manifest.go internal/plugins/manifest_test.go
  git commit -m "$(cat <<'EOF'
  feat(plugins): parse commands/*.md slash commands with YAML frontmatter

  ReadCommands extracts description + argument-hint frontmatter and the
  markdown body (with $ARGUMENTS still present, expanded at invocation
  time by the /plugin dispatcher).

  Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
  EOF
  )"
  ```

---

### Task 7 — State file: atomic load/save `~/.czcli/plugins.json`

**Files:** `internal/plugins/state.go`, `internal/plugins/state_test.go`

- [ ] Write FAILING test `internal/plugins/state_test.go`:

```go
package plugins

import (
	"os"
	"path/filepath"
	"testing"
)

func TestStateRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "plugins.json")
	in := state{
		"foo": {Enabled: true, Source: "https://github.com/x/foo"},
		"bar": {Enabled: false, Source: "local"},
	}
	if err := writeState(path, in); err != nil {
		t.Fatalf("writeState: %v", err)
	}
	out, err := readState(path)
	if err != nil {
		t.Fatalf("readState: %v", err)
	}
	if !out["foo"].Enabled || out["foo"].Source != "https://github.com/x/foo" {
		t.Errorf("foo round-trip = %+v", out["foo"])
	}
	if out["bar"].Enabled {
		t.Errorf("bar should be disabled, got %+v", out["bar"])
	}
}

func TestStateAtomicWriteOverwrites(t *testing.T) {
	path := filepath.Join(t.TempDir(), "plugins.json")
	if err := writeState(path, state{"a": {Enabled: true, Source: "s"}}); err != nil {
		t.Fatal(err)
	}
	if err := writeState(path, state{"b": {Enabled: false, Source: "t"}}); err != nil {
		t.Fatal(err)
	}
	out, _ := readState(path)
	if _, has := out["a"]; has {
		t.Error("second writeState should fully overwrite")
	}
	if out["b"].Enabled {
		t.Error("b should be disabled")
	}
}

func TestStateMissingFile(t *testing.T) {
	out, err := readState(filepath.Join(t.TempDir(), "nope.json"))
	if err != nil {
		t.Fatalf("missing file should be nil error, got %v", err)
	}
	if len(out) != 0 {
		t.Errorf("missing file should yield empty state, got %+v", out)
	}
}

func TestStateCorruptionTolerated(t *testing.T) {
	path := filepath.Join(t.TempDir(), "plugins.json")
	if err := os.WriteFile(path, []byte(`{not json`), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := readState(path)
	if err != nil {
		t.Fatalf("corrupt file should be nil error (log + empty fallback), got %v", err)
	}
	if len(out) != 0 {
		t.Errorf("corrupt file should yield empty state, got %+v", out)
	}
}
```

- [ ] Run `go test ./internal/plugins/...` → FAIL.

- [ ] Minimal impl `internal/plugins/state.go`:

```go
package plugins

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
)

// stateEntry is one record in ~/.czcli/plugins.json.
type stateEntry struct {
	Enabled bool   `json:"enabled"`
	Source  string `json:"source"`
}

// state is the on-disk map: plugin name -> stateEntry.
type state map[string]stateEntry

// readState returns the parsed state. Missing file or corrupt JSON => empty
// state + slog.Warn (corruption never blocks discovery; users keep working).
func readState(path string) (state, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return state{}, nil
		}
		return nil, fmt.Errorf("read state %s: %w", path, err)
	}
	out := state{}
	if err := json.Unmarshal(data, &out); err != nil {
		slog.Warn("plugins: state file corrupt; falling back to empty", "path", path, "error", err)
		return state{}, nil
	}
	return out, nil
}

// writeState serializes the state to a temp file in the same dir and renames
// it over the target for atomicity. Creates parent dirs as needed.
func writeState(path string, s state) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("mkdir for state: %w", err)
	}
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal state: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".plugins.*.json")
	if err != nil {
		return fmt.Errorf("temp state file: %w", err)
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return fmt.Errorf("write temp state: %w", err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("close temp state: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("rename state: %w", err)
	}
	return nil
}
```

- [ ] Run `go test ./internal/plugins/...` → PASS.

- [ ] Commit:

  ```bash
  git add internal/plugins/state.go internal/plugins/state_test.go
  git commit -m "$(cat <<'EOF'
  feat(plugins): atomic state file ~/.czcli/plugins.json

  readState/writeState with temp+rename atomicity. Corrupt JSON degrades
  to an empty state + slog.Warn; missing file is not an error.

  Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
  EOF
  )"
  ```

---

### Task 8 — `Manager` + `New` + `Load`

**Files:** `internal/plugins/manager.go`, `internal/plugins/manager_test.go`

- [ ] Write FAILING test `internal/plugins/manager_test.go`:

```go
package plugins

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/caxqueiroz/czcli/internal/config"
)

func writeFullPlugin(t *testing.T, root, name string) {
	t.Helper()
	dir := filepath.Join(root, name)
	if err := os.MkdirAll(filepath.Join(dir, ".claude-plugin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "commands"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "skills", "demo"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "agents"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".claude-plugin", "plugin.json"),
		[]byte(`{"name":"`+name+`","version":"0.1.0","description":"d"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "commands", "hi.md"),
		[]byte("---\ndescription: say hi\n---\nHi $ARGUMENTS"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".mcp.json"),
		[]byte(`{"mcpServers":{"x":{"command":"./x"}}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "skills", "demo", "SKILL.md"),
		[]byte("---\ndescription: d\n---\nbody"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestManagerLoadAggregates(t *testing.T) {
	dirA := t.TempDir()
	dirB := t.TempDir()
	writeFullPlugin(t, dirA, "alpha")
	writeFullPlugin(t, dirB, "beta")
	state := filepath.Join(t.TempDir(), "plugins.json")
	m := New(config.PluginsConfig{Enabled: true, Dirs: []string{dirA, dirB}}, state, nil)

	contrib, infos, err := m.Load(context.Background())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(infos) != 2 {
		t.Fatalf("infos = %d, want 2 (%+v)", len(infos), infos)
	}
	if len(contrib.SkillDirs) != 2 || len(contrib.AgentDirs) != 2 {
		t.Errorf("SkillDirs/AgentDirs not aggregated: %+v", contrib)
	}
	if len(contrib.MCPServers) != 2 {
		t.Errorf("MCPServers = %+v", contrib.MCPServers)
	}
	if len(contrib.Commands) != 2 {
		t.Errorf("Commands = %+v", contrib.Commands)
	}
}

func TestManagerLoadDisabledDropped(t *testing.T) {
	dir := t.TempDir()
	writeFullPlugin(t, dir, "alpha")
	state := filepath.Join(t.TempDir(), "plugins.json")
	if err := writeState(state, map[string]stateEntry{"alpha": {Enabled: false, Source: "local"}}); err != nil {
		t.Fatal(err)
	}
	m := New(config.PluginsConfig{Enabled: true, Dirs: []string{dir}}, state, nil)
	contrib, infos, _ := m.Load(context.Background())
	if len(contrib.SkillDirs) != 0 || len(contrib.Commands) != 0 {
		t.Errorf("disabled plugin should contribute nothing: %+v", contrib)
	}
	if len(infos) != 1 || infos[0].Enabled {
		t.Errorf("info should still appear but disabled: %+v", infos)
	}
}

func TestManagerLoadSkipsDirsWithoutManifest(t *testing.T) {
	dir := t.TempDir()
	// Random non-plugin directory.
	if err := os.MkdirAll(filepath.Join(dir, "junk"), 0o755); err != nil {
		t.Fatal(err)
	}
	state := filepath.Join(t.TempDir(), "plugins.json")
	m := New(config.PluginsConfig{Enabled: true, Dirs: []string{dir}}, state, nil)
	contrib, infos, err := m.Load(context.Background())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(infos) != 0 || len(contrib.Commands) != 0 {
		t.Errorf("no-manifest dir should be skipped: infos=%+v contrib=%+v", infos, contrib)
	}
}

func TestManagerLoadDisabledViaConfig(t *testing.T) {
	dir := t.TempDir()
	writeFullPlugin(t, dir, "alpha")
	state := filepath.Join(t.TempDir(), "plugins.json")
	m := New(config.PluginsConfig{Enabled: false, Dirs: []string{dir}}, state, nil)
	contrib, infos, _ := m.Load(context.Background())
	if len(infos) != 0 || len(contrib.SkillDirs) != 0 {
		t.Errorf("disabled subsystem should return empty: infos=%+v contrib=%+v", infos, contrib)
	}
}
```

- [ ] Run `go test ./internal/plugins/...` → FAIL.

- [ ] Minimal impl `internal/plugins/manager.go`:

```go
package plugins

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"sync"

	"github.com/caxqueiroz/czcli/internal/config"
)

// Contributions is the flat aggregate of every enabled plugin's contributed
// extension points. The agent rebuild path consumes this directly.
type Contributions struct {
	SkillDirs  []string
	AgentDirs  []string
	MCPServers []config.MCPServerConfig
	LSPServers []config.LSPServerConfig
	Hooks      []HookEntry
	Commands   []PluginCommand
}

// PluginInfo is a discovered plugin's metadata (used by /plugin list and
// channel.Status). Counts are filled at Load time so /plugin list can show
// "skills 3 / mcp 1 / hooks 0".
type PluginInfo struct {
	Name    string
	Version string
	Source  string
	Dir     string
	Enabled bool
	Counts  struct {
		Skills, Agents, MCP, LSP, Hooks, Commands int
	}
}

// CloneFunc is the seam used by Install. Production wires
// `exec.CommandContext(ctx, "git", "clone", "--depth=1", url, dest).Run()`;
// tests pass a fake.
type CloneFunc func(ctx context.Context, gitURL, dest string) error

// Manager owns plugin discovery, state, and mutations. Safe for concurrent
// use: every mutation holds a single mutex; Load takes a read lock against
// concurrent mutations only (the file scan itself does not need locking).
type Manager struct {
	cfg       config.PluginsConfig
	stateFile string
	clone     CloneFunc

	mu sync.Mutex
}

// New constructs a Manager. stateFile is typically `~/.czcli/plugins.json`
// (caller resolves ~/ — config.expandHome is internal). clone may be nil
// if Install will never be called.
func New(cfg config.PluginsConfig, stateFile string, clone CloneFunc) *Manager {
	return &Manager{cfg: cfg, stateFile: stateFile, clone: clone}
}

// Load discovers, parses, and aggregates all plugins. Per-plugin errors are
// logged and never fail the whole Load. Returns a deterministic ordering
// (plugins sorted by name across all dirs combined).
func (m *Manager) Load(_ context.Context) (Contributions, []PluginInfo, error) {
	if !m.cfg.Enabled {
		return Contributions{}, nil, nil
	}
	m.mu.Lock()
	st, _ := readState(m.stateFile)
	m.mu.Unlock()

	type discovered struct {
		dir, name string
	}
	var found []discovered
	for _, root := range m.cfg.Dirs {
		entries, err := os.ReadDir(root)
		if err != nil {
			if !os.IsNotExist(err) {
				slog.Warn("plugins: read dir", "dir", root, "error", err)
			}
			continue
		}
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			pdir := filepath.Join(root, e.Name())
			if _, err := os.Stat(filepath.Join(pdir, ".claude-plugin", "plugin.json")); err != nil {
				continue
			}
			found = append(found, discovered{dir: pdir, name: e.Name()})
		}
	}
	sort.Slice(found, func(i, j int) bool { return found[i].name < found[j].name })

	var contrib Contributions
	var infos []PluginInfo
	seen := make(map[string]bool, len(found))
	for _, d := range found {
		if seen[d.name] {
			slog.Warn("plugins: duplicate plugin name across dirs; keeping first", "name", d.name, "dir", d.dir)
			continue
		}
		seen[d.name] = true

		manifest, err := ReadManifest(d.dir)
		if err != nil {
			slog.Warn("plugins: read manifest", "dir", d.dir, "error", err)
			continue
		}
		// State: explicit disabled wins; absent => enabled.
		enabled := true
		source := "local"
		if entry, ok := st[manifest.Name]; ok {
			enabled = entry.Enabled
			if entry.Source != "" {
				source = entry.Source
			}
		}

		info := PluginInfo{
			Name:    manifest.Name,
			Version: manifest.Version,
			Source:  source,
			Dir:     d.dir,
			Enabled: enabled,
		}

		if !enabled {
			infos = append(infos, info)
			continue
		}

		mcps := ReadMCPServers(d.dir, manifest.Name)
		lsps := ReadLSPServers(d.dir, manifest.Name)
		hooks := ReadHooks(d.dir, manifest.Name)
		cmds := ReadCommands(d.dir, manifest.Name)

		skillsDir := filepath.Join(d.dir, "skills")
		agentsDir := filepath.Join(d.dir, "agents")
		skillCount, agentCount := 0, 0
		if entries, err := os.ReadDir(skillsDir); err == nil {
			for _, e := range entries {
				if e.IsDir() {
					skillCount++
				}
			}
			contrib.SkillDirs = append(contrib.SkillDirs, skillsDir)
		}
		if entries, err := os.ReadDir(agentsDir); err == nil {
			for _, e := range entries {
				if !e.IsDir() && filepath.Ext(e.Name()) == ".md" {
					agentCount++
				}
			}
			contrib.AgentDirs = append(contrib.AgentDirs, agentsDir)
		}
		contrib.MCPServers = append(contrib.MCPServers, mcps...)
		contrib.LSPServers = append(contrib.LSPServers, lsps...)
		contrib.Hooks = append(contrib.Hooks, hooks...)
		contrib.Commands = append(contrib.Commands, cmds...)

		info.Counts.Skills = skillCount
		info.Counts.Agents = agentCount
		info.Counts.MCP = len(mcps)
		info.Counts.LSP = len(lsps)
		info.Counts.Hooks = len(hooks)
		info.Counts.Commands = len(cmds)
		infos = append(infos, info)
	}
	return contrib, infos, nil
}

// targetDir is the path the manager will use to install a new plugin under
// the first writable user-scope dir (the first entry in cfg.Dirs).
func (m *Manager) targetDir(name string) (string, error) {
	if len(m.cfg.Dirs) == 0 {
		return "", fmt.Errorf("plugins: no dirs configured")
	}
	return filepath.Join(m.cfg.Dirs[0], name), nil
}
```

- [ ] Run `go test ./internal/plugins/...` → PASS.

- [ ] Commit:

  ```bash
  git add internal/plugins/manager.go internal/plugins/manager_test.go
  git commit -m "$(cat <<'EOF'
  feat(plugins): Manager.Load discovers + aggregates Contributions

  Walks cfg.Plugins.Dirs, parses every plugin's manifests, applies the
  enable/disable state, and returns a flat Contributions + []PluginInfo.
  Per-plugin errors logged; never fails the whole load.

  Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
  EOF
  )"
  ```

---

### Task 9 — `Manager.Install` / `Enable` / `Disable` / `Remove`

**Files:** `internal/plugins/manager.go`, `internal/plugins/manager_test.go`

- [ ] Append FAILING tests:

```go
func TestManagerInstallCallsCloneAndEnables(t *testing.T) {
	root := t.TempDir()
	state := filepath.Join(t.TempDir(), "plugins.json")
	cloned := ""
	clone := func(_ context.Context, url, dest string) error {
		cloned = url
		if err := os.MkdirAll(filepath.Join(dest, ".claude-plugin"), 0o755); err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(dest, ".claude-plugin", "plugin.json"),
			[]byte(`{"name":"gamma","version":"0.0.1"}`), 0o644)
	}
	m := New(config.PluginsConfig{Enabled: true, Dirs: []string{root}}, state, clone)
	info, err := m.Install(context.Background(), "https://github.com/x/gamma", "gamma")
	if err != nil {
		t.Fatalf("Install: %v", err)
	}
	if cloned != "https://github.com/x/gamma" {
		t.Errorf("clone URL = %q", cloned)
	}
	if info.Name != "gamma" || !info.Enabled || info.Source != "https://github.com/x/gamma" {
		t.Errorf("info = %+v", info)
	}
	// State file should record it as enabled.
	st, _ := readState(state)
	if !st["gamma"].Enabled || st["gamma"].Source != "https://github.com/x/gamma" {
		t.Errorf("state = %+v", st["gamma"])
	}
}

func TestManagerInstallRefusesExisting(t *testing.T) {
	root := t.TempDir()
	writeFullPlugin(t, root, "alpha")
	state := filepath.Join(t.TempDir(), "plugins.json")
	clone := func(context.Context, string, string) error { return nil }
	m := New(config.PluginsConfig{Enabled: true, Dirs: []string{root}}, state, clone)
	if _, err := m.Install(context.Background(), "git://x", "alpha"); err == nil {
		t.Fatal("expected error when target dir already exists")
	}
}

func TestManagerEnableDisable(t *testing.T) {
	root := t.TempDir()
	writeFullPlugin(t, root, "alpha")
	state := filepath.Join(t.TempDir(), "plugins.json")
	m := New(config.PluginsConfig{Enabled: true, Dirs: []string{root}}, state, nil)
	if err := m.Disable("alpha"); err != nil {
		t.Fatalf("Disable: %v", err)
	}
	st, _ := readState(state)
	if st["alpha"].Enabled {
		t.Error("Disable did not persist")
	}
	if err := m.Enable("alpha"); err != nil {
		t.Fatalf("Enable: %v", err)
	}
	st, _ = readState(state)
	if !st["alpha"].Enabled {
		t.Error("Enable did not persist")
	}
}

func TestManagerRemoveDeletesAndForgetsState(t *testing.T) {
	root := t.TempDir()
	writeFullPlugin(t, root, "alpha")
	state := filepath.Join(t.TempDir(), "plugins.json")
	m := New(config.PluginsConfig{Enabled: true, Dirs: []string{root}}, state, nil)
	if err := m.Disable("alpha"); err != nil {
		t.Fatal(err)
	}
	if err := m.Remove("alpha"); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "alpha")); !os.IsNotExist(err) {
		t.Errorf("plugin dir not removed: %v", err)
	}
	st, _ := readState(state)
	if _, has := st["alpha"]; has {
		t.Error("state entry not cleared")
	}
}

func TestManagerRemoveUnknown(t *testing.T) {
	root := t.TempDir()
	state := filepath.Join(t.TempDir(), "plugins.json")
	m := New(config.PluginsConfig{Enabled: true, Dirs: []string{root}}, state, nil)
	if err := m.Remove("ghost"); err == nil {
		t.Fatal("Remove of unknown plugin should error")
	}
}
```

- [ ] Run `go test ./internal/plugins/...` → FAIL.

- [ ] Minimal impl — append to `internal/plugins/manager.go`:

```go
// Install clones gitURL into m.cfg.Dirs[0]/<name>, parses its manifest, and
// records {enabled:true, source:gitURL} in the state file. Returns the
// resulting PluginInfo. Fails if the target directory already exists, the
// clone fails, or the cloned tree has no valid manifest.
func (m *Manager) Install(ctx context.Context, gitURL, name string) (PluginInfo, error) {
	if m.clone == nil {
		return PluginInfo{}, fmt.Errorf("plugins: install requires a CloneFunc")
	}
	if name == "" {
		return PluginInfo{}, fmt.Errorf("plugins: install requires a plugin name")
	}
	m.mu.Lock()
	defer m.mu.Unlock()

	dest, err := m.targetDir(name)
	if err != nil {
		return PluginInfo{}, err
	}
	if _, err := os.Stat(dest); err == nil {
		return PluginInfo{}, fmt.Errorf("plugins: target %s already exists", dest)
	} else if !os.IsNotExist(err) {
		return PluginInfo{}, fmt.Errorf("plugins: stat target %s: %w", dest, err)
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return PluginInfo{}, fmt.Errorf("plugins: mkdir parent: %w", err)
	}
	if err := m.clone(ctx, gitURL, dest); err != nil {
		_ = os.RemoveAll(dest)
		return PluginInfo{}, fmt.Errorf("plugins: clone %s: %w", gitURL, err)
	}
	manifest, err := ReadManifest(dest)
	if err != nil {
		_ = os.RemoveAll(dest)
		return PluginInfo{}, fmt.Errorf("plugins: validate cloned manifest: %w", err)
	}

	st, _ := readState(m.stateFile)
	st[manifest.Name] = stateEntry{Enabled: true, Source: gitURL}
	if err := writeState(m.stateFile, st); err != nil {
		return PluginInfo{}, fmt.Errorf("plugins: write state: %w", err)
	}
	return PluginInfo{
		Name: manifest.Name, Version: manifest.Version,
		Source: gitURL, Dir: dest, Enabled: true,
	}, nil
}

// Enable flips the state for name to enabled. Idempotent: enabling an
// already-enabled plugin is not an error. Does NOT require the plugin to be
// discovered (caller can enable a plugin that's about to be added).
func (m *Manager) Enable(name string) error {
	return m.setEnabled(name, true)
}

// Disable flips the state for name to disabled. Idempotent.
func (m *Manager) Disable(name string) error {
	return m.setEnabled(name, false)
}

func (m *Manager) setEnabled(name string, enabled bool) error {
	if name == "" {
		return fmt.Errorf("plugins: name required")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	st, _ := readState(m.stateFile)
	entry := st[name]
	entry.Enabled = enabled
	st[name] = entry
	return writeState(m.stateFile, st)
}

// Remove deletes the plugin directory and clears its state entry. Errors if
// the plugin is not found in any cfg.Dirs.
func (m *Manager) Remove(name string) error {
	if name == "" {
		return fmt.Errorf("plugins: name required")
	}
	m.mu.Lock()
	defer m.mu.Unlock()

	var dir string
	for _, root := range m.cfg.Dirs {
		candidate := filepath.Join(root, name)
		if _, err := os.Stat(candidate); err == nil {
			dir = candidate
			break
		}
	}
	if dir == "" {
		return fmt.Errorf("plugins: %q not found in any plugins dir", name)
	}
	if err := os.RemoveAll(dir); err != nil {
		return fmt.Errorf("plugins: remove %s: %w", dir, err)
	}
	st, _ := readState(m.stateFile)
	delete(st, name)
	return writeState(m.stateFile, st)
}
```

- [ ] Run `go test ./internal/plugins/...` → PASS.

- [ ] Commit:

  ```bash
  git add internal/plugins/manager.go internal/plugins/manager_test.go
  git commit -m "$(cat <<'EOF'
  feat(plugins): Install/Enable/Disable/Remove mutations

  Install calls the injected CloneFunc, validates the cloned manifest,
  and persists {enabled:true, source} in plugins.json. Enable/Disable
  are idempotent state flips; Remove deletes the dir and forgets state.

  Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
  EOF
  )"
  ```

---

### Task 10 — CLI: `pluginBackend` interface + `WithPlugins` option

**Files:** `internal/channel/cli/model.go`, `internal/channel/cli/cli.go`

- [ ] Edit `internal/channel/cli/model.go` — add the interface and `model` field. Insert directly after the existing `scheduleBackend` interface declaration:

```go
// pluginBackend is the surface the /plugin command drives. It mirrors
// scheduleBackend's split: the CLI depends only on this minimal contract;
// cmd/czcli wires a real plugins.Manager adapter. Every mutation triggers
// Rebuild so the agent picks up new contributions on the next turn.
type pluginBackend interface {
	List(ctx context.Context) ([]PluginListItem, error)
	Install(ctx context.Context, gitURL, name string) error
	Enable(ctx context.Context, name string) error
	Disable(ctx context.Context, name string) error
	Remove(ctx context.Context, name string) error
	Rebuild(ctx context.Context) error
}

// PluginListItem is the projection of plugins.PluginInfo the CLI renders. It
// lives in the cli package so internal/plugins is not imported here (mirror
// of the scheduleBackend pattern, which keeps cli package-clean of plugins).
type PluginListItem struct {
	Name        string
	Version     string
	Source      string
	Enabled     bool
	SkillCount  int
	MCPCount    int
	LSPCount    int
	HookCount   int
	CmdCount    int
	AgentCount  int
}
```

- [ ] Add a `plugins pluginBackend` field on `model` (next to `sched scheduleBackend`):

```go
	plugins pluginBackend // optional; nil when /plugin is not wired
```

- [ ] Edit `internal/channel/cli/cli.go` — add the option and wiring. After `WithScheduler`, insert:

```go
// WithPlugins wires the /plugin (list|install|enable|disable|remove) backend.
// When unset, /plugin reports that plugins are not available.
func WithPlugins(b pluginBackend) Option {
	return func(c *CLI) { c.plugins = b }
}
```

Add the field `plugins pluginBackend` to the `CLI` struct (next to `sched`), and copy it to `m.plugins = c.plugins` in `Start` (next to `m.sched = c.sched`).

- [ ] No test yet — the wiring is exercised by Task 11's command tests. Commit:

  ```bash
  git add internal/channel/cli/model.go internal/channel/cli/cli.go
  git commit -m "$(cat <<'EOF'
  feat(cli): pluginBackend interface + WithPlugins option

  Mirrors the scheduleBackend / WithScheduler pattern: the CLI depends on
  a minimal interface; cmd/czcli adapts plugins.Manager to satisfy it.

  Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
  EOF
  )"
  ```

---

### Task 11 — CLI: `/plugin` command dispatcher + subcommands

**Files:** `internal/channel/cli/commands.go`, `internal/channel/cli/plugin_test.go`

- [ ] Write FAILING test `internal/channel/cli/plugin_test.go`:

```go
package cli

import (
	"context"
	"errors"
	"strings"
	"testing"
)

type fakePlugins struct {
	items        map[string]PluginListItem
	installErr   error
	installedURL string
	rebuilds     int
	listErr      error
}

func newFakePlugins() *fakePlugins {
	return &fakePlugins{items: map[string]PluginListItem{}}
}

func (f *fakePlugins) List(context.Context) ([]PluginListItem, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	out := make([]PluginListItem, 0, len(f.items))
	for _, p := range f.items {
		out = append(out, p)
	}
	return out, nil
}

func (f *fakePlugins) Install(_ context.Context, gitURL, name string) error {
	if f.installErr != nil {
		return f.installErr
	}
	f.installedURL = gitURL
	if name == "" {
		name = "from-git"
	}
	f.items[name] = PluginListItem{Name: name, Source: gitURL, Enabled: true}
	return nil
}

func (f *fakePlugins) Enable(_ context.Context, name string) error {
	p, ok := f.items[name]
	if !ok {
		return errors.New("not found")
	}
	p.Enabled = true
	f.items[name] = p
	return nil
}

func (f *fakePlugins) Disable(_ context.Context, name string) error {
	p, ok := f.items[name]
	if !ok {
		return errors.New("not found")
	}
	p.Enabled = false
	f.items[name] = p
	return nil
}

func (f *fakePlugins) Remove(_ context.Context, name string) error {
	if _, ok := f.items[name]; !ok {
		return errors.New("not found")
	}
	delete(f.items, name)
	return nil
}

func (f *fakePlugins) Rebuild(context.Context) error {
	f.rebuilds++
	return nil
}

func TestPluginNoBackend(t *testing.T) {
	m := newModel(80, 24)
	out := m.cmdPlugin("list")
	if !strings.Contains(out, "not available") {
		t.Errorf("expected unavailable, got %q", out)
	}
}

func TestPluginListEmpty(t *testing.T) {
	m := newModel(80, 24)
	m.plugins = newFakePlugins()
	out := m.cmdPlugin("list")
	if !strings.Contains(out, "no plugins") {
		t.Errorf("got %q", out)
	}
}

func TestPluginListRenders(t *testing.T) {
	fk := newFakePlugins()
	fk.items["foo"] = PluginListItem{Name: "foo", Version: "1.0", Source: "git://x", Enabled: true, SkillCount: 2, MCPCount: 1}
	fk.items["bar"] = PluginListItem{Name: "bar", Version: "0.1", Source: "local", Enabled: false}
	m := newModel(80, 24)
	m.plugins = fk
	out := m.cmdPlugin("list")
	for _, want := range []string{"foo", "1.0", "git://x", "on", "bar", "off"} {
		if !strings.Contains(out, want) {
			t.Errorf("list missing %q in:\n%s", want, out)
		}
	}
}

func TestPluginInstallTriggersRebuild(t *testing.T) {
	fk := newFakePlugins()
	m := newModel(80, 24)
	m.plugins = fk
	out := m.cmdPlugin("install https://github.com/x/foo foo")
	if !strings.Contains(out, "installed") {
		t.Errorf("install output = %q", out)
	}
	if fk.installedURL != "https://github.com/x/foo" {
		t.Errorf("install URL = %q", fk.installedURL)
	}
	if fk.rebuilds != 1 {
		t.Errorf("rebuilds after install = %d, want 1", fk.rebuilds)
	}
}

func TestPluginEnableDisableRemoveRebuild(t *testing.T) {
	fk := newFakePlugins()
	fk.items["foo"] = PluginListItem{Name: "foo", Enabled: false}
	m := newModel(80, 24)
	m.plugins = fk

	m.cmdPlugin("enable foo")
	if !fk.items["foo"].Enabled {
		t.Error("enable did not flip")
	}
	if fk.rebuilds != 1 {
		t.Errorf("rebuilds after enable = %d", fk.rebuilds)
	}

	m.cmdPlugin("disable foo")
	if fk.items["foo"].Enabled {
		t.Error("disable did not flip")
	}
	if fk.rebuilds != 2 {
		t.Errorf("rebuilds after disable = %d", fk.rebuilds)
	}

	out := m.cmdPlugin("remove foo")
	if _, has := fk.items["foo"]; has {
		t.Error("remove did not delete")
	}
	if fk.rebuilds != 3 {
		t.Errorf("rebuilds after remove = %d", fk.rebuilds)
	}
	if !strings.Contains(out, "removed") {
		t.Errorf("remove output = %q", out)
	}
}

func TestPluginUsage(t *testing.T) {
	fk := newFakePlugins()
	m := newModel(80, 24)
	m.plugins = fk
	for _, in := range []string{"", "bogus", "install"} {
		out := m.cmdPlugin(in)
		if !strings.Contains(out, "usage") {
			t.Errorf("cmdPlugin(%q) should show usage, got %q", in, out)
		}
	}
}

func TestPluginInstallError(t *testing.T) {
	fk := newFakePlugins()
	fk.installErr = errors.New("clone failed")
	m := newModel(80, 24)
	m.plugins = fk
	out := m.cmdPlugin("install https://x/y z")
	if !strings.Contains(out, "install failed") {
		t.Errorf("got %q", out)
	}
	if fk.rebuilds != 0 {
		t.Errorf("no rebuild on failed install, got %d", fk.rebuilds)
	}
}
```

- [ ] Run `go test ./internal/channel/cli/...` → FAIL (cmdPlugin undefined).

- [ ] Minimal impl — edit `internal/channel/cli/commands.go`:

  a) Add `"plugin"` to the switch in `handleCommand` (right next to the `"schedule"` case):

  ```go
  	case "plugin":
  		return m.cmdPlugin(args), false
  ```

  Also update the unknown-command help text to include `/plugin`.

  b) Append at end of file:

  ```go
  const pluginUsage = "usage: /plugin <list | install <git-url> [name] | enable <name> | disable <name> | remove <name>>"

  // cmdPlugin drives the injected pluginBackend. Mutations (install/enable/
  // disable/remove) trigger Rebuild so the running agent picks up the new
  // Contributions on the next turn. List is a pure query.
  func (m model) cmdPlugin(args string) string {
  	if m.plugins == nil {
  		return "plugin: not available (plugins backend not wired); set plugins.enabled: true and configure plugins.dirs in config.yaml"
  	}
  	fields := tokenizeArgs(args)
  	if len(fields) == 0 {
  		return pluginUsage
  	}
  	ctx := context.Background()
  	sub, rest := fields[0], fields[1:]

  	switch sub {
  	case "list", "ls":
  		return m.pluginList(ctx)
  	case "install", "add":
  		return m.pluginInstall(ctx, rest)
  	case "enable":
  		return m.pluginSetEnabled(ctx, rest, true)
  	case "disable":
  		return m.pluginSetEnabled(ctx, rest, false)
  	case "remove", "rm", "uninstall":
  		return m.pluginRemove(ctx, rest)
  	default:
  		return pluginUsage
  	}
  }

  func (m model) pluginList(ctx context.Context) string {
  	items, err := m.plugins.List(ctx)
  	if err != nil {
  		return fmt.Sprintf("plugin list failed: %v", err)
  	}
  	if len(items) == 0 {
  		return "no plugins installed (try /plugin install <git-url> [name])"
  	}
  	sort.Slice(items, func(i, j int) bool { return items[i].Name < items[j].Name })
  	var b strings.Builder
  	fmt.Fprintf(&b, "plugins (%d):", len(items))
  	for _, p := range items {
  		state := "off"
  		if p.Enabled {
  			state = "on"
  		}
  		ver := p.Version
  		if ver == "" {
  			ver = "?"
  		}
  		src := p.Source
  		if src == "" {
  			src = "local"
  		}
  		fmt.Fprintf(&b, "\n  %-16s %-8s [%s] %s  (skills %d · mcp %d · lsp %d · hooks %d · cmds %d)",
  			p.Name, ver, state, src, p.SkillCount, p.MCPCount, p.LSPCount, p.HookCount, p.CmdCount)
  	}
  	return b.String()
  }

  func (m model) pluginInstall(ctx context.Context, args []string) string {
  	if len(args) < 1 {
  		return pluginUsage
  	}
  	gitURL := args[0]
  	name := ""
  	if len(args) >= 2 {
  		name = args[1]
  	} else {
  		name = inferPluginName(gitURL)
  	}
  	if err := m.plugins.Install(ctx, gitURL, name); err != nil {
  		return fmt.Sprintf("plugin install failed: %v", err)
  	}
  	if err := m.plugins.Rebuild(ctx); err != nil {
  		return fmt.Sprintf("plugin %q installed but rebuild failed: %v", name, err)
  	}
  	return fmt.Sprintf("plugin %q installed from %s", name, gitURL)
  }

  func (m model) pluginSetEnabled(ctx context.Context, args []string, enabled bool) string {
  	if len(args) < 1 {
  		return pluginUsage
  	}
  	name := args[0]
  	var err error
  	if enabled {
  		err = m.plugins.Enable(ctx, name)
  	} else {
  		err = m.plugins.Disable(ctx, name)
  	}
  	if err != nil {
  		return fmt.Sprintf("plugin update failed: %v", err)
  	}
  	if err := m.plugins.Rebuild(ctx); err != nil {
  		return fmt.Sprintf("plugin %q updated but rebuild failed: %v", name, err)
  	}
  	verb := "enabled"
  	if !enabled {
  		verb = "disabled"
  	}
  	return fmt.Sprintf("plugin %q %s", name, verb)
  }

  func (m model) pluginRemove(ctx context.Context, args []string) string {
  	if len(args) < 1 {
  		return pluginUsage
  	}
  	name := args[0]
  	if err := m.plugins.Remove(ctx, name); err != nil {
  		return fmt.Sprintf("plugin remove failed: %v", err)
  	}
  	if err := m.plugins.Rebuild(ctx); err != nil {
  		return fmt.Sprintf("plugin %q removed but rebuild failed: %v", name, err)
  	}
  	return fmt.Sprintf("plugin %q removed", name)
  }

  // inferPluginName extracts a sensible default from a git URL: the last path
  // segment minus a trailing ".git".
  func inferPluginName(gitURL string) string {
  	s := gitURL
  	if i := strings.LastIndex(s, "/"); i >= 0 {
  		s = s[i+1:]
  	}
  	s = strings.TrimSuffix(s, ".git")
  	if s == "" {
  		return "plugin"
  	}
  	return s
  }
  ```

- [ ] Run `go test ./internal/channel/cli/...` → PASS.

- [ ] Commit:

  ```bash
  git add internal/channel/cli/commands.go internal/channel/cli/plugin_test.go
  git commit -m "$(cat <<'EOF'
  feat(cli): /plugin (list|install|enable|disable|remove) command

  Drives the injected pluginBackend, calling Rebuild after every mutation
  so the agent reloads contributions on the next turn. Mirrors the
  /schedule + scheduleBackend pattern from Plan 5.

  Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
  EOF
  )"
  ```

---

### Task 12 — Wire `cmd/czcli/main.go`: Manager + backend adapter + Rebuild callback

**Files:** `cmd/czcli/main.go`

- [ ] Edit `cmd/czcli/main.go`. After the existing `assistant, err := agent.Build(...)` block and before the scheduler seeding, insert:

```go
	// Plugins: discover Claude Code-compatible plugin bundles. Drop-folder
	// roots come from cfg.Plugins.Dirs (defaults: ~/.czcli/plugins,
	// .czcli/plugins). State file lives at ~/.czcli/plugins.json. Per-plugin
	// parse errors are logged + skipped; a broken plugin never blocks startup.
	home, _ := os.UserHomeDir()
	pluginsState := home + "/.czcli/plugins.json"
	pluginsMgr := plugins.New(cfg.Plugins, pluginsState, gitClone)
	if _, _, err := pluginsMgr.Load(ctx); err != nil {
		slog.Warn("plugins: initial load", "error", err)
	}
```

- [ ] After the scheduler is started, build the CLI with `WithPlugins`:

```go
	ch := cli.New(
		cli.WithSessionID("cli"),
		cli.WithScheduler(scheduleAdapter{store: store, sched: sched}),
		cli.WithPlugins(pluginAdapter{mgr: pluginsMgr, rebuild: assistant}),
	)
```

- [ ] Add the production `gitClone` function and the `pluginAdapter` type at end of `cmd/czcli/main.go`:

```go
// gitClone is the production CloneFunc: shallow git clone of gitURL into dest.
// We pin --depth=1 since runtime never needs history. Any non-zero exit is
// surfaced verbatim through the returned error.
func gitClone(ctx context.Context, gitURL, dest string) error {
	cmd := exec.CommandContext(ctx, "git", "clone", "--depth=1", gitURL, dest)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git clone %s: %w (output: %s)", gitURL, err, string(out))
	}
	return nil
}

// pluginAdapter satisfies cli.pluginBackend over the Manager + Assistant. It
// flattens plugins.PluginInfo into cli.PluginListItem for List, and on every
// mutation re-runs Manager.Load and asks the Assistant to Rebuild so the
// agent picks up new skills/MCP/LSP/hooks/commands on the next turn.
type pluginAdapter struct {
	mgr     *plugins.Manager
	rebuild *agent.Assistant
}

func (a pluginAdapter) List(ctx context.Context) ([]cli.PluginListItem, error) {
	_, infos, err := a.mgr.Load(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]cli.PluginListItem, 0, len(infos))
	for _, p := range infos {
		out = append(out, cli.PluginListItem{
			Name:       p.Name,
			Version:    p.Version,
			Source:     p.Source,
			Enabled:    p.Enabled,
			SkillCount: p.Counts.Skills,
			MCPCount:   p.Counts.MCP,
			LSPCount:   p.Counts.LSP,
			HookCount:  p.Counts.Hooks,
			CmdCount:   p.Counts.Commands,
			AgentCount: p.Counts.Agents,
		})
	}
	return out, nil
}

func (a pluginAdapter) Install(ctx context.Context, gitURL, name string) error {
	_, err := a.mgr.Install(ctx, gitURL, name)
	return err
}

func (a pluginAdapter) Enable(_ context.Context, name string) error  { return a.mgr.Enable(name) }
func (a pluginAdapter) Disable(_ context.Context, name string) error { return a.mgr.Disable(name) }
func (a pluginAdapter) Remove(_ context.Context, name string) error  { return a.mgr.Remove(name) }

// Rebuild re-runs Manager.Load and (when Plan 6 has landed Assistant.Rebuild)
// rebuilds the agent with the refreshed Contributions. Until then, it logs and
// is a soft success — Plan 7 is forward-compatible with Plan 6 wiring.
func (a pluginAdapter) Rebuild(ctx context.Context) error {
	contrib, _, err := a.mgr.Load(ctx)
	if err != nil {
		return fmt.Errorf("plugins: reload: %w", err)
	}
	if rb, ok := any(a.rebuild).(interface {
		Rebuild(ctx context.Context, contrib plugins.Contributions) error
	}); ok {
		return rb.Rebuild(ctx, contrib)
	}
	slog.Info("plugins: contributions reloaded (agent rebuild not yet wired)",
		"skills_dirs", len(contrib.SkillDirs),
		"mcp_servers", len(contrib.MCPServers),
		"lsp_servers", len(contrib.LSPServers),
		"hooks", len(contrib.Hooks),
		"commands", len(contrib.Commands),
	)
	return nil
}
```

- [ ] Add imports to `cmd/czcli/main.go`:

```go
	"os/exec"

	"github.com/caxqueiroz/czcli/internal/plugins"
```

- [ ] Sanity-build:

  ```bash
  go build ./...
  ```

  FAIL or PASS depends on whether `agent.Assistant.Rebuild` exists from Plan 6. Plan 7's `Rebuild` is intentionally interface-typed-asserted so the build passes either way. If the build fails for an unrelated reason, fix the import order or stub.

- [ ] Run the full suite:

  ```bash
  go test ./...
  ```

  Expect PASS across `internal/plugins/...` and `internal/channel/cli/...`. Other packages are unaffected.

- [ ] Commit:

  ```bash
  git add cmd/czcli/main.go
  git commit -m "$(cat <<'EOF'
  feat(czcli): wire plugins.Manager + /plugin command into main

  Constructs the Manager with cfg.Plugins + ~/.czcli/plugins.json + git
  CloneFunc, builds a pluginAdapter satisfying cli.pluginBackend, and
  registers it on the CLI. Mutations re-run Manager.Load and (when Plan 6
  lands Assistant.Rebuild) hot-reload the agent.

  Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
  EOF
  )"
  ```

---

### Task 13 — Channel Status: surface `PluginCount` / `PluginNames`

**Files:** `internal/channel/channel.go`, `cmd/czcli/main.go`

This task is a no-op IF Plan 6 has already widened `channel.Status` with `PluginCount` / `PluginNames` and `agent.Assistant.Status` already populates them. Verify with:

```bash
grep -n "PluginCount" internal/channel/channel.go
```

If absent (Plan 6 not yet landed), add the fields here so the dashboard counter has somewhere to land. Plan 6 will idempotently re-add when it merges.

- [ ] Edit `internal/channel/channel.go` — append to the `Status` struct (preserving existing fields):

```go
	PluginCount int
	PluginNames []string
```

- [ ] No new test (Plan 6 / Plan 4 own the view-rendering test). Build:

  ```bash
  go build ./...
  ```

- [ ] In `cmd/czcli/main.go`, wrap the `assistant.Status` call (or the StatusFunc passed to `cli.New`) to inject the count from a snapshot the `pluginAdapter` exposes via a `Snapshot() (count int, names []string)` method:

  Add to `pluginAdapter`:

  ```go
  // Snapshot returns the last-known plugin count + names for the dashboard.
  // Cheap: re-runs Load against the local filesystem only.
  func (a pluginAdapter) Snapshot(ctx context.Context) (int, []string) {
  	_, infos, err := a.mgr.Load(ctx)
  	if err != nil {
  		return 0, nil
  	}
  	names := make([]string, 0, len(infos))
  	for _, p := range infos {
  		if p.Enabled {
  			names = append(names, p.Name)
  		}
  	}
  	return len(names), names
  }
  ```

  Then wrap the status func:

  ```go
  adapter := pluginAdapter{mgr: pluginsMgr, rebuild: assistant}
  ch := cli.New(
  	cli.WithSessionID("cli"),
  	cli.WithScheduler(scheduleAdapter{store: store, sched: sched}),
  	cli.WithPlugins(adapter),
  )
  statusFn := func(ctx context.Context) (channel.Status, error) {
  	st, err := assistant.Status(ctx)
  	if err != nil {
  		return st, err
  	}
  	st.PluginCount, st.PluginNames = adapter.Snapshot(ctx)
  	return st, nil
  }
  if err := ch.Start(ctx, assistant.Handle, statusFn); err != nil {
  	return fmt.Errorf("run cli channel: %w", err)
  }
  ```

  Replace the existing `ch.Start(ctx, assistant.Handle, assistant.Status)` line with the block above.

- [ ] Build + test:

  ```bash
  go build ./...
  go test ./...
  ```

  PASS.

- [ ] Commit:

  ```bash
  git add internal/channel/channel.go cmd/czcli/main.go
  git commit -m "$(cat <<'EOF'
  feat(channel): surface PluginCount/PluginNames on Status

  Adds the fields to channel.Status if Plan 6 hasn't yet, and wires a
  pluginAdapter.Snapshot() into the Status func so the dashboard renders
  a live plugin counter.

  Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
  EOF
  )"
  ```

---

### Task 14 — Manifest parser: `$ARGUMENTS` expansion helper (used at dispatch time)

**Files:** `internal/plugins/manifest.go`, `internal/plugins/manifest_test.go`

Plan 7 ships `$ARGUMENTS` as a tiny utility that the slash-command dispatcher (or future `cli.Command` hook) calls. We expose it so a follow-up plan can plug plugin commands into `handleCommand`. The CLI registration of plugin-contributed commands is a separate concern (`Contributions.Commands` is already returned; wiring `cli.handleCommand` to look them up after the fixed cases is a follow-up).

- [ ] Append FAILING test:

```go
func TestExpandArgumentsPresent(t *testing.T) {
	got := ExpandArguments("Deploy $ARGUMENTS now.", "prod")
	if got != "Deploy prod now." {
		t.Errorf("got %q", got)
	}
}

func TestExpandArgumentsAbsentAppends(t *testing.T) {
	got := ExpandArguments("Just do it.", "fast")
	want := "Just do it.\n\nARGUMENTS: fast"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestExpandArgumentsAbsentNoArgsUnchanged(t *testing.T) {
	got := ExpandArguments("Hello.", "")
	if got != "Hello." {
		t.Errorf("got %q", got)
	}
}
```

- [ ] Run `go test ./internal/plugins/...` → FAIL.

- [ ] Minimal impl — append to `internal/plugins/manifest.go`:

```go
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
```

- [ ] Run `go test ./internal/plugins/...` → PASS.

- [ ] Commit:

  ```bash
  git add internal/plugins/manifest.go internal/plugins/manifest_test.go
  git commit -m "$(cat <<'EOF'
  feat(plugins): ExpandArguments helper for $ARGUMENTS substitution

  v1 ships whole-string substitution (matches Claude Code's documented
  baseline). Indexed forms ($ARGUMENTS[N], $N) are a YAGNI follow-up.

  Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
  EOF
  )"
  ```

---

### Task 15 — End-to-end smoke test + lint pass

**Files:** none (verification only)

- [ ] Run the full suite + lint:

  ```bash
  go test ./...
  golangci-lint run ./...
  go mod tidy
  ```

  All must PASS / produce no diff.

- [ ] Manual smoke (optional — requires a real `git` and network; skip in CI):

  ```bash
  mkdir -p ~/.czcli/plugins
  echo '{"name":"hello"}' > /tmp/p.json
  # Build + run czcli, then in the TUI:
  # /plugin list
  # /plugin install https://github.com/example/hello-plugin hello
  # /plugin list
  # /plugin disable hello
  # /plugin enable hello
  # /plugin remove hello
  ```

- [ ] If all green, no commit (verification only). If lint flagged anything, fix in a follow-up commit per its message.

---

## YAGNI follow-ups (not in this plan)

- `$ARGUMENTS[N]` / `$N` / named `arguments` frontmatter (positional argument expansion).
- Manifest-level `commands` / `agents` / `hooks` / `mcpServers` / `lspServers` / `outputStyles` path overrides.
- `${CLAUDE_PLUGIN_ROOT}` / `${CLAUDE_PLUGIN_DATA}` / `${CLAUDE_PROJECT_DIR}` substitution in MCP / LSP / hook commands.
- `userConfig` interactive prompts on enable.
- Marketplace (`/plugin marketplace add`, `marketplace.json`).
- MCP `env` / `headers` plumbing — requires Plan 6 widening `config.MCPServerConfig`.
- 32+ Claude Code hook events beyond `UserPromptSubmit`/`PreToolUse`/`PostToolUse`/`Stop`.
- Plugin signing, sandboxing, `--strict` validation, dependency resolution.
- `defaultEnabled` honoring, `displayName`, `keywords`-driven search.
- Wiring `Contributions.Commands` into `cli.handleCommand` for direct `/<plugin>:<command>` invocation (Plan 6 or a follow-up to Plan 7 owns the namespacing).
