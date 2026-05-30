# czcli Extensibility — Skills, MCP, Plugins, LSP, Hooks

**Date:** 2026-05-30
**Status:** Approved design, ready for implementation planning

## Summary

Make czcli extensible the way Claude Code is. Add five tightly-related capabilities:

1. **Skills** — markdown-based discoverable agent capabilities, via dive's top-level `skill/` package.
2. **MCP servers** — real connectivity (stdio + HTTP) using dive's `experimental/mcp` module.
3. **Plugins** — **Claude Code-compatible** plugin bundles that contribute skills, MCP servers, sub-agents, slash commands, LSP servers, and hooks.
4. **LSP servers** — code-intelligence tools (`definition`, `references`, `hover`, `symbols`, `diagnostics`) backed by Language Server Protocol clients.
5. **Hooks** — Claude-Code-style declarative shell-command hooks on `UserPromptSubmit` / `PreToolUse` / `PostToolUse` / `Stop`.

Plugins package the other four. Users install via `~/.czcli/plugins/<name>/` drop-folders or `/plugin install <git-url>`.

## Goals

- One coherent extensibility story so a single Claude Code-format plugin works in czcli with no porting.
- Lean wiring over dive's `skill/` and `experimental/mcp/` packages — no parallel abstractions.
- Hot-reload after enable/disable/install so users don't restart for each change.
- Best-effort throughout: a broken plugin/server/hook degrades gracefully, never blocks a reply.

## Non-goals

- Plugin marketplace (`/plugin marketplace add` + `marketplace.json`). Manual git URLs only.
- Plugin signing/verification or sandboxing beyond OS user permissions.
- Cross-session sub-agent / hook persistence (separate backlog item).
- Replacing dive's hook system; we layer plugin-declared shell hooks on top of it.

## Key decisions

| Decision | Choice |
|---|---|
| Plugin format | **Claude Code-compatible** (`.claude-plugin/plugin.json` + `commands/` + `skills/` + `agents/` + `.mcp.json` + `.claude-plugin/lsp.json` + `.claude-plugin/hooks.json`) |
| Plugin discovery | Drop-folder: `~/.czcli/plugins/*` and `./.czcli/plugins/*` (project-local override) |
| Plugin install UX | `/plugin install <git-url> [name]` + `list / enable / disable / remove` |
| Plugin enable state | `~/.czcli/plugins.json` (`{"<name>": {"enabled": bool, "source": "..."}}`) |
| Skills source | dive `skill/` package (v1.7.0, top-level), thin wrapper |
| MCP source | dive `experimental/mcp` (separately-versioned module), real client |
| MCP transports | stdio + HTTP (driven by which fields the server config carries) |
| LSP client | `go.lsp.dev/protocol` + `go.lsp.dev/jsonrpc2` |
| Hook executor | shell child process + JSON envelope on stdin, exit ≠ 0 blocks, 5s default timeout |
| Status | `channel.Status` gains `SkillCount`/`MCPServerCount`/`LSPServerCount`/`PluginCount`/`HookCount` |

## Architecture

```
   ┌─────────────────── cmd/czcli/main.go ───────────────────┐
   │ config → embedder → store → plugins.Load                │
   │         → skills.Load  → mcp.Connect → lsp.Connect      │
   │         → hooks.Load  → agent.Build(catalog, tools, …)  │
   └─────────────────────────────────────────────────────────┘
                                │
        ┌────────── Plugins (internal/plugins) ──────────┐
        │ discover ~/.czcli/plugins/* + .czcli/plugins/* │
        │ parse .claude-plugin/plugin.json + manifests   │
        │ contribute: skill dirs, mcp servers, lsp       │
        │   servers, agent dirs, slash commands, hooks   │
        │ /plugin install|list|enable|disable|remove     │
        └────────────────┬───────────────────────────────┘
                         │
   ┌─────┬───────────────┼──────────────┬─────────────┬──────────────┐
   ▼     ▼               ▼              ▼             ▼              ▼
 Skills  MCP            LSP           Hooks       Sub-agents    Slash cmds
 dive    dive exp/mcp  go.lsp.dev    internal/    dive          internal/
 skill.* Manager       client +      hooks +      subagent +    channel/cli
 Catalog + tools       FuncTools     dispatcher   orchestration
```

Hot-reload = rebuild the dive.Agent after a `/plugin` mutation (cheap; dive is stateless per turn).

