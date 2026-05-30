# cax — Backlog

> The project was renamed czcli → cax on 2026-05-30. The module path, binary,
> config dir (`~/.cax/`), env vars (`CAX_*`), and TUI brand all reflect the new
> name. Historical specs and plans under `docs/superpowers/` retain the old name
> as a record of when they were written.

Deferred features and known gaps. Each entry: why it matters, a rough sketch, and what it
depends on. Order is rough priority — adjust as we learn.

## Shipped recently (delete once it's old news)

- **Skills wiring** — dive's top-level `skill/` package wired via `AgentOptions.Extensions`.
- **MCP servers (real)** — `internal/mcp` now uses dive's `experimental/mcp` with stdio + HTTP + file-backed OAuth `TokenStore`.
- **Plugins (Claude-Code-compatible)** — `~/.cax/plugins/*` discovery + `/plugin install <git-url>` + hot-reload via `agent.Rebuild`.
- **LSP** — `go.lsp.dev` client + 6 language-routed FuncTools (`lsp_definition`/`references`/`hover`/`document_symbols`/`workspace_symbols`/`diagnostics`).
- **Hooks** — declarative shell hooks on `UserPromptSubmit`/`PreToolUse`/`PostToolUse`/`Stop` with JSON envelope + exit-code gate + SIGKILL timeout.
- **Sub-agent personas** — dive v1.7 ships `GeneralPurpose`/`Explore`/`Plan` as built-ins; custom personas via `subagent.FileLoader` already supported.

---

## Dynamic Workflows (Claude-Code-style fan-out + verification)

