# czcli — Shared Contracts (read before any plan)

This document pins the cross-cutting Go types, package boundaries, and dependencies that
**all five implementation plans must use verbatim**. If a plan needs a type defined here, it
references this file rather than redefining it. The plan that *owns* a type (column "Defined in")
is responsible for creating it; other plans import it.

Spec: `docs/superpowers/specs/2026-05-30-czcli-ai-assistant-design.md`

## Module & toolchain

- Module path: `github.com/caxqueiroz/czcli`
- Go: `1.25` (dive v1.5.0 and its sub-modules declare `go 1.25`; a consumer's `go` directive must be ≥ its deps')
- Logging: `log/slog` (structured). Acronyms capitalized (`userID`, `dbURL`).
- Errors wrapped with `fmt.Errorf("context: %w", err)`. Early returns over deep nesting.

## Dependencies (pinned at implementation time)

| Dependency | Use | Plan |
|---|---|---|
| `github.com/deepnoodle-ai/dive` | agent loop, `llm`, tools, hooks | 1,3 |
| `github.com/deepnoodle-ai/dive/llm` | `llm.LLM`, `llm.StreamingLLM`, `llm.Response`, `llm.Usage`, options | 1,3 |
| `github.com/deepnoodle-ai/dive/providers/openai` | OpenAI chat provider | 1 |
| `github.com/deepnoodle-ai/dive/providers/anthropic` | reuse `Request`/response/stream types for Bedrock body | 1 |
| `github.com/deepnoodle-ai/dive/toolkit` | built-in tools (Read/Write/Edit/Glob/Grep/Bash/WebSearch/WebFetch) | 3 |
| `github.com/deepnoodle-ai/dive/subagent` | sub-agent personas | 3 |
| `github.com/deepnoodle-ai/dive/toolkit/orchestration` | `NewAgentTool`, `NewTaskStopTool`, `NewRuns` | 3 |
| `modernc.org/sqlite` (blank import) | cgo-free SQLite driver (`sql.Open("sqlite", ...)`) | 2 |
| `modernc.org/sqlite/vec` (blank import) | registers `vec0` virtual table | 2 |
| `github.com/aws/aws-sdk-go-v2/aws/protocol/eventstream` | decode Bedrock stream framing | 1 |
| `github.com/charmbracelet/bubbletea` + `lipgloss` + `bubbles` | TUI | 4 |
| `github.com/robfig/cron/v3` | scheduler | 5 |
| `gopkg.in/yaml.v3` | config | 1 |

> The agent's `Model` is a `dive.AgentOptions.Model` which accepts an `llm.LLM`. dive's
> `llm.LLM` interface is: `Name() string` and `Generate(ctx, ...llm.Option) (*llm.Response, error)`;
> `llm.StreamingLLM` adds `Stream(ctx, ...llm.Option) (llm.StreamIterator, error)`. Verify exact
> signatures against the installed dive version in Plan 1, Task 1.

## Package layout (final)

```
czcli/
├── cmd/czcli/main.go                 # Plan 3 (basic) → Plan 4 (TUI) → Plan 5 (scheduler) wire-up
├── internal/
│   ├── config/        config.go      # Plan 1
│   ├── providers/
│   │   └── bedrock/   bedrock.go, stream.go, bedrock_test.go   # Plan 1
│   ├── agent/         model.go (BuildModel+fallback), agent.go, hooks.go  # model.go=Plan1, rest=Plan3
│   ├── memory/        store.go, history.go, summarizer.go, semantic.go, usage.go, embedder.go  # Plan 2
│   ├── channel/       channel.go (interfaces)   # Plan 1 (interfaces only), cli/ (Plan 4)
│   ├── tools/         registry.go, recall.go, permission.go   # Plan 3
│   └── scheduler/     scheduler.go   # Plan 5
├── config.example.yaml               # Plan 1
└── go.mod
```

