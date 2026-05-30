# czcli Extensibility — Shared Contracts (read before plans 6–9)

This addendum pins the cross-cutting Go types for the extensibility plans (Skills + MCP +
Plugins + LSP + Hooks). It augments `00-shared-contracts.md`; everything there still applies.

Spec: `docs/superpowers/specs/2026-05-30-extensibility-design.md`

## Dive at v1.7.0 (already on main)

Already on main: dive v1.7.0 + `providers/openai` v1.7.0. Sub-agent / orchestration imports
already migrated (`subagent`, `toolkit/orchestration`). For these plans:

| Dependency | Use | Plan |
|---|---|---|
| `github.com/deepnoodle-ai/dive/skill` | top-level Skills package (Loader/Catalog/Parser) | 6 |
| `github.com/deepnoodle-ai/dive/experimental/mcp` | MCP client + Manager (separate go.mod) | 6 |
| `go.lsp.dev/protocol` + `go.lsp.dev/jsonrpc2` | LSP client | 8 |

Verify exact `skill.*` exports and v1.7 `dive.AgentOptions` field for skills against the
installed source under `$(go env GOMODCACHE)/github.com/deepnoodle-ai/dive@v1.7.0/`.

## Config additions — `internal/config` (extends Plan 1)

```go
type SkillsConfig struct {
    Enabled bool     `yaml:"enabled"`
    Dirs    []string `yaml:"dirs"`     // defaults: [".dive/skills", "~/.dive/skills"]
}

type PluginsConfig struct {
    Enabled bool     `yaml:"enabled"`
    Dirs    []string `yaml:"dirs"`     // defaults: ["~/.czcli/plugins", ".czcli/plugins"]
}

type LSPConfig struct {
    Enabled bool              `yaml:"enabled"`
    Servers []LSPServerConfig `yaml:"servers"`
}

type LSPServerConfig struct {
    Name         string   `yaml:"name"`
    Command      string   `yaml:"command"`
    Args         []string `yaml:"args"`
    Languages    []string `yaml:"languages"`
    RootPatterns []string `yaml:"root_patterns"`
}

// Config gains fields:
//   Skills  SkillsConfig  `yaml:"skills"`
//   Plugins PluginsConfig `yaml:"plugins"`
//   LSP     LSPConfig     `yaml:"lsp"`
```

`config.Load` expands `~` in `Skills.Dirs` and `Plugins.Dirs` (existing helper applies).

## Skills layer — `internal/skills` (Plan 6)

```go
type LoadResult struct {
    Loader *skill.Loader  // dive v1.7 hands the Loader directly to dive.AgentOptions.Extensions
    Names  []string       // for /skills + channel.Status
}

func Load(cfg config.SkillsConfig, extraDirs []string) (*LoadResult, error)
```

Implementation calls `skill.Load(ctx, skill.LoaderOptions{ProjectDir, HomeDir, AdditionalPaths, Logger})`
covering `cfg.Dirs ∪ extraDirs`. The returned `*skill.Loader` is passed to `dive.NewAgent` via
`AgentOptions.Extensions` (the Loader implements `dive.Extension`: `Tools()`, `Hooks()`, `Rules()`).
Errors per skill are logged + skipped; aggregate error returned only if NO dirs were readable.

## MCP layer — `internal/mcp` (Plan 6 rewrite)

```go
type ServerInfo struct {
    Name      string
    Transport string  // "stdio" | "http"
    Connected bool
    ToolCount int
    LastError string
}

func Connect(ctx context.Context, servers []config.MCPServerConfig, tokenStorePath string) ([]dive.Tool, []ServerInfo, error)
```

Pin `github.com/deepnoodle-ai/dive/experimental/mcp` (its own go.mod).
Transport selection:

- `Command != ""` → stdio with `Command` + `Args` + `Env`.
- `URL != ""` → HTTP (streamable HTTP transport).

OAuth: file-backed `TokenStore` at `tokenStorePath` (default `~/.czcli/mcp-tokens.json`).
Errors per server logged + skipped; surface in `ServerInfo.LastError`.

## Plugins layer — `internal/plugins` (Plan 7)

