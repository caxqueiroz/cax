# czcli — Backlog

Deferred features and known gaps from the MVP. Each entry: why it matters, a rough
sketch, and what it depends on. Order is rough priority — adjust as we learn.

## Dynamic Workflows (Claude-Code-style fan-out + verification)

**Why.** Claude Code's "Dynamic Workflows" lets the agent fan out work to many parallel
sub-agents with adversarial verification and iteration to convergence
([announcement](https://claude.com/blog/introducing-dynamic-workflows-in-claude-code)).
czcli already has the primitives: dive's `experimental/toolkit/extended` Task/TaskStop
tools support background sub-agents, the TUI tracks running ones, and memory persists
turns. We can add a meaningful subset without new infrastructure.

**Scope — proposed MVP (subset 1+2+3):**
1. **Fan-out persona.** Update the system prompt / persona so the main agent uses
   the `Task` tool **in parallel** for decomposable work (research, multi-file
   refactors, bug sweeps). No code change — prompt + a couple `.dive/agents/*.md`
   personas (e.g. `explorer`, `researcher`) with focused tool slices.
2. **Verifier sub-agent.** A `.dive/agents/verifier.md` persona whose job is to
   critique the main answer (find counterexamples, missed cases). The orchestrator
   prompt is updated to spawn it on non-trivial answers and iterate if the verifier
   disagrees.
3. **`ultracode` mode.** A config flag (or `/ultra` slash-command) that bumps
   `llm.Config.ReasoningEffort` to `high`/`max` and biases the persona toward
   decomposition. Wire through `internal/agent/agent.go` via an `llm.WithReasoningEffort`
   option on `CreateResponse`.

**Bigger lift (defer):**
- **Cross-session resumability.** Real Claude-Code workflows survive hours/days.
  dive's `Runs`/Task state is in-memory; we'd need to persist sub-agent run state
  (id, parent turn, status, partial output) to a new `subagent_runs` table in
  `memory.Store` and resume on startup. Its own design + plan.

**Dependencies.** None for the MVP subset (only persona files + a small agent.go
change). Resumability depends on a new memory subsystem + a way to reconstruct
dive sub-agent state from persisted records (may require upstream dive support).

---

## MCP servers (real, not the no-op)

**Why.** Spec listed MCP as a tool source; dive v1.5.0 ships no usable MCP module,
so `internal/mcp.Connect` currently logs configured servers and returns no tools.

**Scope.** Either (a) wait for dive to publish a stable `mcp` package and wire it
into `internal/mcp.Connect`, or (b) implement a small stdio MCP client ourselves
that returns dive `Tool` adapters. Config (`MCPConfig.Servers`) already exists.

**Dependencies.** Upstream dive MCP, OR a small in-house MCP client.

---

## WebSearch tool

**Why.** Spec listed web search; dive's `WebSearch` tool needs a `web.Searcher`
backend the MVP config doesn't carry. Currently only `WebFetch` is exposed.

**Scope.** Add a `search` section to config (provider + API key env) for
Brave/Tavily/Serper, implement the dive `web.Searcher` interface, and include the
tool in `tools.Registry` when configured. Cost: one new file + 2–3 config fields.

**Dependencies.** A search-provider API key.

---

## Bedrock embeddings (Titan v2)

**Why.** `memory.NewEmbedder` currently only handles `provider: "openai"`. Spec's
config example shows a Bedrock Titan alternative; calling it returns
"not yet supported".

**Scope.** A `BedrockEmbedder` impl in `internal/memory/embedder.go` that talks to
the KrakenD gateway with the same `x-api-key` auth pattern as the chat provider,
calling Bedrock's embeddings invoke endpoint with the Titan request shape. Test
via `httptest.Server` like the OpenAI embedder.

**Dependencies.** Plan 1's bedrock provider patterns to reuse for HTTP + auth.

---

## Sub-agent personas beyond `GeneralPurpose`

**Why.** dive's `experimental/subagent` only ships `GeneralPurpose`; the spec
mentioned `Explore` and `Plan` patterns. We can supply them as `.dive/agents/*.md`
files (already supported by `subagent.FileLoader`).

**Scope.** Add `.dive/agents/explore.md` (read-only: `Read`/`Glob`/`Grep`) and
`.dive/agents/plan.md` (read-only architectural analysis). No code change.

**Dependencies.** None.

---

## Cross-channel scheduler routing

**Why.** Scheduler's `RunFunc` only carries `(prompt, channel)`, and main.go
currently just logs/prints the reply. The schedule's `Channel` field is parsed
but not routed.

**Scope.** Introduce a `ChannelRegistry` keyed by channel name (`"cli"`,
`"telegram"`, …); the `RunFunc` adapter looks up the channel and dispatches the
reply. Needed once a second channel (e.g. Telegram) exists.

**Dependencies.** A second `channel.Channel` implementation.

---

## Additional channels (Telegram, Discord, Slack)

**Why.** Pluggable channel layer was an explicit design goal; CLI is the only
implementation today.

**Scope.** Each is a new package under `internal/channel/<name>/` implementing
`channel.Channel`. Telegram is the obvious next (bot token + long polling,
minimal infra). Wire selection through config and `main.go`.

**Dependencies.** Per channel: an auth credential and a Go client lib.