> `internal/agent/model.go` (BuildModel + fallback wrapper) is owned by **Plan 1**.
> `internal/agent/agent.go` + `hooks.go` (the dive.Agent assembly) are owned by **Plan 3**.
> `internal/channel/channel.go` (interfaces below) is owned by **Plan 1** so Plans 3 & 4 can import it.

## Config types — `internal/config` (Defined in: Plan 1)

```go
package config

type Config struct {
	Persona    string           `yaml:"persona"`
	Providers  []ProviderConfig `yaml:"providers"`
	Embeddings EmbeddingsConfig `yaml:"embeddings"`
	Memory     MemoryConfig     `yaml:"memory"`
	Tools      ToolsConfig      `yaml:"tools"`
	Subagents  SubagentsConfig  `yaml:"subagents"`
	MCP        MCPConfig        `yaml:"mcp"`
	Schedules  []ScheduleConfig `yaml:"schedules"`
}

type ProviderConfig struct {
	Name      string `yaml:"name"`        // "bedrock" | "openai"
	Model     string `yaml:"model"`
	BaseURL   string `yaml:"base_url"`    // bedrock: KrakenD endpoint
	TokenEnv  string `yaml:"token_env"`   // bedrock: env var holding the x-api-key value
	APIKeyEnv string `yaml:"api_key_env"` // openai: env var holding the API key
	MaxTokens int    `yaml:"max_tokens"`  // default 4096 if 0
}

type EmbeddingsConfig struct {
	Provider  string `yaml:"provider"`    // "openai" | "bedrock"
	Model     string `yaml:"model"`
	Dim       int    `yaml:"dim"`
	BaseURL   string `yaml:"base_url"`
	APIKeyEnv string `yaml:"api_key_env"`
	TokenEnv  string `yaml:"token_env"`
}

type MemoryConfig struct {
	DBPath      string `yaml:"db_path"`      // "~" expanded
	TokenBudget int    `yaml:"token_budget"` // default 8000
	RecallK     int    `yaml:"recall_k"`     // default 5
}

type ToolsConfig struct {
	WebEnabled     bool `yaml:"web_enabled"`
	FilesEnabled   bool `yaml:"files_enabled"`
	BashEnabled    bool `yaml:"bash_enabled"`
	RequireConfirm bool `yaml:"require_confirm"`
}

type SubagentsConfig struct {
	Enabled bool   `yaml:"enabled"`
	Dir     string `yaml:"dir"` // default ".dive/agents"
}

type MCPConfig struct {
	Servers []MCPServerConfig `yaml:"servers"`
}

type MCPServerConfig struct {
	Name    string            `yaml:"name"`
	Command string            `yaml:"command"`
	Args    []string          `yaml:"args"`
	URL     string            `yaml:"url"`
	Env     map[string]string `yaml:"env,omitempty"`     // stdio env vars; also lets plugin .mcp.json env pass through
	Headers map[string]string `yaml:"headers,omitempty"` // HTTP headers; also lets plugin .mcp.json headers pass through
}

type ScheduleConfig struct {
	Name    string `yaml:"name"`
	Cron    string `yaml:"cron"`
	Prompt  string `yaml:"prompt"`
	Channel string `yaml:"channel"`
	Enabled bool   `yaml:"enabled"`
}

// Load reads YAML from path, applies defaults, expands "~" in Memory.DBPath, validates.
func Load(path string) (*Config, error)
```

## Provider layer (Defined in: Plan 1)

`internal/providers/bedrock`:
```go
package bedrock

// Provider implements llm.LLM and llm.StreamingLLM, talking to Bedrock through
// the KrakenD no-op gateway with the native Anthropic Messages payload.
type Provider struct { /* baseURL, model, apiKey, httpClient, maxTokens */ }

type Option func(*Provider)
func WithBaseURL(u string) Option
func WithModel(m string) Option
func WithAPIKey(k string) Option       // value sent in the x-api-key header
func WithHTTPClient(c *http.Client) Option
func WithMaxTokens(n int) Option

func New(opts ...Option) *Provider
func (p *Provider) Name() string                                              // "bedrock"
func (p *Provider) Generate(ctx context.Context, opts ...llm.Option) (*llm.Response, error)
func (p *Provider) Stream(ctx context.Context, opts ...llm.Option) (llm.StreamIterator, error)
```