## Package layout (additions / changes)

```
internal/
├── plugins/        NEW       discover, parse, install, state, hot-reload
├── skills/         NEW       thin wrapper over dive skill.Loader/Catalog
├── lsp/            NEW       LSP client manager + dive FuncTools
├── hooks/          NEW       hook loader + dispatcher (shell exec + JSON envelope)
├── mcp/            REWRITE   replace no-op Connect with real dive MCP Manager
├── agent/agent.go  MODIFY    Build() wires skills + mcp + lsp tools + hook dispatcher
├── channel/cli/    MODIFY    /plugin /skills /mcp /lsp /hooks slash commands
└── config/         MODIFY    Plugins, Skills, LSP sections; MCP merged from user + plugins
```

## Plugin format (Claude Code-compatible)

```
<plugin_root>/
├── .claude-plugin/
│   ├── plugin.json        manifest: name, version, description, author, homepage
│   ├── lsp.json           LSP servers (czcli extension; mirrors .mcp.json shape)
│   └── hooks.json         hook entries
├── commands/*.md          slash commands (YAML frontmatter + prompt body)
├── skills/<name>/SKILL.md skill markdown
├── agents/<name>.md       sub-agent personas (consumed by dive subagent.FileLoader)
├── .mcp.json              { "mcpServers": { "<name>": { "command"|"url", "args", "env" } } }
└── README.md
```

`.claude-plugin/plugin.json`:
```json
{
  "name": "my-plugin",
  "version": "0.1.0",
  "description": "...",
  "author": { "name": "...", "url": "..." },
  "homepage": "..."
}
```