```go
type Manifest struct {
    Name        string  `json:"name"`
    Version     string  `json:"version"`
    Description string  `json:"description"`
    Author      Author  `json:"author"`
    Homepage    string  `json:"homepage"`
}
type Author struct { Name, URL string }

type Contributions struct {
    SkillDirs  []string                  // each plugin's skills/ if present
    AgentDirs  []string                  // each plugin's agents/ if present
    MCPServers []config.MCPServerConfig  // from each plugin's .mcp.json
    LSPServers []config.LSPServerConfig  // from each plugin's .claude-plugin/lsp.json
    Hooks      []HookEntry               // from each plugin's .claude-plugin/hooks.json
    Commands   []PluginCommand           // from each plugin's commands/*.md
}

// HookEntry mirrors hooks.Entry but lives here so plugins doesn't import hooks.
type HookEntry struct {
    Event          string                 // "UserPromptSubmit"|"PreToolUse"|"PostToolUse"|"Stop"
    Matcher        map[string]string      // {"tool":"Bash"} | {"command":"rm"}
    Command        []string               // argv
    TimeoutSeconds int                    // default 5
    Source         string                 // plugin name
}

type PluginCommand struct {
    Name        string  // file basename without .md
    Description string  // frontmatter description
    Prompt      string  // markdown body (expanded with args at call time)
    Source      string  // plugin name
}

type PluginInfo struct {
    Name, Version, Source, Dir string
    Enabled                    bool
    Counts                     struct{ Skills, Agents, MCP, LSP, Hooks, Commands int }
}

// CloneFunc is injected so tests can mock git.
type CloneFunc func(ctx context.Context, gitURL, dest string) error

type Manager struct { /* dirs []string, stateFile string, clone CloneFunc */ }

func New(cfg config.PluginsConfig, stateFile string, clone CloneFunc) *Manager
func (m *Manager) Load(ctx context.Context) (Contributions, []PluginInfo, error)
func (m *Manager) Install(ctx context.Context, gitURL, name string) (PluginInfo, error)
func (m *Manager) Enable(name string) error
func (m *Manager) Disable(name string) error
func (m *Manager) Remove(name string) error
```

State file `~/.czcli/plugins.json`:
```json
{ "<plugin-name>": { "enabled": true, "source": "<git-url-or-local>" } }
```
Atomic write via temp + rename. Corruption → log + fall back to "all discovered enabled".

## LSP layer — `internal/lsp` (Plan 8)

```go
type ServerInfo struct {
    Name      string
    Languages []string
    Running   bool
    LastError string
}

type Manager struct { /* per-language clients + lifecycle */ }

func New(ctx context.Context, servers []config.LSPServerConfig, rootDir string) (*Manager, []ServerInfo, error)
func (m *Manager) Tools() []dive.Tool   // generic LSP tools, language-routed
func (m *Manager) Close() error
```

Tools registered (all return `dive.NewToolResultText(...)` formatted output):

| Tool name | Maps to |
|---|---|
| `lsp_definition` | `textDocument/definition` |
| `lsp_references` | `textDocument/references` |
| `lsp_hover` | `textDocument/hover` |
| `lsp_document_symbols` | `textDocument/documentSymbol` |
| `lsp_workspace_symbols` | `workspace/symbol` |
| `lsp_diagnostics` | last cached `textDocument/publishDiagnostics` |

Language routing: file extension → language string → server. Default extension map:
`.go→go .py→python .ts→typescript .tsx→typescript .js→javascript .jsx→javascript .rs→rust
.rb→ruby .java→java .c→c .cpp→cpp .h→c .hpp→cpp .cs→csharp .php→php .lua→lua .yaml→yaml
.yml→yaml .json→json .md→markdown`. Files with no mapping return a "no LSP server for X"
tool result so the model adapts.

## Hooks layer — `internal/hooks` (Plan 9)

```go
type Event string
const (
    EventUserPromptSubmit Event = "UserPromptSubmit"
    EventPreToolUse       Event = "PreToolUse"
    EventPostToolUse      Event = "PostToolUse"
    EventStop             Event = "Stop"
)

type Matcher struct {
    Tool    string // exact match on tool name (case-insensitive)
    Command string // substring match on Bash command, etc.
}

type Entry struct {
    Event          Event
    Matcher        Matcher
    Command        []string
    TimeoutSeconds int     // default 5
    Source         string  // plugin name
}

type Result struct {
    Block    bool   // true if exit != 0
    Feedback string // stdout (trimmed)
}

type Dispatcher struct { /* entries []Entry, logger *slog.Logger */ }

// From plugins.HookEntry to hooks.Entry: convert in plugins.Contributions consumer.
func Load(entries []Entry, logger *slog.Logger) *Dispatcher
func (d *Dispatcher) Dispatch(ctx context.Context, ev Event, payload any) Result
func (d *Dispatcher) Entries() []Entry  // for /hooks
```