`internal/agent/model.go`:
```go
package agent

// fallbackLLM implements llm.StreamingLLM over an ordered list; advances to the
// next provider on retryable errors (HTTP 429/5xx, timeouts, context-independent net errors).
type fallbackLLM struct { providers []llm.StreamingLLM }
func (f *fallbackLLM) Name() string
func (f *fallbackLLM) Generate(ctx context.Context, opts ...llm.Option) (*llm.Response, error)
func (f *fallbackLLM) Stream(ctx context.Context, opts ...llm.Option) (llm.StreamIterator, error)

// BuildModel constructs each configured provider and wraps them in fallback order.
func BuildModel(cfg *config.Config) (llm.StreamingLLM, error)

// isRetryable reports whether an error should trigger fallback to the next provider.
func isRetryable(err error) bool

// fallbackLLM also reports which provider served the most recent call, for the
// dashboard's fallback indicator. Active() returns the 0-based index in the chain
// and the provider name; index 0 == primary.
func (f *fallbackLLM) Active() (index int, name string)

// ActiveReporter is the interface agent.Status type-asserts BuildModel's result to,
// to populate channel.Status.OnFallback / FallbackIndex / Model.
type ActiveReporter interface{ Active() (index int, name string) }
```

## Memory layer — `internal/memory` (Defined in: Plan 2)

```go
package memory

type Role string
const ( RoleUser Role = "user"; RoleAssistant Role = "assistant"; RoleSystem Role = "system"; RoleTool Role = "tool" )

type Message struct {
	ID        int64
	SessionID string
	Role      Role
	Content   string
	Tokens    int
	CreatedAt time.Time
}

type Recalled struct {
	Text      string
	Distance  float64
	CreatedAt time.Time
}

type UsageKind string
const ( UsageChat UsageKind = "chat"; UsageEmbedding UsageKind = "embedding"; UsageSummary UsageKind = "summary"; UsageSubagent UsageKind = "subagent" )

type UsageTotals struct { InputTokens, OutputTokens int }
type UsageRollup struct { Day, Week, Month UsageTotals }

type Stats struct {
	DBSizeBytes  int64
	MessageCount int
	MemoryCount  int
}

// Embedder is pluggable; implementations live in embedder.go.
type Embedder interface {
	Embed(ctx context.Context, texts []string) ([][]float32, error)
	Dim() int
	Model() string
}

// Summarizer condenses old messages; implemented in Plan 3 by the agent's model.
type Summarizer interface {
	Summarize(ctx context.Context, msgs []Message) (string, error)
}

// EstimateTokens is the shared approximate tokenizer (len/4 + 1). Good enough for
// budgeting/usage display; swap for a real tokenizer later if needed.
func EstimateTokens(s string) int

// NewEmbedder builds an Embedder from config (provider "openai" | "bedrock").
// This is the single constructor other packages (e.g. cmd/czcli/main.go) call.
func NewEmbedder(cfg config.EmbeddingsConfig) (Embedder, error)

type Store struct { /* db *sql.DB, embedder Embedder, dim int */ }

func Open(cfg config.MemoryConfig, embedder Embedder) (*Store, error) // open, migrate, verify dim vs meta
func (s *Store) Close() error

func (s *Store) AppendMessage(ctx context.Context, m Message) (int64, error)
func (s *Store) LoadWindow(ctx context.Context, sessionID string, tokenBudget int) (summary string, msgs []Message, err error)
func (s *Store) MaybeSummarize(ctx context.Context, sessionID string, sum Summarizer, tokenBudget int) error
func (s *Store) AddMemory(ctx context.Context, sessionID, text string, sourceMsgID int64) error
func (s *Store) Recall(ctx context.Context, sessionID, query string, k int) ([]Recalled, error)
func (s *Store) RecordUsage(ctx context.Context, provider, model string, in, out int, kind UsageKind) error
func (s *Store) UsageRollups(ctx context.Context) (UsageRollup, error)
func (s *Store) Stats(ctx context.Context) (Stats, error)

// Schedule persistence (used by Plan 5). ScheduleConfig is the config type.
func (s *Store) ListSchedules(ctx context.Context) ([]config.ScheduleConfig, error)
func (s *Store) UpsertSchedule(ctx context.Context, sc config.ScheduleConfig) error
func (s *Store) SetLastRun(ctx context.Context, name string, t time.Time) error
```