`.claude-plugin/hooks.json` (Claude Code's structure):
```json
[
  { "event": "PreToolUse", "matcher": { "tool": "Bash" },
    "command": ["/bin/sh", "-c", "scripts/policy.sh"], "timeout_seconds": 5 }
]
```

`.claude-plugin/lsp.json`:
```json
{ "servers": {
    "gopls": { "command": "gopls", "args": [], "languages": ["go"], "rootPatterns": ["go.mod"] }
  }
}
```

## Subsystem detail

### Plugins (`internal/plugins`)

`Manager` walks `cfg.Plugins.Dirs`, parses each plugin's manifests, skips disabled ones (per `~/.czcli/plugins.json`), and returns:

```go
type Contributions struct {
    SkillDirs   []string
    AgentDirs   []string
    MCPServers  []config.MCPServerConfig
    LSPServers  []config.LSPServerConfig
    Hooks       []hooks.Entry
    Commands    []cli.Command   // pre-composed slash commands from plugins/commands/*.md
}

func (m *Manager) Load(ctx context.Context) (Contributions, []PluginInfo, error)
func (m *Manager) Install(ctx context.Context, gitURL, name string) (PluginInfo, error)  // git clone + enable
func (m *Manager) Enable(name string) error
func (m *Manager) Disable(name string) error
func (m *Manager) Remove(name string) error
```

`PluginInfo`: name, version, source, enabled, dir, contribution counts. Used by `/plugin list` and `channel.Status`.

State file `~/.czcli/plugins.json` is plain JSON, atomic-write via temp + rename. Corruption → log + fall back to "all discovered enabled" (don't lose work).

### Skills (`internal/skills`)

```go
type LoadResult struct {
    Catalog *skill.Catalog
    Names   []string  // for /skills and Status
}
func Load(cfg config.SkillsConfig, extraDirs []string) (*LoadResult, error)
```

Calls dive's `skill.Loader` over `cfg.Skills.Dirs` (defaults `.dive/skills`, `~/.dive/skills`) **plus** plugin-contributed `skills/` dirs. The catalog is passed to `dive.NewAgent` via whichever `AgentOptions` field dive v1.7 exposes (verified at impl time — likely `Skills` or `SkillCatalog`).

### MCP (`internal/mcp` — rewrite)

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

Pin `github.com/deepnoodle-ai/dive/experimental/mcp` (its own go.mod). Build a `mcp.Manager`, register each server (stdio if `Command` set; HTTP if `URL`), call `InitializeServers(ctx)`, return adapted tools.

OAuth token store: file-based `~/.czcli/mcp-tokens.json`, implementing dive's `TokenStore` interface.

User-config MCP servers are merged with plugin-contributed ones; duplicate names log a warning, user config wins.

### LSP (`internal/lsp`)

```go
type Server struct { Name, Cmd string; Args, Languages, RootPatterns []string; client *jsonrpc2.Conn }

type Manager struct { /* servers map[lang]*Server; rootDir string */ }

func New(ctx context.Context, servers []config.LSPServerConfig, rootDir string) (*Manager, []ServerInfo, error)
func (m *Manager) Tools() []dive.Tool   // generic LSP tools, routed by file extension
func (m *Manager) Close() error
```

Lifecycle per server: spawn `command+args` with stdio, do `initialize`/`initialized` with `workspaceFolders=rootDir`, track `didOpen`/`didChange` per file the agent reads. Per-request 10s timeout. Crash → log, drop tools for that language, continue.

Tools exposed (one set, routed by file's language → server):

| Tool | LSP method |
|---|---|
| `lsp_definition(file, line, character)` | `textDocument/definition` |
| `lsp_references(file, line, character)` | `textDocument/references` |
| `lsp_hover(file, line, character)` | `textDocument/hover` |
| `lsp_document_symbols(file)` | `textDocument/documentSymbol` |
| `lsp_workspace_symbols(query)` | `workspace/symbol` |
| `lsp_diagnostics(file)` | last cached `textDocument/publishDiagnostics` for the file |

Language routing: file extension → `Server.Languages`; ambiguity → first match deterministic. Files outside any registered language route return a "no LSP server for language X" tool result (model adapts).

### Hooks (`internal/hooks`)

```go
type Event string  // "UserPromptSubmit" | "PreToolUse" | "PostToolUse" | "Stop"

type Entry struct {
    Event          Event
    Matcher        Matcher       // tool name, command substring, etc.
    Command        []string      // argv
    TimeoutSeconds int           // default 5
    Source         string        // plugin name (for /hooks listing)
}

type Matcher struct { Tool, Command string }  // exact-match on Tool name; substring on Command (Bash)

type Dispatcher struct { /* entries []Entry; logger *slog.Logger */ }

func Load(plugin Contributions) (*Dispatcher, error)
func (d *Dispatcher) Dispatch(ctx context.Context, ev Event, payload any) (block bool, feedback string)
func (d *Dispatcher) Entries() []Entry  // for /hooks
```

Wiring into the agent:

- `PreGenerationHook` → `Dispatch(UserPromptSubmit, {input})`. Non-zero exit → return feedback to user, abort turn.
- `PreToolUseHook` → `Dispatch(PreToolUse, {tool, input})` BEFORE the existing permission gate. Non-zero exit → return blocking ToolResult.
- `PostGenerationHook` (per turn) → `Dispatch(Stop, {final_text})`. Block is informational only here.
- Post-tool wiring: dive doesn't expose a discrete `PostToolUse` hook in v1.7; we synthesize it inside the agent's tool callback path. If a clean injection point doesn't exist, log the limitation and ship `PreToolUse` + `Stop` + `UserPromptSubmit` (still very useful).

Envelope: JSON on stdin, e.g. `{"event":"PreToolUse","tool":"Bash","input":{"command":"rm -rf /"}}`. Stdout becomes the feedback string. Exit code is the gate. SIGKILL after `TimeoutSeconds`. Working directory = the project root. Environment inherits czcli's env.

### Slash commands

`internal/channel/cli/commands.go` adds:

- `/plugin list` — table of name · version · enabled · source · counts.
- `/plugin install <git-url> [name]` — git clone, enable, hot-reload.
- `/plugin enable|disable|remove <name>` — flip state, hot-reload (remove confirms).
- `/skills [name]` — list available skills (name · description · plugin); show details with name.
- `/mcp` — list connected servers · transport · tool count; show last error if failed.
- `/lsp` — list running language servers · languages · status.
- `/hooks` — list registered hooks (event · matcher · plugin · timeout).

All commands operate on the live `Manager` instances; mutations call back into `agent.Rebuild(ctx)` which re-runs `Build()` with refreshed contributions.

### Config additions

```yaml
plugins:
  enabled: true
  dirs:    [~/.czcli/plugins, .czcli/plugins]
skills:
  enabled: true
  dirs:    [.dive/skills, ~/.dive/skills]
lsp:
  enabled: true
  # user-level LSP server entries; plugins contribute more
  servers: []
# existing mcp: section unchanged; user + plugin entries merged
```

`config.LSPServerConfig`:
```go
type LSPServerConfig struct {
    Name         string   `yaml:"name"`
    Command      string   `yaml:"command"`
    Args         []string `yaml:"args"`
    Languages    []string `yaml:"languages"`
    RootPatterns []string `yaml:"root_patterns"`
}
```

## Data flow (startup)

```
config.Load
  → plugins.Manager.Load           → Contributions
  → skills.Load(cfg, extraDirs)    → catalog
  → mcp.Connect(servers, tokens)   → tools, infos
  → lsp.New(servers, rootDir)      → tools
  → hooks.Load(contributions)      → dispatcher
  → agent.Build(cfg, store, model,
       skills.catalog, mcp+lsp+plugin+toolkit tools, hooks.Dispatcher)
```

On a slash-command mutation: re-run from `plugins.Manager.Load` and rebuild the agent. `agent.Rebuild(ctx)` swaps the inner `*dive.Agent` atomically under a mutex; in-flight turn completes first, next turn uses the new agent.

## Error handling (best-effort throughout)

- Plugin parse error → log, skip plugin, continue.
- Skill load error → log, skip skill, catalog gets the rest.
- MCP connect error → log, server marked failed in `/mcp`, others connect.
- LSP spawn / initialize error → log, drop that language, others continue.
- Hook exec error / timeout → log, treat as no-op (don't block agent).
- `git clone` failure → return error to user via slash command; nothing persisted.
- `plugins.json` corruption → log + fall back to "all enabled" (don't lose state silently).

## Testing

- `plugins`: `t.TempDir()` plugin trees; parse `.claude-plugin/plugin.json` + sub-manifests; state-file round-trip; mock `git` via injected `cloneFunc(url, dest) error`; hot-reload re-emits contributions.
- `skills`: temp `.dive/skills/<name>/SKILL.md` fixtures; assert catalog entries and extraDirs merging.
- `mcp`: dive's `experimental/mcp` exposes mockable `Transport`s; use them for stdio + HTTP fixtures; no live servers.
- `lsp`: a fake `jsonrpc2` server (in-process bidi pipe) that responds to `initialize` and one method per tool; tests assert tool requests/responses and language routing; no real language server processes.
- `hooks`: shell-command tests use `/bin/sh -c 'echo …; exit N'` with a temp `PATH`; assert dispatcher result/feedback/block; timeout test uses `sleep 10` with `TimeoutSeconds=1`.
- `agent`: extend existing tests to assert skills, mcp, lsp tools land in the registry; hook dispatcher wired into PreGeneration/PreToolUse.
- `cli`: drive `/plugin`/`/skills`/`/mcp`/`/lsp`/`/hooks` via synthetic `tea.Msg`s and assert `View()` substrings (existing pattern).

## Status / dashboard

`channel.Status` gains:
```go
SkillCount     int
SkillNames     []string
MCPServerCount int
MCPServerNames []string
LSPServerCount int
LSPLanguages   []string
PluginCount    int
PluginNames    []string
HookCount      int
```

Bottom bar appends compact counters (`· 📜N · 🧩P · 🧠L · ⚓H`) keeping the model/context gauge in the top bar. Detail via the slash commands.

## Open items to verify during implementation

1. dive v1.7 `AgentOptions` field for skills (`Skills` vs `SkillCatalog` vs an option func) — read `$(go env GOMODCACHE)/github.com/deepnoodle-ai/dive@v1.7.0/agent.go`.
2. dive `experimental/mcp` Manager API surface (server registration shape, tool adapter, OAuth `TokenStore` interface) — pin module + read source.
3. Whether a clean `PostToolUse` injection point exists in dive v1.7; if not, ship `UserPromptSubmit` + `PreToolUse` + `Stop` and log the gap.
4. Concrete LSP client choice — `go.lsp.dev/protocol`/`jsonrpc2` vs `sourcegraph/go-lsp` vs `tliron/glsp`. Pick at plan time based on maintenance status (current date: 2026-05).
5. Hook envelope keys exact match with Claude Code's payloads so cross-tool plugin compatibility is real.

## Out of scope (tracked in `docs/backlog.md`)

- Plugin marketplace + `marketplace.json`.
- Plugin signing / verification / sandboxing.
- Cross-session sub-agent and hook persistence.
- Plugin contributions of Go code (only declarative manifests + markdown + shell hooks).