**Why.** Claude Code's "Dynamic Workflows" lets the agent fan out work to many parallel
sub-agents with adversarial verification and iteration to convergence
([announcement](https://claude.com/blog/introducing-dynamic-workflows-in-claude-code)).
cax already has the primitives: `toolkit/orchestration`'s `Agent` tool supports background
sub-agents, the TUI tracks running ones, and memory persists turns.

**Scope — proposed MVP (subset 1+2+3):**
1. **Fan-out persona.** Update the system prompt / persona so the main agent uses
   the `Agent` tool **in parallel** for decomposable work. No code change — prompt + a
   couple `.dive/agents/*.md` personas (e.g. `explorer`, `researcher`) with focused tool slices.
2. **Verifier sub-agent.** A `.dive/agents/verifier.md` persona whose job is to
   critique the main answer (find counterexamples, missed cases). The orchestrator prompt
   spawns it on non-trivial answers and iterates if the verifier disagrees.
3. **`ultracode` mode.** A config flag (or `/ultra` slash-command) that bumps
   `llm.Config.ReasoningEffort` to `high`/`max` and biases the persona toward decomposition.
   Wire through `internal/agent/agent.go` via `llm.WithReasoningEffort` on `CreateResponse`.

**Bigger lift (defer):**
- **Cross-session resumability.** Real Claude-Code workflows survive hours/days.
  `orchestration.Runs` is in-memory; we'd persist sub-agent run state (id, parent turn,
  status, partial output) to a new `subagent_runs` table in `memory.Store` and resume on
  startup. dive's `experimental/compaction` may help with long-context survival. Its own design + plan.

**Dependencies.** None for the MVP subset. Resumability depends on a new memory subsystem
and a way to reconstruct dive sub-agent state from persisted records (may require upstream
dive support).

---

## Plugin marketplace (`/plugin marketplace add <url>`)

**Why.** Plugins install only by git URL today. Claude Code has a marketplace concept
(`marketplace.json` indexes plugins, `/plugin marketplace add <url>` adds a source,
`/plugin install <name>` resolves through it). Closer parity makes discovery easier.

**Scope.** Add `internal/plugins/marketplace.go`: fetch + cache `marketplace.json`,
implement `MarketplaceAdd`/`MarketplaceList`/`MarketplaceRemove`, and have
`/plugin install <name>` first try the cached marketplaces before treating the arg as a
git URL. State file `~/.cax/marketplaces.json` (atomic write).

**Dependencies.** None.

---

## Plugin slash-command dispatch (`/<plugin>:<command>`)

**Why.** Plan 7 parses each plugin's `commands/*.md` into `Contributions.Commands`
(name + description + prompt + source) but the CLI doesn't dispatch them yet. Plugins
shipping slash commands today are silently ignored.

**Scope.** In `internal/channel/cli/commands.go` route `/<plugin>:<command> [args]`
to the matching `PluginCommand`, run `plugins.ExpandArguments(prompt, args)`, send the
expanded prompt as a user turn through `Handle`. Also surface a `/commands` listing.
Argument-hint frontmatter shown in the listing.

**Dependencies.** None (Plan 7's parser already populates `Contributions.Commands`).

---

## Structured hook output (`hookSpecificOutput` / `decision`)

**Why.** Plan 9 treats hook stdout as plain text feedback. Claude Code defines a JSON
output form (`{"decision": "block|approve|...", "hookSpecificOutput": {...}}`) for
fine-grained control, especially on `PreToolUse` (e.g. transform the tool input).

**Scope.** In `internal/hooks/dispatcher.go`, when stdout is valid JSON with a known
schema, parse `decision` (`block`/`approve`/`continue`) + `hookSpecificOutput` and
honor them; fall back to the current "any non-zero exit blocks; stdout is feedback" if
not. Add per-event schema validation.

**Dependencies.** None.

---

## `$ARGUMENTS` advanced expansion

**Why.** Plan 7 only does whole-string `$ARGUMENTS` substitution. Claude Code supports
`$ARGUMENTS[N]` / `$N` / named arguments via frontmatter — useful for commands that
parse arguments structurally.

**Scope.** Extend `plugins.ExpandArguments` to tokenize args (shlex-style) and substitute
`$1..$N`/`$ARGUMENTS[N]`; honor `arguments:` frontmatter for named placeholders.

**Dependencies.** None.

---

## Plugin signing / verification

**Why.** `/plugin install <git-url>` runs arbitrary code from the network on first use
(via hook shell commands, MCP commands, LSP commands). Signing/verification reduces the
"drive-by" risk and lets teams pin a trusted publisher set.

**Scope.** Verify a detached signature (e.g. `cosign`/`minisign`) of the plugin's
`.claude-plugin/plugin.json` against a configured public-key set; refuse on mismatch.
Per-plugin "trusted" override for local dev.

**Dependencies.** A key-distribution story (signing infra is upstream).

---

## WebSearch tool

**Why.** Spec listed web search; dive's `WebSearch` needs a `web.Searcher` backend the
MVP config doesn't carry. Currently only `WebFetch` is exposed.

**Scope.** Add a `search` section to config (provider + API key env) for
Brave/Tavily/Serper, implement the dive `web.Searcher` interface, include the tool in
`tools.Registry` when configured.

**Dependencies.** A search-provider API key.

---

## Bedrock embeddings (Titan v2)

**Why.** `memory.NewEmbedder` currently only handles `provider: "openai"`. Spec's
example config shows a Bedrock Titan alternative; calling it returns "not yet supported".

**Scope.** A `BedrockEmbedder` in `internal/memory/embedder.go` that talks to KrakenD with
the same `x-api-key` pattern as the chat provider, calling Bedrock's embeddings invoke
endpoint with the Titan request shape. Test via `httptest.Server` like the OpenAI embedder.

**Dependencies.** Plan 1's bedrock provider patterns to reuse.

---

## Cross-channel scheduler routing

**Why.** Scheduler's `RunFunc` carries `(prompt, channel)` and main.go just logs/prints
the reply. The schedule's `Channel` field is parsed but not routed.

**Scope.** A `ChannelRegistry` keyed by channel name (`"cli"`, `"telegram"`, …); the
`RunFunc` adapter looks up the channel and dispatches the reply. Needed once a second
channel exists.

**Dependencies.** A second `channel.Channel` implementation.

---

## Additional channels (Telegram, Discord, Slack)

**Why.** Pluggable channel layer was an explicit design goal; CLI is the only impl today.

**Scope.** Each is a new package under `internal/channel/<name>/` implementing
`channel.Channel`. Telegram is the obvious next (bot token + long polling). Wire selection
through config and `main.go`.

**Dependencies.** Per channel: an auth credential and a Go client lib.