SQLite schema (created by `Open` migration in Plan 2):
```sql
CREATE TABLE IF NOT EXISTS messages (
  id INTEGER PRIMARY KEY AUTOINCREMENT, session_id TEXT NOT NULL, role TEXT NOT NULL,
  content TEXT NOT NULL, token_count INTEGER NOT NULL, created_at TIMESTAMP NOT NULL);
CREATE TABLE IF NOT EXISTS summaries (
  id INTEGER PRIMARY KEY AUTOINCREMENT, session_id TEXT NOT NULL, summary_text TEXT NOT NULL,
  covers_up_to_msg_id INTEGER NOT NULL, created_at TIMESTAMP NOT NULL);
CREATE TABLE IF NOT EXISTS memories (
  id INTEGER PRIMARY KEY AUTOINCREMENT, session_id TEXT NOT NULL, text TEXT NOT NULL,
  source_msg_id INTEGER, created_at TIMESTAMP NOT NULL);
CREATE VIRTUAL TABLE IF NOT EXISTS vec_memories USING vec0(memory_id INTEGER PRIMARY KEY, embedding float[N]);
CREATE TABLE IF NOT EXISTS usage (
  id INTEGER PRIMARY KEY AUTOINCREMENT, ts TIMESTAMP NOT NULL, provider TEXT, model TEXT,
  input_tokens INTEGER NOT NULL, output_tokens INTEGER NOT NULL, kind TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS schedules (
  id INTEGER PRIMARY KEY AUTOINCREMENT, name TEXT UNIQUE NOT NULL, cron_expr TEXT NOT NULL,
  prompt TEXT NOT NULL, channel TEXT NOT NULL, enabled INTEGER NOT NULL, last_run TIMESTAMP);
CREATE TABLE IF NOT EXISTS meta (key TEXT PRIMARY KEY, value TEXT);
```
`N` = `embedder.Dim()`, written/checked against `meta('embed_model','embed_dim')`; `Open` errors if a stored dim differs.

## Channel layer — `internal/channel` (interfaces Defined in: Plan 1; CLI impl: Plan 4)

```go
package channel

type Message struct { SessionID, Text string }
type Reply struct { Text string }

// StreamEvent is emitted during a turn so channels can render progress live.
type StreamEvent struct {
	Type string // "text" | "tool_start" | "tool_end" | "subagent_start" | "subagent_end" | "error"
	Text string // text delta, or tool/agent name, or error message
}
type EventSink func(ev StreamEvent)

type Handler func(ctx context.Context, msg Message, emit EventSink) (Reply, error)

// Status is the always-on dashboard snapshot the TUI renders.
type Status struct {
	Provider         string
	Model            string
	OnFallback       bool
	FallbackIndex    int
	ContextTokens    int
	ContextBudget    int
	Usage            UsageRollup
	MemSizeBytes     int64
	MessageCount     int
	MemoryCount      int
	ToolNames        []string
	SubagentNames    []string
	RunningSubagents []string
}
// UsageRollup/UsageTotals are duplicated here as plain data to avoid a channel→memory
// import cycle; the agent maps memory.UsageRollup → channel.UsageRollup.
type UsageTotals struct { InputTokens, OutputTokens int }
type UsageRollup struct { Day, Week, Month UsageTotals }

type StatusFunc func(ctx context.Context) (Status, error)

type Channel interface {
	Start(ctx context.Context, handle Handler, status StatusFunc) error
}
```