Wiring (Plan 9 modifies `internal/agent/hooks.go`):
- Existing `PreGenerationHook` → `Dispatch(EventUserPromptSubmit, …)`; block → abort turn with feedback.
- Existing `PreToolUseHook` → `Dispatch(EventPreToolUse, …)` BEFORE the permission gate; block → return `dive.NewUserFeedback(feedback)` or equivalent blocking ToolResult.
- Existing `PostGenerationHook` → `Dispatch(EventStop, …)`; informational only (no block).
- `EventPostToolUse`: if dive v1.7 doesn't expose a discrete post-tool injection, log the limitation and ship the other three (documented).

Envelope = JSON on hook stdin:
```json
{ "event":"PreToolUse", "tool":"Bash", "input": { "...": "..." } }
```
Stdout (UTF-8, trimmed) becomes `Result.Feedback`. Exit code is the gate. SIGKILL after `TimeoutSeconds`. Working dir = process CWD.

## Agent integration — `internal/agent` (additive across plans)

```go
// Build's signature extends:
func Build(ctx context.Context, cfg *config.Config, store *memory.Store, model llm.StreamingLLM,
    skills *skills.LoadResult, mcpTools []dive.Tool, lspTools []dive.Tool,
    plugins plugins.Contributions, hooksDisp *hooks.Dispatcher,
) (*Assistant, error)

// BuildWithMCPInfos is Plan 6's variant that ALSO accepts MCP ServerInfos so the dashboard's
// /mcp command and Status.MCPServerNames are populated without bloating Build's surface. Plans
// 7-9 may use either Build or BuildWithMCPInfos depending on whether MCP info is being passed.
func BuildWithMCPInfos(ctx context.Context, cfg *config.Config, store *memory.Store, model llm.StreamingLLM,
    skills *skills.LoadResult, mcpTools []dive.Tool, mcpInfos []mcp.ServerInfo,
    lspTools []dive.Tool, plugins plugins.Contributions, hooksDisp *hooks.Dispatcher,
) (*Assistant, error)

// Rebuild atomically swaps the inner *dive.Agent under a mutex (in-flight turn completes;
// next turn uses the new agent). Plans 6+ all use this for hot-reload after /plugin mutations.
func (a *Assistant) Rebuild(ctx context.Context, cfg *config.Config, skills *skills.LoadResult,
    mcpTools, lspTools []dive.Tool, plugins plugins.Contributions, hooksDisp *hooks.Dispatcher,
) error
```

Plan 6 introduces the extended signature with `skills`+`mcpTools` (LSP/plugins/hooks may be
empty initially); Plans 7–9 fill in the rest. Plan 6 introduces `Rebuild` even if unused, so
Plan 7 can call it without changing Plan 6's surface.

## Status / channel — `internal/channel` (extends Plan 1)

`channel.Status` gains:
```go
SkillCount     int
SkillNames     []string
MCPServerCount int
MCPServerNames []string
LSPServerCount int
LSPLanguages   []string
LSPServers     []LSPServerSummary  // per-server detail for /lsp; mirror of internal/lsp.ServerInfo to avoid import cycle
PluginCount    int
PluginNames    []string
HookCount      int

type LSPServerSummary struct {
    Name      string
    Languages []string
    Running   bool
    LastError string
}
```

Bottom-bar counters appended in `internal/channel/cli/view.go`: `· 📜N · 🧩P · 🧠L · ⚓H`.

## Slash commands — `internal/channel/cli/commands.go` (extends Plan 4/5)

Add: `/plugin (list|install|enable|disable|remove)`, `/skills [name]`, `/mcp`, `/lsp`, `/hooks`.
Each plan adds its own subcommands; Plan 7 adds the `/plugin` family + hot-reload trigger.

## Conventions

- TDD per-step (failing test → run → minimal impl → run → commit), trailer
  `Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>`.
- All tests offline: temp dirs for plugins/skills, dive's `experimental/mcp` mockable
  `Transport`s for MCP, in-process `jsonrpc2` pipe for LSP, `/bin/sh -c '...'` for hooks.
- `go test ./...`, `golangci-lint run ./...`, `go mod tidy` no-op.
- Stay on `main` per the MVP convention unless a plan needs isolation; create
  `feat/<topic>` branches if a plan's blast radius warrants it.
