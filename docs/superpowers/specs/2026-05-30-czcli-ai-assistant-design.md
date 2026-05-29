# czcli — A nanobot-style AI Assistant in Go

**Date:** 2026-05-30
**Status:** Approved design, ready for implementation planning

## Summary

`czcli` is an ultra-lightweight, long-running personal AI assistant written in Go,
inspired by [nanobot](https://github.com/HKUDS/nanobot) and built on top of the
[dive](https://github.com/deepnoodle-ai/dive) agent framework.

dive already provides nanobot's hard parts — the agent loop, 8+ LLM providers,
tool calling, sessions, hooks, sub-agents, and MCP — so czcli is a **thin
orchestrator** that adds the application layer nanobot is known for: a rich
chat channel, persistent long-term memory, multi-provider fallback, and a
scheduler. The result keeps nanobot's "ultra-lightweight, readable, hackable"
spirit.

## Goals

- A deployable, long-running personal assistant with a coherent personality and memory.
- A TUI channel that, at all times, shows what the agent is doing: active model,
  context/compaction state, token usage over time, memory size, available tools and sub-agents.
- Persistent memory: conversation history + rolling summarization + semantic recall.
- Multi-provider chat with automatic fallback, plus pluggable embeddings.
- A pluggable channel layer so Telegram/Discord/Slack can be added later without core changes.

## Non-goals

- Peer-to-peer agent handoffs (Swarm/OpenAI-style control transfer). Not native to dive
  and fragments memory; we use hierarchical delegation instead.
- Reimplementing what dive already provides (providers, agent loop, tool calling).
- A web UI or OpenAI-compatible HTTP API in the MVP (possible later).

## Key decisions

| Decision | Choice |
|----------|--------|
| Architecture | Thin orchestrator over dive |
| First channel | CLI/TUI (Bubble Tea); channel layer pluggable |
| Memory | Persistent history + summarization + semantic/vector |
| Vector store | SQLite via `modernc.org/sqlite` + `modernc.org/sqlite/vec` (vec0), cgo-free |
| Providers | OpenAI + Bedrock Claude (via KrakenD no-op gateway, bearer auth), ordered fallback; pluggable `Embedder` |
| Tools | WebSearch, WebFetch, file ops, Bash (permission-gated), MCP, `search_memory` |
| Agent model | Single orchestrator + opt-in dive sub-agents (Explore/Plan/custom personas) |
| TUI layout | Top + bottom status bars around a full-width conversation pane |

### Why modernc + vec0 is viable

As of 2026-05-28, `modernc.org/sqlite` ships a companion package
`modernc.org/sqlite/vec` that auto-registers sqlite-vec. Blank-importing both gives
the `vec0` virtual table in a pure-Go, cgo-free build — no native extension loading.

## Architecture

```
   ┌──────────────┐  user text          reply   ┌──────────────┐
   │   Channel    │ ───────────────►  ◄───────── │   Channel    │
   │  (CLI/TUI,   │                               │  (pluggable) │
   │  pluggable)  │                               └──────────────┘
   └──────┬───────┘
          │ Message{sessionID, text}
   ┌──────▼─────────────────────────────────────────────────┐
   │                     Agent Core                          │
   │  dive.Agent  (Model = fallback.LLM over N providers)    │
   │   ├─ Tools: WebSearch, WebFetch, file ops,              │
   │   │         Bash(perm-gated), MCP toolset,              │
   │   │         search_memory, Agent (sub-agent spawn)      │
   │   ├─ Hooks:                                             │
   │   │    PreGeneration  → inject summary + top-k recall   │
   │   │    PreToolUse     → permission gate (bash/write)    │
   │   │    PostGeneration → persist turn + embed + usage    │
   │   └─ dive.Session (live working window)                 │
   └──────┬───────────────────────────────────┬─────────────┘
          │                                    │
   ┌──────▼────────────────┐          ┌────────▼────────┐
   │   Memory (SQLite)      │          │   Scheduler     │
   │  modernc + vec0        │          │  (cron → agent) │
   │  history│summaries│vec │          └─────────────────┘
   │  │usage│schedules      │
   └──────┬─────────────────┘
          │
   ┌──────▼────────┐
   │   Embedder    │  (OpenAI / Ollama, pluggable interface)
   └───────────────┘
```

### Package layout

```
czcli/
├── cmd/czcli/main.go         # load config, wire deps, start channel + scheduler
├── internal/
│   ├── config/               # YAML + env loading; provider list, embed cfg, tools
│   ├── agent/                # builds dive.Agent
│   │   ├── agent.go
│   │   ├── hooks.go          # memory injection + persistence + usage; perm gate
│   │   └── fallback.go       # multi-provider fallback wrapper (implements llm.LLM)
│   ├── providers/
│   │   └── bedrock/          # custom llm.LLM/StreamingLLM: AWS bedrockruntime InvokeModel(+Stream), native Anthropic payload
│   ├── memory/
│   │   ├── store.go          # SQLite schema + migrations (modernc)
│   │   ├── history.go        # message history persistence + working-window load
│   │   ├── summarizer.go     # rolling summarization when token budget exceeded
│   │   ├── semantic.go       # vec0 table; embed + store; KNN recall
│   │   ├── usage.go          # usage rows + 1d/1w/1m rollups; memory-size stats
│   │   └── embedder.go       # Embedder interface + OpenAI/Ollama impls
│   ├── channel/
│   │   ├── channel.go        # Channel interface
│   │   └── cli/              # Bubble Tea TUI implementation
│   ├── tools/
│   │   ├── registry.go       # assemble dive built-ins + MCP + sub-agent tools
│   │   ├── recall.go         # search_memory FuncTool
│   │   └── permission.go     # CLI confirm Dialog + allowlist
│   ├── scheduler/            # cron jobs that run stored prompts through the agent
│   └── mcp/                  # connect MCP servers → dive Toolset (experimental)
├── config.example.yaml
└── go.mod
```

### Channel interface

```go
type Message struct {
    SessionID string
    Text      string
}

type Channel interface {
    // Start runs the channel loop, calling handle for each inbound message
    // and rendering the streamed reply. Blocks until ctx is cancelled.
    Start(ctx context.Context, handle Handler) error
}

type Handler func(ctx context.Context, msg Message, emit EventSink) (Reply, error)
```

The CLI/TUI is the first implementation; Telegram/Discord/Slack implement the same
interface later without touching the agent core.

## Data flow (one message)

1. Channel reads input → `Message{sessionID, text}` to the Agent Core.
2. `agent.CreateResponse(ctx, input, session)`.
3. **PreGenerationHook**: load latest summary + recent history (within `token_budget`);
   embed the query, run vec0 KNN for top-k memories; inject summary + memories as context.
4. Agent loop: LLM generates → may call tools. Bash/Write/Edit pass through the
   **PreToolUseHook** permission gate. The agent may spawn **sub-agents** via the Agent tool.
5. **PostGenerationHook**: persist user+assistant turn to `messages`; embed the turn into
   `memories`/`vec_memories`; record token `usage`; if the working window exceeds
   `token_budget`, summarize the oldest chunk and trim the window.
6. Channel streams the reply and refreshes the status bars.

Memory reads/writes are **best-effort**: recall or embedding failures are logged and
degraded, never blocking a reply.

## Subsystem detail

### Memory (SQLite: modernc + vec0)

Blank-import `modernc.org/sqlite` and `modernc.org/sqlite/vec`. DB at `~/.czcli/memory.db`.

```sql
messages(id, session_id, role, content, token_count, created_at)
summaries(id, session_id, summary_text, covers_up_to_msg_id, created_at)
memories(id, session_id, text, source_msg_id, created_at)
vec_memories USING vec0(memory_id INTEGER PRIMARY KEY, embedding float[N])
usage(id, ts, provider, model, input_tokens, output_tokens, kind)  -- kind ∈ {chat,embedding,summary,subagent}
schedules(id, name, cron_expr, prompt, channel, enabled, last_run)
meta(key, value)  -- embedding model + dim N, schema version
```

- **Working window:** on session start, load the latest summary + recent messages
  within `token_budget`.
- **Summarization:** when the window exceeds `token_budget`, an LLM call (via the
  agent's model) condenses the oldest chunk into a `summaries` row; raw rows remain
  in `messages`. The current summary is always injected at the top of context.
- **Semantic recall:** each turn is embedded and stored; recall = embed query →
  `vec_distance_cosine` KNN → top-`recall_k`.
- **Dimension constraint:** N is fixed at table creation. `meta` records the embedding
  model + dim. Startup fails fast if configured dim ≠ stored dim; switching embedders
  requires a documented re-embed migration (not automatic).

### Multi-provider fallback

`agent/fallback.go` implements dive's `llm.LLM` interface over an ordered list of
providers from config. On a retryable error (rate-limit / 5xx / timeout) it advances
to the next provider; if all fail, it returns the last error. The same wrapper is the
agent `Model` and is reused by the summarizer.

dive's `llm.LLM` interface is minimal — `Name()` and `Generate(ctx, ...Option) (*Response, error)`,
with optional `StreamingLLM.Stream(...)`. Options arrive as an `llm.Config` carrying
`Messages`, `SystemPrompt`, `Tools`, `ToolChoice`, `MaxTokens`, `Temperature`, thinking/reasoning,
etc. The fallback wrapper and the Bedrock provider both implement this interface, so either
can be an entry in the chain.

**Configured providers (MVP): OpenAI + AWS Bedrock (Claude).** OpenAI uses dive's built-in
`providers/openai`. Bedrock is a custom provider (below), since dive ships no Bedrock provider
(only anthropic, openai, google, grok, mistral, ollama, openrouter, openaicompletions).

### Bedrock (Claude) provider

`internal/providers/bedrock` implements `llm.LLM` and `llm.StreamingLLM` for Bedrock Claude
**through the KrakenD AI Gateway**, not by calling AWS directly.

**Topology:** `czcli → KrakenD AI Gateway (no-op passthrough) → Amazon Bedrock`. KrakenD's
no-op encoding forwards request/response bytes unchanged, so the provider speaks Bedrock's
**`InvokeModel`** / **`InvokeModelWithResponseStream`** wire format with the **native Anthropic
Messages payload** (`anthropic_version: bedrock-2023-05-31`) — the same path the Anthropic SDK's
Bedrock client and Claude Code use. Chosen over Converse because it unlocks Anthropic-native
features (prompt caching, extended thinking, beta headers) and lets us reuse dive's anthropic
serialization.

- **Transport:** a plain HTTPS client to the configured **KrakenD base URL** (no AWS SDK service
  client / SigV4 needed, since the gateway terminates the AWS side). The model ID and
  invoke-vs-stream selection follow the gateway's endpoint path template (a config value, since it
  mirrors Bedrock's `/model/{id}/invoke` and `/model/{id}/invoke-with-response-stream`).
- **Payload reuse:** build the body from dive's exported `providers/anthropic` `Request` type
  (and reuse its response/stream-event types where exported, else replicate the small subset), so
  the `Messages`/`SystemPrompt`/`Tools`/`ToolChoice`/reasoning mapping is shared with dive's
  direct-Anthropic provider. `model` is omitted from the body; `anthropic_version` is injected.
- **Non-streaming:** POST to the invoke endpoint → parse the Anthropic Messages response JSON into
  `llm.Response`.
- **Streaming:** POST to the invoke-with-response-stream endpoint → the response is AWS
  **event-stream framing** (passed through by no-op KrakenD). Decode it with
  `github.com/aws/aws-sdk-go-v2/aws/protocol/eventstream`; each frame's payload is Anthropic
  streaming-event JSON (`message_start`, `content_block_delta`, …) which we map to dive
  `StreamIterator` events. (This framing is why dive's SSE-based anthropic parser can't be reused
  for streaming.)
- **Usage:** the Anthropic response / `message_delta` usage fields populate `llm.Usage`
  (`InputTokens`/`OutputTokens`, plus cache tokens), so dashboard token tracking is uniform.
- **Auth/config:** an API key sent in the **`x-api-key`** header (the Anthropic / Claude Code auth
  header) — not `Authorization: Bearer`, and no SigV4 access-key/secret. The key is read from a
  configurable env var. Config supplies the KrakenD `base_url`, the `token_env`, and the
  Bedrock/inference-profile model ID (e.g. `us.anthropic.claude-...`).
- **Testing:** the HTTP transport is wrapped behind a small interface and mocked; unit tests assert
  request-body mapping (native Anthropic shape, no `model`, with `anthropic_version`), response/usage
  parsing, and event-stream decoding — using captured KrakenD/Bedrock fixtures, no live calls.

### Tools, permissions & sub-agents

- **Built-ins:** WebSearch, WebFetch, Read, Write, Edit, Glob, Grep, Bash.
- **Permission gate:** `PreToolUseHook` intercepts Bash/Write/Edit and prompts via a
  CLI confirm `Dialog` (or a config allowlist). Denial returns a "permission denied"
  ToolResult so the model can adapt rather than crash.
- **Sub-agents:** `toolkit/orchestration.NewAgentTool` + `NewTaskStopTool`; catalog =
  `subagent.GeneralPurpose/Explore/Plan` merged with `FileLoader` over `.dive/agents/*.md`.
  Sub-agents inherit a filtered tool slice, run in isolated context, return one final
  message, and cannot spawn sub-agents. **Only the parent turn is persisted/embedded**,
  keeping long-term memory coherent. Config flag enables/disables.
- **MCP:** `internal/mcp` connects configured servers and exposes them as a dive
  `Toolset` (dive MCP is experimental — flagged as such).
- **Recall tool:** `search_memory(query, k)` FuncTool for explicit deep searches beyond
  the automatic injection.

### Scheduler

`robfig/cron` reads the `schedules` table; each job runs a stored prompt through the
agent and routes output to a channel. MVP: schedules defined in config and persisted to
the table; CRUD via CLI slash-commands (`/schedule ...`). Job failures are logged and
do not stop the scheduler.

### CLI/TUI channel

**Framework:** Bubble Tea + Lipgloss + Bubbles. Layout = top status bar + full-width
conversation + bottom status bar + input line:

```
┌────────────────────────────────────────┐
│ claude-opus ✓ │ ctx 6.1k/8k ▓▓▓░ 76% ⚠  │
├────────────────────────────────────────┤
│ you: hey                               │
│ bot: hi! how can I help?               │
│ ...                                    │
├────────────────────────────────────────┤
│ tok 1d124k 1w812k 1m3.2M·mem18MB·🔧8 🤖3│
├────────────────────────────────────────┤
│ > _                                    │
└────────────────────────────────────────┘
```

Always-on status content:

- **Model** — active `provider:model`, fallback indicator (`✓ primary` / `⚠ fallback #2`).
- **Context / compaction gauge** — working-window tokens vs `token_budget` as a bar + %,
  turning amber/red near the summarization threshold so compaction is visible before it happens.
- **Token usage** — input+output totals for **1d / 1w / 1m**, from the `usage` table.
- **Memory** — DB file size + counts (messages, stored vectors).
- **Tools** — count (and which fired last turn via `/tools`).
- **Sub-agents** — available personas + running background sub-agents with a live spinner.

**Interaction:** live updates via Bubble Tea messages as the agent streams; slash-commands
`/stats`, `/tools`, `/agents`, `/schedule`, `/model` for detail and control.

**Usage capture:** every model call (chat, embedding, summary, sub-agent) writes a `usage`
row; the dashboard aggregates `ts >= now-window`. The context gauge reads the live
working-window token count; memory size = DB file size + `COUNT(*)`. We verify dive surfaces
per-response and per-sub-agent token usage during implementation; where a path does not,
we estimate via a tokenizer.

## Configuration

`config.example.yaml`:

```yaml
providers:                       # ordered = fallback priority
  # bedrock via KrakenD AI Gateway (no-op passthrough); native Anthropic payload; key sent as x-api-key
  - {name: bedrock, model: us.anthropic.claude-..., base_url: https://krakend.internal/bedrock, token_env: CZCLI_BEDROCK_API_KEY}
  - {name: openai,  model: gpt-...,                 api_key_env: OPENAI_API_KEY}
embeddings: {provider: openai, model: text-embedding-3-small, dim: 1536, api_key_env: OPENAI_API_KEY}
#   embeddings alt: {provider: bedrock, model: amazon.titan-embed-text-v2:0, dim: 1024, base_url: https://krakend.internal/bedrock, token_env: CZCLI_BEDROCK_API_KEY}
memory:     {db_path: ~/.czcli/memory.db, token_budget: 8000, recall_k: 5}
tools:      {bash_enabled: true, require_confirm: true}
subagents:  {enabled: true, dir: .dive/agents}
mcp:        {servers: []}
schedules:  []
```

## Error handling

- Provider errors → fallback chain; all-fail → graceful message to the channel.
- Memory is best-effort: recall/embed failures log via `log/slog` and degrade.
- Embedder write failure → history still saved, embedding skipped.
- Tool denial → ToolResult, not a crash.
- Scheduler job failure → log + continue.

## Testing

- **memory:** temp SQLite; test history load, summarization trigger, and vec0 KNN
  ordering with a deterministic fake embedder (hash → vector).
- **fallback:** table-driven with fake LLMs that error/succeed to verify chain order.
- **usage:** insert rows across timestamps; assert 1d/1w/1m rollups.
- **hooks:** verify injected context and persisted rows.
- **channel/scheduler:** scripted stdin/stdout; cron dispatch with a fake clock + fake agent.
- Live provider calls are gated behind a build tag/env and excluded from unit runs.

## Open items to verify during implementation

1. KrakenD gateway specifics: the exact endpoint path template for invoke vs.
   invoke-with-response-stream. (Confirmed: KrakenD streams the event-stream body incrementally —
   no buffering — and the key is sent in the `x-api-key` header.) Also which dive
   `providers/anthropic` types (`Request`, response, stream events) are exported for reuse vs. need
   replicating, and pinning `aws-sdk-go-v2/aws/protocol/eventstream` for frame decoding.
2. Whether dive surfaces per-sub-agent token usage (top-level response `Usage` is confirmed;
   if a path doesn't, estimate via a tokenizer).
3. dive MCP API surface (experimental) and version pinning.
4. `modernc.org/sqlite/vec` API specifics (table creation, `vec_f32`, distance functions).