## Agent core — `internal/agent` (Defined in: Plan 3, except model.go=Plan 1)

```go
package agent

type Assistant struct { /* agent *dive.Agent, store *memory.Store, model llm.StreamingLLM, cfg *config.Config, tools []dive.Tool, runs *orchestration.Runs */ }

// Build assembles the dive.Agent: model (from BuildModel), tools (tools.Registry),
// sub-agents (if enabled), and registers memory hooks (PreGeneration/PreToolUse/PostGeneration).
//
// We do NOT use dive.Session. The dive.Agent is stateless per call; czcli owns conversation
// history in memory.Store. Each turn passes session_id + user_input through the hook context
// (dive HookContext.Values), and the hooks load/inject context and persist results.
func Build(ctx context.Context, cfg *config.Config, store *memory.Store, model llm.StreamingLLM) (*Assistant, error)

// Handle is a channel.Handler: runs one turn through dive with streaming → emit.
func (a *Assistant) Handle(ctx context.Context, msg channel.Message, emit channel.EventSink) (channel.Reply, error)

// Status is a channel.StatusFunc: composes current model/fallback state (via ActiveReporter
// type-assert on the model) + memory stats/usage + tool/subagent names. RunningSubagents is
// read from a mutex-guarded set on Assistant that the Handle event loop updates when it sees
// subagent_start / subagent_end stream events.
func (a *Assistant) Status(ctx context.Context) (channel.Status, error)

// Summarizer returns a memory.Summarizer backed by the agent's model (used by hooks).
func (a *Assistant) Summarizer() memory.Summarizer
```

## Tools — `internal/tools` (Defined in: Plan 3)

```go
package tools

// Registry assembles dive built-in tools per config + the recall tool. Bash/Write/Edit
// are returned as-is; gating happens via the PreToolUse hook + Dialog (permission.go).
//
// MVP web access = WebFetch only. dive's WebSearch tool needs a web.Searcher backend
// (an API like Brave/Tavily/Serper) that the MVP config doesn't carry, so WebSearch is a
// post-MVP follow-up (add a search-provider config + searcher, then include the tool here).
func Registry(cfg config.ToolsConfig, store *memory.Store) ([]dive.Tool, error)

// RecallTool is the search_memory FuncTool: embeds query, KNN, returns formatted snippets.
func RecallTool(store *memory.Store) dive.Tool

// ConfirmDialog implements dive.Dialog: prompts on stdin/stdout (or auto-approves).
func ConfirmDialog(requireConfirm bool, in io.Reader, out io.Writer) dive.Dialog
```

## Scheduler — `internal/scheduler` (Defined in: Plan 5)

```go
package scheduler

type RunFunc func(ctx context.Context, prompt, channel string) error

type Scheduler struct { /* cron *cron.Cron, store *memory.Store, run RunFunc */ }
func New(store *memory.Store, run RunFunc) *Scheduler
func (s *Scheduler) Load(ctx context.Context) error // read schedules table, register cron entries
func (s *Scheduler) Start()
func (s *Scheduler) Stop()
```

## Conventions for all plans

- TDD: failing test → run (see it fail) → minimal impl → run (pass) → commit. One action per step.
- Commit messages follow `type(scope): subject` with `Co-Authored-By:` trailer.
- Tests use a deterministic fake `Embedder` (hash→vector) and fake `llm.StreamingLLM` — never live network in unit tests. Gate any live test behind `//go:build integration`.
- Every code step shows complete code. No placeholders, no "similar to Task N".
- Run tests with `go test ./...`; lint with `golangci-lint run`.
