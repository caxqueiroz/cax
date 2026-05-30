# cax — Architecture

This document describes the engineering shape of cax: how the pieces fit
together, the data flow for a turn, the key design decisions, and the
operational properties of the system. It is descriptive (what is) rather than
prescriptive (what should be) — the design specs under
`docs/superpowers/specs/` are the historical decision record; this file is a
single up-to-date snapshot.

## Big picture

cax is a *thin orchestrator* layered on top of
[dive](https://github.com/deepnoodle-ai/dive). dive supplies the LLM-provider
interface, the agent loop, hooks, tools, skills, sub-agents, and an MCP
module. cax adds the application layer dive deliberately leaves out: persistent
memory, multi-provider fallback, channels, a TUI, plugins, LSP, declarative
shell hooks, a scheduler, and a permission gate.

```
   ┌─────────────────────  cmd/cax/main.go  ─────────────────────┐
   │ config → embedder → store → providers (fallback)            │
   │ ↓ plugin/skills/mcp/lsp/hooks load                          │
   │ ↓ creator + assistantReloader + permission dialog           │
   │ ↓ agent.Build → *Assistant                                  │
   │ ↓ cli.New(...).Start → bubbletea program                    │
   └─────────────────────────────────────────────────────────────┘

   capabilities (all optional, hot-reloadable, best-effort):
     skills • mcp • lsp • plugins • hooks • creator • scheduler

   substrate:
     config (YAML + embedded defaults) • memory (sqlite + vec0)
```

## Layering (bottom up)

1. **dive (external).** Provides `llm.LLM`/`llm.StreamingLLM`, `dive.Agent`,
   hook types, tool primitives (`dive.FuncTool`, `dive.Tool`), sub-agents
   (`subagent.*` + `toolkit/orchestration`), skills (`skill.Loader` as a
   `dive.Extension`), and `experimental/mcp` (its own go module).
2. **Substrate.** `internal/config` (YAML + embedded `example.yaml` as the
   first-run default) and `internal/memory` (SQLite via `modernc.org/sqlite`
   + `modernc.org/sqlite/vec` — cgo-free vector store). These two packages
   are the only universal dependencies.
3. **Providers.** `internal/providers/bedrock` implements `llm.StreamingLLM`
   for Bedrock Claude through a KrakenD no-op gateway: native Anthropic
   Messages payload, `InvokeModel` + `InvokeModelWithResponseStream`, AWS
   event-stream decoding, `x-api-key` auth. OpenAI uses dive's built-in
   provider.
4. **Glue.** `internal/agent` builds the `*Assistant`. `BuildModel`
   constructs each enabled provider and wraps them in `fallbackLLM`.
   `Build`/`BuildWithMCPInfos`/`Rebuild` assemble a `dive.Agent` with tools,
   hooks, sub-agents, and skills.
5. **Capabilities.** `tools`, `skills`, `mcp`, `lsp`, `hooks`, `plugins`,
   `creator`, `scheduler`, `usercmds`. Each is independently optional and
   emits its contribution (a slice of `dive.Tool`s, a `Catalog`, a
   `Dispatcher`, etc.) into `Build`.
6. **Channels.** `internal/channel` defines the abstraction
   (`Channel`/`Handler`/`StatusFunc`/`Status`); `internal/channel/cli` is the
   only implementation today (Bubble Tea TUI). Telegram/Discord slot in here.
7. **Theme.** `internal/theme` is a global registry with embedded built-ins,
   user YAMLs, and a state file.

## Single turn — data flow

```
keystroke → bubbletea Update → programModel intercepts submitMsg
   → cli.runTurn goroutine
     → assistant.Handle(ctx, channel.Message, emit)
       → dive.Agent.CreateResponse(WithInput, WithEventCallback)
         → PreGenerationHook   (memory injection, plugin hook dispatcher)
         → llm chain (fallbackLLM picks provider, retries on retryable err)
         → PreToolUseHook      (permission dialog + plugin hooks)
         → tools run           (built-ins, MCP, LSP, sub-agents, creator,
                                recall)
         → PostToolUseHook     (plugin hooks, audit)
         → PostGenerationHook  (persist turn + embed + record usage +
                                maybe summarise)
       → stream events flow through emit → tea.Msgs via p.Send
     → turnDoneMsg back to the model with the final reply + err
```

`emit` is a `channel.EventSink`. The CLI bridges dive's `ResponseItem` events
(model deltas, tool-call/result, sub-agent start/end) into `tea.Msg`s pushed
back into the program with the captured `p.Send` — the worker goroutine never
touches model state directly.

## Memory subsystem

`internal/memory` is the only thing on disk besides config/state.

| Table | Purpose |
|---|---|
| `messages` | Full conversation history (tokens, role, content) |
| `summaries` | Rolling summary chunks once the working window exceeds `token_budget` |
| `memories` + `vec_memories` (vec0) | Semantic recall vectors; KNN via `vec_distance_cosine` |
| `usage` | Per-call token usage (chat/embedding/summary/subagent) for 1d/1w/1m rollups |
| `schedules` | Cron entries |
| `meta` | Embedding model + dim; refuses to open if config dim ≠ stored dim |

`Embedder` is pluggable (OpenAI today; Bedrock-Titan is a backlog item).
Tests use a deterministic `fakeEmbedder` (hash → fixed-dim float32) so vec0
KNN is reproducible without network.

## Provider fallback

```go
type fallbackLLM struct {
    providers   []llm.StreamingLLM
    modelIDs    []string // configured model strings for the status row
    activeIndex int      // mutex-guarded
}
```

`Generate`/`Stream` walk the chain; `isRetryable(err)` (5xx, 429,
`net.Error.Timeout()`) advances. Successful calls record `activeIndex`.
`ActiveReporter`/`ActiveModel` expose state to `agent.Status` without coupling
`channel.Status` to `llm`. This is how the status row shows
`gpt-5.5 ⚠ fallback #1`.

## Permission gate (dialog late-binding)

The permission gate is a `dive.Dialog`. Two implementations:

- `tools.ConfirmDialog` — legacy stdin/stdout for non-TUI flows.
- `cli.PermDialog` — TUI modal. Pushes a `permRequestMsg` into the program,
  blocks on a response channel; the model captures `y/Enter` or `n/Esc`.

The interesting trick: hooks read the dialog at **fire time**, not at build
time. `hookDeps.dialogFn func() dive.Dialog` closes over the `*Assistant`;
`Assistant.SetDialog` swaps the dialog after Build. That lets us wire the TUI
dialog only once the bubbletea program exists. The runtime `/permissions on|off`
toggle reuses this — it flips a flag on the live `PermDialog`.

## Hot-reload (`assistantReloader`)

`/reload`, `/plugin {enable|disable|install|remove}`, and
`create_skill/create_agent/create_command` all want to rebuild the agent's
tool/skill/MCP/LSP/hook bundle. The mechanism:

```go
type assistantReloader struct {
    mu sync.Mutex
    assistant    *agent.Assistant
    cfg          *config.Config
    skillRes     *skills.LoadResult
    mcpTools     []dive.Tool
    mcpInfos     []mcp.ServerInfo
    lspTools     []dive.Tool
    lspInfos     []lsp.ServerInfo
    hooksDisp    *hooks.Dispatcher
    creatorTools []dive.Tool
}

func (r *assistantReloader) Rebuild(ctx context.Context) error {
    // shallow-copy captured args under lock, call Rebuild unlocked.
}
```

`pluginAdapter.Rebuild` re-runs
`plugins.Manager.Load → skills.Load → mcp.Connect → lsp.New → hooks.Load`,
calls `reloader.update(...)` to refresh the captured args, then calls
`assistant.Rebuild`. The `dive.Agent` is swapped under a mutex; in-flight
turns finish on the old agent.

## Plugin model (Claude-Code-compatible)

A plugin is a directory at `~/.cax/plugins/<name>/` (or project-local
`.cax/plugins/<name>/`) with:

```
.claude-plugin/plugin.json     (name, version, author, …)
.claude-plugin/lsp.json        (LSP servers)
.claude-plugin/hooks.json      (declarative shell hooks)
.mcp.json                      (MCP servers)
commands/*.md                  (slash commands w/ frontmatter)
skills/<name>/SKILL.md
agents/<name>.md
```

`plugins.Manager.Load` returns a `Contributions` struct. cax merges those with
user config (e.g. `cfg.MCP.Servers ∪ contrib.MCPServers`) and feeds the union
into the rest of the system. State (enable/disable) lives in
`~/.cax/plugins.json` (atomic temp+rename).

## TUI architecture

bubbletea is single-threaded over one `tea.Msg` channel. cax uses a
**programModel** wrapper:

```go
type programModel struct {
    model  model    // the pure model: all View/Update logic is here
    cli    *CLI
    ctx    context.Context
    send   sender   // captured (*tea.Program).Send
    handle channel.Handler
    status channel.StatusFunc
}
```

`programModel.Update` intercepts only the side-effecting messages —
`submitMsg`, `statusRequestMsg`, `tickMsg`, `permRequestMsg` — and spawns
goroutines that call the agent/status async, pushing results back via `send`.
Everything else falls through to the pure `model.Update`. The pure model is
testable in isolation by driving it with synthetic `tea.Msg`s.

Layout math lives in two places, kept in sync:

- `View()` decides what to render based on terminal size, welcome card
  presence, completion dropdown, help overlay, and the hint line.
- `WindowSizeMsg` / `resizeInput` size the textarea and viewport to match.

When they drift, you get the resize-overflow bug. Tests assert both paths.

## Theme system

`internal/theme` is global state behind a `sync.RWMutex`. `LoadBuiltins()`
parses 8 embedded YAMLs (`builtins/*.yaml`); `LoadUserDir` reads
`~/.cax/themes/*.yaml`; `Active()/Set()/Cycle()` mutate the registry;
`Resolve(stateFile, cfgName)` picks the startup theme (priority: state.json →
config → default-dark; `default-dark` vs `default-light` chosen via
`termenv.HasDarkBackground()`).

Styles aren't pre-computed; `view.go::styles()` builds a `themedStyles` bag
from `theme.Active()` on every render. Per-frame is cheap, and lets
`/theme dracula` take effect on the very next render. Markdown rendering goes
through a glamour wrapper keyed to `theme.Active().Markdown`.

## Config + state

```
~/.cax/config.yaml      (user-edited; first-run defaults embedded in binary)
~/.cax/memory.db        (sqlite + vec0)
~/.cax/state.json       (theme + future runtime state, atomic write)
~/.cax/plugins.json     (plugin enable/disable, atomic write)
~/.cax/mcp-tokens.json  (OAuth refresh tokens)
~/.cax/{plugins,skills,agents,commands,themes}/...
```

Config resolution: `$CAX_CONFIG` → `./.cax/config.yaml` →
`~/.cax/config.yaml`. First run writes the embedded
`internal/config/example.yaml` to the default path and exits with a "fill in
credentials" message.

## Testing strategy

- **Unit-only by default.** Every package has fakes/mocks; nothing makes
  network calls in `go test ./...`. dive's MCP exposes mockable `Transport`s;
  LSP uses an in-process `jsonrpc2` bidi pipe via `net.Pipe()`; embedders use
  a deterministic hash→vector; the agent tests use scripted
  `llm.StreamingLLM` fakes.
- **Live calls** are gated behind a build tag (none in the tree today, but
  the pattern is there).
- **TUI tests** drive the pure model with synthetic `tea.Msg`s and assert on
  `View()` substrings — no PTY required.
- **`task verify`** is the CI gate: `fmt:check` + `vet` + `golangci-lint run`
  + `go mod tidy` no-op + full test suite. Always green on `main`.

## Operational properties

- **Best-effort everywhere.** Plugin parse error → log + skip. MCP connect
  failure → mark in `ServerInfo.LastError`, others connect. Embedding failure
  → memory not stored, reply still proceeds. Hook timeout → SIGKILL, agent
  loop unaffected.
- **No goroutine leaks in the agent loop.** Streams are read by a single
  reader; pending permission requests live on a buffered chan so the dialog's
  `Show()` always unblocks (ctx-cancellable).
- **One timestamp authority.** Per-turn duration starts at `submit()`, ends
  at `turnDoneMsg`. Session uptime starts at `newModel()`. Both render on
  every tick (every 100ms for the spinner, every 5s for status).
- **`agent.Rebuild` is idempotent.** Multiple `/reload`s in flight serialise
  on the reloader's mutex; the old `*dive.Agent` is replaced atomically.

## What's deliberately *not* there (and why)

| Missing | Why |
|---|---|
| Peer-to-peer handoffs | Fragments memory; the orchestrator + opt-in sub-agents model preserves a single coherent transcript. |
| `dive.Session` | We own history in `memory.Store`; passing `session_id` via `HookContext.Values` is simpler. |
| HTTP API / OpenAI-compat server | The channel layer abstraction is ready, but focus is the personal-assistant TUI for now. |
| Sub-agent persistence across restarts | `orchestration.Runs` is in-memory; cross-session resumability needs a `subagent_runs` table — backlog item ("Dynamic Workflows"). |
| Real plugin signing / verification | Drive-by-install risk noted in `docs/backlog.md`. |

## Where to look (file map)

| Concern | Code |
|---|---|
| Entry point + wiring | `cmd/cax/main.go` |
| Config + first-run write | `internal/config/{config.go,example.yaml,example.go}` |
| Memory (history, summaries, vec0, usage, schedules) | `internal/memory/*.go` |
| Provider chain | `internal/agent/model.go` (`fallbackLLM`, `BuildModel`, `isRetryable`) |
| Bedrock-via-KrakenD | `internal/providers/bedrock/{bedrock.go,stream.go}` |
| Assistant orchestration | `internal/agent/{agent.go,hooks.go,subagents.go}` |
| Plugin manager | `internal/plugins/{manager.go,manifest.go,state.go}` |
| Real MCP wiring | `internal/mcp/mcp.go` |
| LSP client + 6 FuncTools | `internal/lsp/{manager.go,lifecycle.go,tools.go,ext.go}` |
| Hook dispatcher | `internal/hooks/{dispatcher.go,matcher.go,entry.go}` |
| Tools registry + permission + recall | `internal/tools/{registry.go,permission.go,recall.go}` |
| Skills loader | `internal/skills/skills.go` |
| Creator (`/new`, create_*) | `internal/creator/{writer.go,tools.go,wizard.go,reloader.go}` |
| User commands loader | `internal/usercmds/usercmds.go` |
| Scheduler | `internal/scheduler/scheduler.go` |
| Channel abstraction | `internal/channel/channel.go` |
| TUI | `internal/channel/cli/{model.go,view.go,cli.go,commands.go,permission.go,completion.go,welcome.go,gerunds.go,markdown.go,humanize.go,state.go}` |
| Theme registry + state.json | `internal/theme/{theme.go,registry.go,loader.go,state.go,builtins.go,builtins/*.yaml}` |
