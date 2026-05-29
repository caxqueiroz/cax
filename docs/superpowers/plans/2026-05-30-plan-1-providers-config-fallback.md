# Plan 1: Providers + Config + Fallback — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build the czcli foundation library: load YAML config, expose a `dive` `llm.StreamingLLM` that talks to OpenAI and to Bedrock-Claude through a KrakenD no-op gateway, with ordered multi-provider fallback — fully unit-tested with mocks (no live network).

**Architecture:** `internal/config` loads/validates YAML into the pinned `Config` structs. `internal/providers/bedrock` is a custom `llm.StreamingLLM` that POSTs the native Anthropic Messages payload (no `model` in body; `anthropic_version` injected; `x-api-key` auth) to a configurable KrakenD base URL — non-streaming returns Anthropic Messages JSON, streaming returns AWS event-stream framing decoded with `aws/protocol/eventstream` (each frame payload is `{"bytes":"<base64 Anthropic event JSON>"}`). `internal/agent/model.go` wraps the OpenAI provider (dive built-in) and the Bedrock provider in a `fallbackLLM` that advances to the next provider on retryable errors. `internal/channel/channel.go` holds the cross-cutting channel contract types only.

**Tech Stack:** `github.com/deepnoodle-ai/dive v1.5.0` (`llm`, `providers/anthropic`), `github.com/deepnoodle-ai/dive/providers/openai v1.5.0` (separate module), `github.com/aws/aws-sdk-go-v2/aws/protocol/eventstream v1.7.10`, `gopkg.in/yaml.v3 v3.0.1`, Go **1.25** (dive v1.5.0 requires `go 1.25.0`; see Assumptions).

---

## Assumptions & contract notes (read before starting)

- **Go version:** the shared contract pins Go `1.24`, but dive v1.5.0's root `go.mod` declares `go 1.25.0` and `providers/openai` declares `go 1.25.0`. A consuming module's `go` directive must be `>=` its dependencies', so this plan uses **`go 1.25`** in `go.mod`. Flag to the team that the contract's `1.24` is incompatible with the pinned dive version.
- **OpenAI provider is a separate module.** Its import path is `github.com/deepnoodle-ai/dive/providers/openai` and it is versioned independently as tag `providers/openai/v1.5.0` (proxy version `v1.5.0`). It must be a distinct `require` line; it pulls in `github.com/openai/openai-go/v3`.
- **Bedrock body must omit `model`.** dive's `anthropic.Request.Model` field is `json:"model"` (no `omitempty`), so marshaling `anthropic.Request` directly always emits `"model":""`. This plan defines a local `bedrockRequest` struct (reusing `llm.Message`, `anthropic.ToolChoice`, `anthropic.Thinking`) that omits `model` and adds `anthropic_version`. The model ID goes in the URL path only.
- **Streaming framing is two layers.** Each AWS event-stream frame's `Payload` is a JSON object `{"bytes":"<base64>"}` (Bedrock chunk wrapper); base64-decoding `bytes` yields the Anthropic streaming-event JSON (`message_start`, `content_block_delta`, ...). That JSON unmarshals directly into dive's `llm.Event` (dive's anthropic provider already unmarshals SSE data into `llm.Event`). `eventstream.Decoder.Decode` returns `io.EOF` at end of stream.
- **Retry classification.** dive's `providers.ProviderError` exposes `StatusCode() int`. `isRetryable` checks for any error implementing `interface{ StatusCode() int }` with status 429/500/503/504/520, plus `net.Error.Timeout()`, plus the bedrock provider's own `*HTTPError` (defined in this plan, also exposing `StatusCode()`). `context.Canceled`/`context.DeadlineExceeded` are NOT retryable.
- **Endpoint path template.** Bedrock paths are `/model/{id}/invoke` and `/model/{id}/invoke-with-response-stream`. The provider builds these from `baseURL` + model. KrakenD forwards them unchanged.

## File Structure

| File | Responsibility |
|---|---|
| `go.mod`, `go.sum` | Module `github.com/caxqueiroz/czcli`, go 1.25; deps for this plan. |
| `internal/config/config.go` | `Config` + sub-structs (verbatim from contracts); `Load(path)` with defaults, `~` expansion, validation. |
| `internal/config/config_test.go` | Load/defaults/validation/`~`-expansion/env-ref tests. |
| `internal/channel/channel.go` | Channel contract types only (Message, Reply, StreamEvent, EventSink, Handler, Status, UsageTotals, UsageRollup, StatusFunc, Channel). No logic. |
| `internal/channel/channel_test.go` | Trivial compile/shape test. |
| `internal/providers/bedrock/bedrock.go` | `Provider` (`llm.LLM`); `New`+`Option`s; `Name`, `Generate`; local request building; `HTTPError`. |
| `internal/providers/bedrock/stream.go` | `Stream` (`llm.StreamingLLM`) + `streamIterator` decoding AWS event-stream → `llm.Event`. |
| `internal/providers/bedrock/bedrock_test.go` | `httptest.Server` tests: request mapping, response/usage parsing, event-stream decoding. |
| `internal/agent/model.go` | `fallbackLLM`, `isRetryable`, `BuildModel`. |
| `internal/agent/model_test.go` | Fake `llm.StreamingLLM` table tests for chain order + non-retryable stop. |
| `config.example.yaml` | Example config matching the spec. |

---

### Task 1: Module init + dependency pinning

**Files:** `go.mod`, `go.sum`

- [ ] Run `cd /Users/cq/Dev/czcli && go mod init github.com/caxqueiroz/czcli` to create `go.mod`.
- [ ] Edit `go.mod` to set the go version and add this plan's requires. Replace the file contents with:

```
module github.com/caxqueiroz/czcli

go 1.25

require (
	github.com/aws/aws-sdk-go-v2/aws/protocol/eventstream v1.7.10
	github.com/deepnoodle-ai/dive v1.5.0
	github.com/deepnoodle-ai/dive/providers/openai v1.5.0
	gopkg.in/yaml.v3 v3.0.1
)
```

- [ ] Run `cd /Users/cq/Dev/czcli && go mod tidy` to resolve and write `go.sum`. Expect it to download dive, the openai provider submodule (pulling `github.com/openai/openai-go/v3`), the AWS eventstream module (pulling `github.com/aws/smithy-go`), and yaml. Expected: command exits 0 and `go.sum` is created.
- [ ] Run `cd /Users/cq/Dev/czcli && go build ./... 2>&1 || true`. Expected: no error from missing modules (there are no packages yet, so output is empty / "no Go files" is acceptable at this stage).
- [ ] Commit:
```
git add go.mod go.sum
git commit -m "$(cat <<'EOF'
chore(deps): init module and pin dive, openai, eventstream, yaml

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

### Task 2: Channel contract types

**Files:** `internal/channel/channel.go`, `internal/channel/channel_test.go`

- [ ] Write failing test `internal/channel/channel_test.go` (verifies the contract types exist with the right fields and that `Handler`/`StatusFunc`/`Channel` are usable):

```go
package channel

import (
	"context"
	"testing"
)

func TestContractTypesCompile(t *testing.T) {
	msg := Message{SessionID: "s1", Text: "hi"}
	if msg.SessionID != "s1" || msg.Text != "hi" {
		t.Fatalf("unexpected message: %+v", msg)
	}

	reply := Reply{Text: "ok"}
	if reply.Text != "ok" {
		t.Fatalf("unexpected reply: %+v", reply)
	}

	var got []StreamEvent
	var sink EventSink = func(ev StreamEvent) { got = append(got, ev) }
	sink(StreamEvent{Type: "text", Text: "delta"})
	if len(got) != 1 || got[0].Type != "text" || got[0].Text != "delta" {
		t.Fatalf("sink did not capture event: %+v", got)
	}

	var h Handler = func(ctx context.Context, m Message, emit EventSink) (Reply, error) {
		emit(StreamEvent{Type: "text", Text: m.Text})
		return Reply{Text: m.Text}, nil
	}
	r, err := h(context.Background(), Message{Text: "echo"}, sink)
	if err != nil || r.Text != "echo" {
		t.Fatalf("handler failed: r=%+v err=%v", r, err)
	}

	status := Status{
		Provider:      "bedrock",
		Model:         "claude",
		OnFallback:    true,
		FallbackIndex: 1,
		ContextTokens: 100,
		ContextBudget: 8000,
		Usage: UsageRollup{
			Day:   UsageTotals{InputTokens: 1, OutputTokens: 2},
			Week:  UsageTotals{InputTokens: 3, OutputTokens: 4},
			Month: UsageTotals{InputTokens: 5, OutputTokens: 6},
		},
		MemSizeBytes:     1024,
		MessageCount:     7,
		MemoryCount:      8,
		ToolNames:        []string{"bash"},
		SubagentNames:    []string{"explore"},
		RunningSubagents: []string{"plan"},
	}
	var sf StatusFunc = func(ctx context.Context) (Status, error) { return status, nil }
	gotStatus, err := sf(context.Background())
	if err != nil || gotStatus.Provider != "bedrock" || gotStatus.Usage.Day.OutputTokens != 2 {
		t.Fatalf("status func failed: %+v err=%v", gotStatus, err)
	}

	var _ Channel = (*noopChannel)(nil)
}

type noopChannel struct{}

func (noopChannel) Start(ctx context.Context, handle Handler, status StatusFunc) error {
	return nil
}
```

- [ ] Run `cd /Users/cq/Dev/czcli && go test ./internal/channel/...`. Expected FAIL: `internal/channel/channel_test.go: ... undefined: Message` (package `channel.go` does not exist yet).
- [ ] Write `internal/channel/channel.go` (contract types verbatim):

```go
// Package channel defines the contract types that connect inbound/outbound
// message channels (CLI/TUI, Telegram, ...) to the agent core. It contains
// only type definitions so the agent (Plan 3) and channel implementations
// (Plan 4) can depend on it without import cycles.
package channel

import "context"

// Message is an inbound message from a channel to the agent.
type Message struct {
	SessionID string
	Text      string
}

// Reply is the final response returned from a turn.
type Reply struct {
	Text string
}

// StreamEvent is emitted during a turn so channels can render progress live.
type StreamEvent struct {
	// Type is one of: "text" | "tool_start" | "tool_end" |
	// "subagent_start" | "subagent_end" | "error".
	Type string
	// Text is a text delta, a tool/agent name, or an error message.
	Text string
}

// EventSink receives StreamEvents during a turn.
type EventSink func(ev StreamEvent)

// Handler processes one inbound message, streaming progress via emit and
// returning the final reply.
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

// UsageTotals is duplicated here as plain data to avoid a channel→memory import
// cycle; the agent maps memory.UsageRollup → channel.UsageRollup.
type UsageTotals struct {
	InputTokens  int
	OutputTokens int
}

// UsageRollup holds token totals over 1d/1w/1m windows.
type UsageRollup struct {
	Day   UsageTotals
	Week  UsageTotals
	Month UsageTotals
}

// StatusFunc returns the current dashboard snapshot.
type StatusFunc func(ctx context.Context) (Status, error)

// Channel runs the channel loop, dispatching inbound messages to handle and
// reading the dashboard via status. Start blocks until ctx is cancelled.
type Channel interface {
	Start(ctx context.Context, handle Handler, status StatusFunc) error
}
```

- [ ] Run `cd /Users/cq/Dev/czcli && go test ./internal/channel/...`. Expected PASS: `ok  github.com/caxqueiroz/czcli/internal/channel`.
- [ ] Commit:
```
git add internal/channel/channel.go internal/channel/channel_test.go
git commit -m "$(cat <<'EOF'
feat(channel): add channel contract types

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

### Task 3: Config types + Load defaults

**Files:** `internal/config/config.go`, `internal/config/config_test.go`

- [ ] Write failing test `internal/config/config_test.go` (defaults applied to a minimal valid config). Use a `t.TempDir()` YAML file:

```go
package config

import (
	"os"
	"path/filepath"
	"testing"
)

func writeYAML(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatalf("write yaml: %v", err)
	}
	return p
}

func TestLoadAppliesDefaults(t *testing.T) {
	path := writeYAML(t, `
providers:
  - {name: openai, model: gpt-5.4, api_key_env: OPENAI_API_KEY}
embeddings: {provider: openai, model: text-embedding-3-small, dim: 1536, api_key_env: OPENAI_API_KEY}
memory: {db_path: /tmp/czcli/memory.db}
`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Memory.TokenBudget != 8000 {
		t.Errorf("TokenBudget default = %d, want 8000", cfg.Memory.TokenBudget)
	}
	if cfg.Memory.RecallK != 5 {
		t.Errorf("RecallK default = %d, want 5", cfg.Memory.RecallK)
	}
	if cfg.Providers[0].MaxTokens != 4096 {
		t.Errorf("provider MaxTokens default = %d, want 4096", cfg.Providers[0].MaxTokens)
	}
	if cfg.Subagents.Dir != ".dive/agents" {
		t.Errorf("Subagents.Dir default = %q, want .dive/agents", cfg.Subagents.Dir)
	}
}
```

- [ ] Run `cd /Users/cq/Dev/czcli && go test ./internal/config/...`. Expected FAIL: `undefined: Load` (no `config.go`).
- [ ] Write `internal/config/config.go` with the contract types and `Load` (defaults + `~` expansion + validation):

```go
// Package config loads and validates czcli's YAML configuration.
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// Config is the top-level czcli configuration.
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

// ProviderConfig configures one LLM provider in fallback order.
type ProviderConfig struct {
	Name      string `yaml:"name"`        // "bedrock" | "openai"
	Model     string `yaml:"model"`
	BaseURL   string `yaml:"base_url"`    // bedrock: KrakenD endpoint
	TokenEnv  string `yaml:"token_env"`   // bedrock: env var holding the x-api-key value
	APIKeyEnv string `yaml:"api_key_env"` // openai: env var holding the API key
	MaxTokens int    `yaml:"max_tokens"`  // default 4096 if 0
}

// EmbeddingsConfig configures the embedding model.
type EmbeddingsConfig struct {
	Provider  string `yaml:"provider"` // "openai" | "bedrock"
	Model     string `yaml:"model"`
	Dim       int    `yaml:"dim"`
	BaseURL   string `yaml:"base_url"`
	APIKeyEnv string `yaml:"api_key_env"`
	TokenEnv  string `yaml:"token_env"`
}

// MemoryConfig configures the SQLite memory store.
type MemoryConfig struct {
	DBPath      string `yaml:"db_path"`      // "~" expanded
	TokenBudget int    `yaml:"token_budget"` // default 8000
	RecallK     int    `yaml:"recall_k"`     // default 5
}

// ToolsConfig toggles built-in tool groups.
type ToolsConfig struct {
	WebEnabled     bool `yaml:"web_enabled"`
	FilesEnabled   bool `yaml:"files_enabled"`
	BashEnabled    bool `yaml:"bash_enabled"`
	RequireConfirm bool `yaml:"require_confirm"`
}

// SubagentsConfig configures sub-agent personas.
type SubagentsConfig struct {
	Enabled bool   `yaml:"enabled"`
	Dir     string `yaml:"dir"` // default ".dive/agents"
}

// MCPConfig lists MCP servers.
type MCPConfig struct {
	Servers []MCPServerConfig `yaml:"servers"`
}

// MCPServerConfig configures one MCP server (stdio or URL).
type MCPServerConfig struct {
	Name    string   `yaml:"name"`
	Command string   `yaml:"command"`
	Args    []string `yaml:"args"`
	URL     string   `yaml:"url"`
}

// ScheduleConfig defines a cron-scheduled prompt.
type ScheduleConfig struct {
	Name    string `yaml:"name"`
	Cron    string `yaml:"cron"`
	Prompt  string `yaml:"prompt"`
	Channel string `yaml:"channel"`
	Enabled bool   `yaml:"enabled"`
}

// Load reads YAML from path, applies defaults, expands "~" in Memory.DBPath,
// and validates the result.
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config %s: %w", path, err)
	}
	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse config %s: %w", path, err)
	}
	applyDefaults(&cfg)
	expanded, err := expandHome(cfg.Memory.DBPath)
	if err != nil {
		return nil, fmt.Errorf("expand db_path: %w", err)
	}
	cfg.Memory.DBPath = expanded
	if err := validate(&cfg); err != nil {
		return nil, err
	}
	return &cfg, nil
}

func applyDefaults(cfg *Config) {
	if cfg.Memory.TokenBudget == 0 {
		cfg.Memory.TokenBudget = 8000
	}
	if cfg.Memory.RecallK == 0 {
		cfg.Memory.RecallK = 5
	}
	if cfg.Subagents.Dir == "" {
		cfg.Subagents.Dir = ".dive/agents"
	}
	for i := range cfg.Providers {
		if cfg.Providers[i].MaxTokens == 0 {
			cfg.Providers[i].MaxTokens = 4096
		}
	}
}

// expandHome replaces a leading "~" with the user's home directory.
func expandHome(p string) (string, error) {
	if p == "" {
		return p, nil
	}
	if p == "~" || strings.HasPrefix(p, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("resolve home dir: %w", err)
		}
		if p == "~" {
			return home, nil
		}
		return filepath.Join(home, p[2:]), nil
	}
	return p, nil
}

func validate(cfg *Config) error {
	if len(cfg.Providers) == 0 {
		return fmt.Errorf("config: at least one provider is required")
	}
	for i, p := range cfg.Providers {
		switch p.Name {
		case "openai":
			if p.APIKeyEnv == "" {
				return fmt.Errorf("config: providers[%d] (openai): api_key_env is required", i)
			}
		case "bedrock":
			if p.BaseURL == "" {
				return fmt.Errorf("config: providers[%d] (bedrock): base_url is required", i)
			}
			if p.TokenEnv == "" {
				return fmt.Errorf("config: providers[%d] (bedrock): token_env is required", i)
			}
		default:
			return fmt.Errorf("config: providers[%d]: unknown name %q (want openai|bedrock)", i, p.Name)
		}
		if p.Model == "" {
			return fmt.Errorf("config: providers[%d] (%s): model is required", i, p.Name)
		}
	}
	if cfg.Embeddings.Provider == "" {
		return fmt.Errorf("config: embeddings.provider is required")
	}
	if cfg.Embeddings.Dim <= 0 {
		return fmt.Errorf("config: embeddings.dim must be > 0")
	}
	if cfg.Memory.DBPath == "" {
		return fmt.Errorf("config: memory.db_path is required")
	}
	return nil
}
```

- [ ] Run `cd /Users/cq/Dev/czcli && go test ./internal/config/...`. Expected PASS: `ok  github.com/caxqueiroz/czcli/internal/config`.
- [ ] Commit:
```
git add internal/config/config.go internal/config/config_test.go
git commit -m "$(cat <<'EOF'
feat(config): add Config types and Load with defaults

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

### Task 4: Config validation, `~` expansion, env-ref tests

**Files:** `internal/config/config_test.go` (append)

- [ ] Append the following failing tests to `internal/config/config_test.go` (validation failures, `~` expansion, and env-var reference resolution via `os.Getenv` on the configured `*_env` name):

```go
func TestLoadValidationErrors(t *testing.T) {
	cases := []struct {
		name string
		body string
		want string
	}{
		{
			name: "no providers",
			body: "embeddings: {provider: openai, dim: 1536}\nmemory: {db_path: /tmp/x.db}\n",
			want: "at least one provider",
		},
		{
			name: "openai missing api_key_env",
			body: "providers:\n  - {name: openai, model: gpt-5.4}\nembeddings: {provider: openai, dim: 1536}\nmemory: {db_path: /tmp/x.db}\n",
			want: "api_key_env is required",
		},
		{
			name: "bedrock missing base_url",
			body: "providers:\n  - {name: bedrock, model: claude, token_env: T}\nembeddings: {provider: openai, dim: 1536}\nmemory: {db_path: /tmp/x.db}\n",
			want: "base_url is required",
		},
		{
			name: "bedrock missing token_env",
			body: "providers:\n  - {name: bedrock, model: claude, base_url: http://k}\nembeddings: {provider: openai, dim: 1536}\nmemory: {db_path: /tmp/x.db}\n",
			want: "token_env is required",
		},
		{
			name: "unknown provider",
			body: "providers:\n  - {name: cohere, model: c}\nembeddings: {provider: openai, dim: 1536}\nmemory: {db_path: /tmp/x.db}\n",
			want: "unknown name",
		},
		{
			name: "provider missing model",
			body: "providers:\n  - {name: openai, api_key_env: K}\nembeddings: {provider: openai, dim: 1536}\nmemory: {db_path: /tmp/x.db}\n",
			want: "model is required",
		},
		{
			name: "embeddings missing provider",
			body: "providers:\n  - {name: openai, model: g, api_key_env: K}\nembeddings: {dim: 1536}\nmemory: {db_path: /tmp/x.db}\n",
			want: "embeddings.provider is required",
		},
		{
			name: "embeddings bad dim",
			body: "providers:\n  - {name: openai, model: g, api_key_env: K}\nembeddings: {provider: openai, dim: 0}\nmemory: {db_path: /tmp/x.db}\n",
			want: "embeddings.dim must be > 0",
		},
		{
			name: "memory missing db_path",
			body: "providers:\n  - {name: openai, model: g, api_key_env: K}\nembeddings: {provider: openai, dim: 1536}\nmemory: {token_budget: 8000}\n",
			want: "memory.db_path is required",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := writeYAML(t, tc.body)
			_, err := Load(path)
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tc.want)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %q, want substring %q", err.Error(), tc.want)
			}
		})
	}
}

func TestLoadExpandsHomeInDBPath(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("UserHomeDir: %v", err)
	}
	path := writeYAML(t, `
providers:
  - {name: openai, model: gpt-5.4, api_key_env: OPENAI_API_KEY}
embeddings: {provider: openai, dim: 1536}
memory: {db_path: ~/.czcli/memory.db}
`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	want := filepath.Join(home, ".czcli", "memory.db")
	if cfg.Memory.DBPath != want {
		t.Fatalf("DBPath = %q, want %q", cfg.Memory.DBPath, want)
	}
	if strings.HasPrefix(cfg.Memory.DBPath, "~") {
		t.Fatalf("DBPath still contains ~: %q", cfg.Memory.DBPath)
	}
}

func TestProviderEnvRefResolves(t *testing.T) {
	t.Setenv("CZCLI_TEST_OPENAI_KEY", "sk-test-123")
	path := writeYAML(t, `
providers:
  - {name: openai, model: gpt-5.4, api_key_env: CZCLI_TEST_OPENAI_KEY}
embeddings: {provider: openai, dim: 1536}
memory: {db_path: /tmp/czcli/memory.db}
`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	got := os.Getenv(cfg.Providers[0].APIKeyEnv)
	if got != "sk-test-123" {
		t.Fatalf("resolved env value = %q, want sk-test-123", got)
	}
}
```

- [ ] Add `import "strings"` to the test file's import block (the new tests use `strings.Contains`). Run `cd /Users/cq/Dev/czcli && go test ./internal/config/...`. Expected PASS (the `Load` impl from Task 3 already satisfies these). If `strings` is reported unused or missing, fix the import and re-run.
- [ ] Commit:
```
git add internal/config/config_test.go
git commit -m "$(cat <<'EOF'
test(config): cover validation, home expansion, and env refs

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

### Task 5: Bedrock provider — request building + non-streaming Generate

**Files:** `internal/providers/bedrock/bedrock.go`, `internal/providers/bedrock/bedrock_test.go`

- [ ] Write failing test `internal/providers/bedrock/bedrock_test.go` (asserts: native Anthropic body shape with NO `model`, with `anthropic_version`; `x-api-key` header; URL path `/model/{id}/invoke`; response + usage parsing). Use `httptest.Server`:

```go
package bedrock

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/deepnoodle-ai/dive/llm"
)

func TestGenerateBuildsNativeAnthropicRequest(t *testing.T) {
	var gotPath, gotAPIKey, gotVersionHeader, gotContentType string
	var gotBody map[string]any

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAPIKey = r.Header.Get("x-api-key")
		gotVersionHeader = r.Header.Get("anthropic-version")
		gotContentType = r.Header.Get("content-type")
		raw, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(raw, &gotBody); err != nil {
			t.Errorf("server: bad body: %v", err)
		}
		w.Header().Set("content-type", "application/json")
		_, _ = io.WriteString(w, `{
			"id": "msg_1",
			"model": "claude",
			"role": "assistant",
			"type": "message",
			"stop_reason": "end_turn",
			"content": [{"type": "text", "text": "hello there"}],
			"usage": {"input_tokens": 11, "output_tokens": 7}
		}`)
	}))
	defer srv.Close()

	p := New(
		WithBaseURL(srv.URL),
		WithModel("us.anthropic.claude-test"),
		WithAPIKey("key-abc"),
		WithMaxTokens(256),
	)
	resp, err := p.Generate(context.Background(),
		llm.WithUserTextMessage("hi"),
		llm.WithSystemPrompt("be brief"),
	)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}

	if gotPath != "/model/us.anthropic.claude-test/invoke" {
		t.Errorf("path = %q, want /model/us.anthropic.claude-test/invoke", gotPath)
	}
	if gotAPIKey != "key-abc" {
		t.Errorf("x-api-key = %q, want key-abc", gotAPIKey)
	}
	if gotVersionHeader != "" {
		// version belongs in the body, not as anthropic-version header here.
		t.Logf("anthropic-version header present: %q", gotVersionHeader)
	}
	if !strings.HasPrefix(gotContentType, "application/json") {
		t.Errorf("content-type = %q, want application/json", gotContentType)
	}
	if _, hasModel := gotBody["model"]; hasModel {
		t.Errorf("body must NOT contain model field, got: %v", gotBody)
	}
	if gotBody["anthropic_version"] != "bedrock-2023-05-31" {
		t.Errorf("anthropic_version = %v, want bedrock-2023-05-31", gotBody["anthropic_version"])
	}
	if gotBody["system"] != "be brief" {
		t.Errorf("system = %v, want 'be brief'", gotBody["system"])
	}
	if gotBody["max_tokens"] != float64(256) {
		t.Errorf("max_tokens = %v, want 256", gotBody["max_tokens"])
	}
	msgs, ok := gotBody["messages"].([]any)
	if !ok || len(msgs) != 1 {
		t.Fatalf("messages = %v, want 1 message", gotBody["messages"])
	}

	if resp.Message().Text() != "hello there" {
		t.Errorf("response text = %q, want 'hello there'", resp.Message().Text())
	}
	if resp.Usage.InputTokens != 11 || resp.Usage.OutputTokens != 7 {
		t.Errorf("usage = %+v, want in=11 out=7", resp.Usage)
	}
}

func TestGenerateReturnsHTTPErrorOnNon200(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = io.WriteString(w, `{"message":"slow down"}`)
	}))
	defer srv.Close()

	p := New(WithBaseURL(srv.URL), WithModel("m"), WithAPIKey("k"))
	_, err := p.Generate(context.Background(), llm.WithUserTextMessage("hi"))
	if err == nil {
		t.Fatal("expected error on 429, got nil")
	}
	var he *HTTPError
	if !asHTTPError(err, &he) {
		t.Fatalf("error is not *HTTPError: %v", err)
	}
	if he.StatusCode() != http.StatusTooManyRequests {
		t.Errorf("status = %d, want 429", he.StatusCode())
	}
}

// asHTTPError is a tiny test helper around errors.As.
func asHTTPError(err error, target **HTTPError) bool {
	for err != nil {
		if he, ok := err.(*HTTPError); ok {
			*target = he
			return true
		}
		type unwrapper interface{ Unwrap() error }
		u, ok := err.(unwrapper)
		if !ok {
			return false
		}
		err = u.Unwrap()
	}
	return false
}

func TestName(t *testing.T) {
	if got := New().Name(); got != "bedrock" {
		t.Errorf("Name() = %q, want bedrock", got)
	}
}
```

- [ ] Run `cd /Users/cq/Dev/czcli && go test ./internal/providers/bedrock/...`. Expected FAIL: `undefined: New` (no `bedrock.go`).
- [ ] Write `internal/providers/bedrock/bedrock.go`:

```go
// Package bedrock implements a dive llm.LLM / llm.StreamingLLM that talks to
// Bedrock Claude through a KrakenD no-op gateway using the native Anthropic
// Messages payload (anthropic_version: bedrock-2023-05-31, no model in the
// body), authenticating via the x-api-key header.
package bedrock

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/deepnoodle-ai/dive/llm"
	"github.com/deepnoodle-ai/dive/providers/anthropic"
)

// ProviderName is the name reported by Name().
const ProviderName = "bedrock"

// anthropicVersion is the value Bedrock requires in the request body for the
// native Anthropic Messages format.
const anthropicVersion = "bedrock-2023-05-31"

const defaultMaxTokens = 4096

var defaultClient = &http.Client{Timeout: 300 * time.Second}

var (
	_ llm.LLM          = (*Provider)(nil)
	_ llm.StreamingLLM = (*Provider)(nil)
)

// Provider talks to Bedrock Claude through KrakenD.
type Provider struct {
	baseURL    string
	model      string
	apiKey     string
	httpClient *http.Client
	maxTokens  int
}

// Option configures a Provider.
type Option func(*Provider)

// WithBaseURL sets the KrakenD base URL (e.g. https://krakend.internal/bedrock).
func WithBaseURL(u string) Option { return func(p *Provider) { p.baseURL = u } }

// WithModel sets the Bedrock/inference-profile model ID used in the URL path.
func WithModel(m string) Option { return func(p *Provider) { p.model = m } }

// WithAPIKey sets the value sent in the x-api-key header.
func WithAPIKey(k string) Option { return func(p *Provider) { p.apiKey = k } }

// WithHTTPClient sets the HTTP client.
func WithHTTPClient(c *http.Client) Option { return func(p *Provider) { p.httpClient = c } }

// WithMaxTokens sets the default max tokens when the call does not specify one.
func WithMaxTokens(n int) Option { return func(p *Provider) { p.maxTokens = n } }

// New creates a Provider with the given options.
func New(opts ...Option) *Provider {
	p := &Provider{
		httpClient: defaultClient,
		maxTokens:  defaultMaxTokens,
	}
	for _, opt := range opts {
		opt(p)
	}
	return p
}

// Name returns the provider name.
func (p *Provider) Name() string { return ProviderName }

// HTTPError is returned for non-2xx responses from the gateway.
type HTTPError struct {
	Status int
	Body   string
}

func (e *HTTPError) Error() string {
	return fmt.Sprintf("bedrock api error (status %d): %s", e.Status, e.Body)
}

// StatusCode exposes the HTTP status for retry classification.
func (e *HTTPError) StatusCode() int { return e.Status }

// bedrockRequest is the native Anthropic Messages body for Bedrock. It omits
// the "model" field (model goes in the URL) and injects anthropic_version.
// Field types reuse dive's anthropic/llm types so mapping stays consistent.
type bedrockRequest struct {
	AnthropicVersion string                `json:"anthropic_version"`
	Messages         []*llm.Message        `json:"messages"`
	MaxTokens        int                   `json:"max_tokens"`
	System           string                `json:"system,omitempty"`
	Temperature      *float64              `json:"temperature,omitempty"`
	Stream           bool                  `json:"stream,omitempty"`
	Tools            []map[string]any      `json:"tools,omitempty"`
	ToolChoice       *anthropic.ToolChoice `json:"tool_choice,omitempty"`
	Thinking         *anthropic.Thinking   `json:"thinking,omitempty"`
}

// buildRequest maps an llm.Config into the Bedrock body.
func (p *Provider) buildRequest(config *llm.Config, stream bool) (*bedrockRequest, error) {
	if len(config.Messages) == 0 {
		return nil, fmt.Errorf("bedrock: no messages provided")
	}
	maxTokens := p.maxTokens
	if config.MaxTokens != nil {
		maxTokens = *config.MaxTokens
	}
	req := &bedrockRequest{
		AnthropicVersion: anthropicVersion,
		Messages:         config.Messages,
		MaxTokens:        maxTokens,
		System:           config.SystemPrompt,
		Temperature:      config.Temperature,
		Stream:           stream,
	}
	if len(config.Tools) > 0 {
		tools := make([]map[string]any, 0, len(config.Tools))
		for _, tool := range config.Tools {
			schema := tool.Schema()
			tc := map[string]any{
				"name":        tool.Name(),
				"description": tool.Description(),
			}
			if schema.Type != "" {
				tc["input_schema"] = schema
			}
			tools = append(tools, tc)
		}
		req.Tools = tools
	}
	if config.ToolChoice != nil && len(config.Tools) > 0 {
		req.ToolChoice = &anthropic.ToolChoice{
			Type: anthropic.ToolChoiceType(config.ToolChoice.Type),
			Name: config.ToolChoice.Name,
		}
		if config.ParallelToolCalls != nil && !*config.ParallelToolCalls {
			req.ToolChoice.DisableParallelToolUse = true
		}
	}
	if config.Thinking == llm.ThinkingTypeAdaptive {
		req.Thinking = &anthropic.Thinking{Type: "adaptive"}
	} else if config.Thinking == llm.ThinkingTypeEnabled && config.ReasoningBudget != nil {
		req.Thinking = &anthropic.Thinking{Type: "enabled", BudgetTokens: *config.ReasoningBudget}
	}
	return req, nil
}

// endpoint builds the gateway URL for the given suffix ("invoke" or
// "invoke-with-response-stream").
func (p *Provider) endpoint(suffix string) string {
	base := strings.TrimRight(p.baseURL, "/")
	return base + "/model/" + url.PathEscape(p.model) + "/" + suffix
}

// newHTTPRequest creates a POST request with the Bedrock/Anthropic headers.
func (p *Provider) newHTTPRequest(ctx context.Context, suffix string, body []byte) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.endpoint(suffix), bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("bedrock: create request: %w", err)
	}
	req.Header.Set("x-api-key", p.apiKey)
	req.Header.Set("content-type", "application/json")
	return req, nil
}

// Generate performs a non-streaming InvokeModel-style call.
func (p *Provider) Generate(ctx context.Context, opts ...llm.Option) (*llm.Response, error) {
	config := &llm.Config{}
	config.Apply(opts...)

	br, err := p.buildRequest(config, false)
	if err != nil {
		return nil, err
	}
	body, err := json.Marshal(br)
	if err != nil {
		return nil, fmt.Errorf("bedrock: marshal request: %w", err)
	}
	req, err := p.newHTTPRequest(ctx, "invoke", body)
	if err != nil {
		return nil, err
	}
	resp, err := p.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("bedrock: do request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp.Body)
		return nil, &HTTPError{Status: resp.StatusCode, Body: string(raw)}
	}

	var result llm.Response
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("bedrock: decode response: %w", err)
	}
	if len(result.Content) == 0 {
		return nil, fmt.Errorf("bedrock: empty response")
	}
	return &result, nil
}
```

- [ ] Run `cd /Users/cq/Dev/czcli && go test ./internal/providers/bedrock/...`. Expected: the streaming test (`Stream`) is not yet present, so only these tests run. Expected PASS for `TestGenerateBuildsNativeAnthropicRequest`, `TestGenerateReturnsHTTPErrorOnNon200`, `TestName`: `ok  github.com/caxqueiroz/czcli/internal/providers/bedrock`.
- [ ] Commit:
```
git add internal/providers/bedrock/bedrock.go internal/providers/bedrock/bedrock_test.go
git commit -m "$(cat <<'EOF'
feat(bedrock): add provider with native Anthropic Generate

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

### Task 6: Bedrock provider — streaming via AWS event-stream decode

**Files:** `internal/providers/bedrock/stream.go`, `internal/providers/bedrock/bedrock_test.go` (append)

- [ ] Append a failing streaming test to `internal/providers/bedrock/bedrock_test.go`. It builds a canned AWS event-stream body (frames whose payload is `{"bytes":"<base64 Anthropic event JSON>"}`) using the same encoder library, serves it, then asserts the decoded `llm.Event` sequence reconstructs the text. Add these imports to the existing test file's import block: `"encoding/base64"`, `"github.com/aws/aws-sdk-go-v2/aws/protocol/eventstream"`.

```go
// encodeChunkFrame wraps Anthropic event JSON the way Bedrock does: a frame
// with :event-type=chunk and a JSON payload {"bytes": base64(eventJSON)}.
func encodeChunkFrame(t *testing.T, eventJSON string) []byte {
	t.Helper()
	wrapper := map[string]string{"bytes": base64.StdEncoding.EncodeToString([]byte(eventJSON))}
	payload, err := json.Marshal(wrapper)
	if err != nil {
		t.Fatalf("marshal wrapper: %v", err)
	}
	msg := eventstream.Message{
		Headers: eventstream.Headers{
			{Name: ":event-type", Value: eventstream.StringValue("chunk")},
			{Name: ":content-type", Value: eventstream.StringValue("application/json")},
			{Name: ":message-type", Value: eventstream.StringValue("event")},
		},
		Payload: payload,
	}
	var buf bytes.Buffer
	if err := eventstream.NewEncoder().Encode(&buf, msg); err != nil {
		t.Fatalf("encode frame: %v", err)
	}
	return buf.Bytes()
}

func TestStreamDecodesEventStream(t *testing.T) {
	frames := [][]byte{
		encodeChunkFrame(t, `{"type":"message_start","message":{"id":"msg_1","model":"claude","role":"assistant","type":"message","stop_reason":"","content":[],"usage":{"input_tokens":5,"output_tokens":0}}}`),
		encodeChunkFrame(t, `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`),
		encodeChunkFrame(t, `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hello"}}`),
		encodeChunkFrame(t, `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":" world"}}`),
		encodeChunkFrame(t, `{"type":"content_block_stop","index":0}`),
		encodeChunkFrame(t, `{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":4}}`),
		encodeChunkFrame(t, `{"type":"message_stop"}`),
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/invoke-with-response-stream") {
			t.Errorf("stream path = %q, want suffix /invoke-with-response-stream", r.URL.Path)
		}
		w.Header().Set("content-type", "application/vnd.amazon.eventstream")
		for _, f := range frames {
			_, _ = w.Write(f)
		}
	}))
	defer srv.Close()

	p := New(WithBaseURL(srv.URL), WithModel("m"), WithAPIKey("k"))
	it, err := p.Stream(context.Background(), llm.WithUserTextMessage("hi"))
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	defer it.Close()

	acc := llm.NewResponseAccumulator()
	var types []llm.EventType
	for it.Next() {
		ev := it.Event()
		types = append(types, ev.Type)
		if err := acc.AddEvent(ev); err != nil {
			t.Fatalf("AddEvent: %v", err)
		}
	}
	if err := it.Err(); err != nil {
		t.Fatalf("iterator err: %v", err)
	}
	if !acc.IsComplete() {
		t.Fatal("accumulator not complete")
	}
	resp := acc.Response()
	if resp.Message().Text() != "Hello world" {
		t.Errorf("text = %q, want 'Hello world'", resp.Message().Text())
	}
	if resp.StopReason != "end_turn" {
		t.Errorf("stop_reason = %q, want end_turn", resp.StopReason)
	}
	if resp.Usage.InputTokens != 5 || resp.Usage.OutputTokens != 4 {
		t.Errorf("usage = %+v, want in=5 out=4", resp.Usage)
	}
	if len(types) == 0 || types[0] != llm.EventTypeMessageStart {
		t.Errorf("first event = %v, want message_start", types)
	}
}

func TestStreamReturnsHTTPErrorOnNon200(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = io.WriteString(w, "down")
	}))
	defer srv.Close()

	p := New(WithBaseURL(srv.URL), WithModel("m"), WithAPIKey("k"))
	_, err := p.Stream(context.Background(), llm.WithUserTextMessage("hi"))
	if err == nil {
		t.Fatal("expected error on 503, got nil")
	}
	var he *HTTPError
	if !asHTTPError(err, &he) || he.StatusCode() != http.StatusServiceUnavailable {
		t.Fatalf("want *HTTPError 503, got %v", err)
	}
}
```

- [ ] Run `cd /Users/cq/Dev/czcli && go test ./internal/providers/bedrock/...`. Expected FAIL: `undefined: (*Provider).Stream` / `undefined: eventstream.NewEncoder` is available (encoder is in the same module). The failure should be about `Stream` not being defined.
- [ ] Write `internal/providers/bedrock/stream.go`:

```go
package bedrock

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"

	"github.com/aws/aws-sdk-go-v2/aws/protocol/eventstream"
	"github.com/deepnoodle-ai/dive/llm"
)

// Stream performs an InvokeModelWithResponseStream-style call and decodes the
// AWS event-stream framing into dive llm.Event values.
func (p *Provider) Stream(ctx context.Context, opts ...llm.Option) (llm.StreamIterator, error) {
	config := &llm.Config{}
	config.Apply(opts...)

	br, err := p.buildRequest(config, true)
	if err != nil {
		return nil, err
	}
	body, err := json.Marshal(br)
	if err != nil {
		return nil, fmt.Errorf("bedrock: marshal request: %w", err)
	}
	req, err := p.newHTTPRequest(ctx, "invoke-with-response-stream", body)
	if err != nil {
		return nil, err
	}
	resp, err := p.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("bedrock: do request: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return nil, &HTTPError{Status: resp.StatusCode, Body: string(raw)}
	}
	return &streamIterator{
		body:    resp.Body,
		decoder: eventstream.NewDecoder(),
	}, nil
}

// streamIterator decodes Bedrock's event-stream framing into llm.Event values.
type streamIterator struct {
	body    io.ReadCloser
	decoder *eventstream.Decoder
	current *llm.Event
	err     error
	done    bool
}

// chunkPayload is the JSON wrapper Bedrock puts in each event-stream frame.
type chunkPayload struct {
	Bytes []byte `json:"bytes"` // base64 in JSON; Go decodes []byte from base64 automatically
}

func (s *streamIterator) Next() bool {
	if s.done {
		return false
	}
	for {
		msg, err := s.decoder.Decode(s.body, nil)
		if err != nil {
			if errors.Is(err, io.EOF) {
				s.done = true
				return false
			}
			s.err = fmt.Errorf("bedrock: decode event-stream frame: %w", err)
			s.done = true
			return false
		}

		// Surface gateway/Bedrock exception frames as errors.
		if mt := msg.Headers.Get(":message-type"); mt != nil && mt.String() == "exception" {
			et := ""
			if h := msg.Headers.Get(":exception-type"); h != nil {
				et = h.String()
			}
			s.err = fmt.Errorf("bedrock: stream exception %q: %s", et, string(msg.Payload))
			s.done = true
			return false
		}

		event, ok, err := decodeFrame(msg.Payload)
		if err != nil {
			s.err = err
			s.done = true
			return false
		}
		if !ok {
			continue // skip frames without a usable event (e.g. pings)
		}
		s.current = event
		return true
	}
}

// decodeFrame unwraps {"bytes": base64(anthropicEventJSON)} into an llm.Event.
// It returns ok=false for frames that do not carry an Anthropic event.
func decodeFrame(payload []byte) (*llm.Event, bool, error) {
	var wrapper chunkPayload
	inner := payload
	if err := json.Unmarshal(payload, &wrapper); err == nil && len(wrapper.Bytes) > 0 {
		inner = wrapper.Bytes
	}
	var event llm.Event
	if err := json.Unmarshal(inner, &event); err != nil {
		// Fall back: the inner bytes may be base64 not auto-decoded; try manual.
		decoded, derr := base64.StdEncoding.DecodeString(string(payload))
		if derr != nil {
			return nil, false, fmt.Errorf("bedrock: unmarshal stream event: %w", err)
		}
		if err := json.Unmarshal(decoded, &event); err != nil {
			return nil, false, fmt.Errorf("bedrock: unmarshal stream event: %w", err)
		}
	}
	if event.Type == "" {
		return nil, false, nil
	}
	return &event, true, nil
}

func (s *streamIterator) Event() *llm.Event { return s.current }

func (s *streamIterator) Err() error { return s.err }

func (s *streamIterator) Close() error { return s.body.Close() }
```

- [ ] Run `cd /Users/cq/Dev/czcli && go test ./internal/providers/bedrock/...`. Expected PASS for all bedrock tests including `TestStreamDecodesEventStream` and `TestStreamReturnsHTTPErrorOnNon200`: `ok  github.com/caxqueiroz/czcli/internal/providers/bedrock`.
- [ ] Commit:
```
git add internal/providers/bedrock/stream.go internal/providers/bedrock/bedrock_test.go
git commit -m "$(cat <<'EOF'
feat(bedrock): decode event-stream framing into llm stream events

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

### Task 7: fallbackLLM + isRetryable

**Files:** `internal/agent/model.go`, `internal/agent/model_test.go`

- [ ] Write failing test `internal/agent/model_test.go` (fake `llm.StreamingLLM` impls verifying: chain advances on retryable error; non-retryable error stops the chain; all-fail returns last error; `Stream` follows the same rules; `isRetryable` classification):

```go
package agent

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"

	"github.com/deepnoodle-ai/dive/llm"
)

// fakeLLM is a scriptable llm.StreamingLLM for fallback tests.
type fakeLLM struct {
	name        string
	genErr      error
	genResp     *llm.Response
	streamErr   error
	stream      llm.StreamIterator
	genCalls    int
	streamCalls int
}

func (f *fakeLLM) Name() string { return f.name }

func (f *fakeLLM) Generate(ctx context.Context, opts ...llm.Option) (*llm.Response, error) {
	f.genCalls++
	if f.genErr != nil {
		return nil, f.genErr
	}
	return f.genResp, nil
}

func (f *fakeLLM) Stream(ctx context.Context, opts ...llm.Option) (llm.StreamIterator, error) {
	f.streamCalls++
	if f.streamErr != nil {
		return nil, f.streamErr
	}
	return f.stream, nil
}

// statusErr is an error carrying an HTTP status code.
type statusErr struct {
	code int
}

func (e *statusErr) Error() string  { return "status error" }
func (e *statusErr) StatusCode() int { return e.code }

func okResp(text string) *llm.Response {
	return &llm.Response{
		Role:    llm.Assistant,
		Content: []llm.Content{&llm.TextContent{Text: text}},
	}
}

func TestFallbackAdvancesOnRetryableError(t *testing.T) {
	p1 := &fakeLLM{name: "p1", genErr: &statusErr{code: 429}}
	p2 := &fakeLLM{name: "p2", genResp: okResp("from p2")}
	f := &fallbackLLM{providers: []llm.StreamingLLM{p1, p2}}

	resp, err := f.Generate(context.Background(), llm.WithUserTextMessage("hi"))
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if resp.Message().Text() != "from p2" {
		t.Errorf("text = %q, want 'from p2'", resp.Message().Text())
	}
	if p1.genCalls != 1 || p2.genCalls != 1 {
		t.Errorf("calls: p1=%d p2=%d, want 1 and 1", p1.genCalls, p2.genCalls)
	}
}

func TestFallbackStopsOnNonRetryableError(t *testing.T) {
	nonRetryable := errors.New("bad request: invalid input")
	p1 := &fakeLLM{name: "p1", genErr: nonRetryable}
	p2 := &fakeLLM{name: "p2", genResp: okResp("from p2")}
	f := &fallbackLLM{providers: []llm.StreamingLLM{p1, p2}}

	_, err := f.Generate(context.Background(), llm.WithUserTextMessage("hi"))
	if !errors.Is(err, nonRetryable) {
		t.Fatalf("err = %v, want the non-retryable error", err)
	}
	if p1.genCalls != 1 {
		t.Errorf("p1 calls = %d, want 1", p1.genCalls)
	}
	if p2.genCalls != 0 {
		t.Errorf("p2 calls = %d, want 0 (chain must stop)", p2.genCalls)
	}
}

func TestFallbackAllFailReturnsLastError(t *testing.T) {
	last := &statusErr{code: 503}
	p1 := &fakeLLM{name: "p1", genErr: &statusErr{code: 429}}
	p2 := &fakeLLM{name: "p2", genErr: last}
	f := &fallbackLLM{providers: []llm.StreamingLLM{p1, p2}}

	_, err := f.Generate(context.Background(), llm.WithUserTextMessage("hi"))
	if !errors.Is(err, last) {
		t.Fatalf("err = %v, want last error %v", err, last)
	}
	if p1.genCalls != 1 || p2.genCalls != 1 {
		t.Errorf("calls: p1=%d p2=%d, want 1 and 1", p1.genCalls, p2.genCalls)
	}
}

func TestFallbackStreamAdvancesOnRetryableError(t *testing.T) {
	p1 := &fakeLLM{name: "p1", streamErr: &statusErr{code: 500}}
	p2 := &fakeLLM{name: "p2", stream: emptyIterator{}}
	f := &fallbackLLM{providers: []llm.StreamingLLM{p1, p2}}

	it, err := f.Stream(context.Background(), llm.WithUserTextMessage("hi"))
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	_ = it.Close()
	if p1.streamCalls != 1 || p2.streamCalls != 1 {
		t.Errorf("stream calls: p1=%d p2=%d, want 1 and 1", p1.streamCalls, p2.streamCalls)
	}
}

func TestFallbackName(t *testing.T) {
	f := &fallbackLLM{providers: []llm.StreamingLLM{&fakeLLM{name: "p1"}, &fakeLLM{name: "p2"}}}
	if got := f.Name(); got != "p1" {
		t.Errorf("Name() = %q, want p1 (first provider)", got)
	}
	if got := (&fallbackLLM{}).Name(); got != "fallback" {
		t.Errorf("empty Name() = %q, want fallback", got)
	}
}

func TestIsRetryable(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"429", &statusErr{code: 429}, true},
		{"500", &statusErr{code: 500}, true},
		{"503", &statusErr{code: 503}, true},
		{"504", &statusErr{code: 504}, true},
		{"520", &statusErr{code: 520}, true},
		{"400", &statusErr{code: 400}, false},
		{"404", &statusErr{code: 404}, false},
		{"plain error", errors.New("boom"), false},
		{"context canceled", context.Canceled, false},
		{"deadline exceeded", context.DeadlineExceeded, false},
		{"net timeout", timeoutErr{}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isRetryable(tc.err); got != tc.want {
				t.Errorf("isRetryable(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

// emptyIterator is a no-op llm.StreamIterator.
type emptyIterator struct{}

func (emptyIterator) Next() bool         { return false }
func (emptyIterator) Event() *llm.Event  { return nil }
func (emptyIterator) Err() error         { return nil }
func (emptyIterator) Close() error       { return nil }

// timeoutErr is a net.Error reporting Timeout() == true.
type timeoutErr struct{}

func (timeoutErr) Error() string   { return "i/o timeout" }
func (timeoutErr) Timeout() bool   { return true }
func (timeoutErr) Temporary() bool { return true }

var _ net.Error = timeoutErr{}
var _ = time.Second // keep time import used if trimmed later
```

- [ ] Run `cd /Users/cq/Dev/czcli && go test ./internal/agent/...`. Expected FAIL: `undefined: fallbackLLM` / `undefined: isRetryable` (no `model.go`).
- [ ] Write the fallback half of `internal/agent/model.go` (BuildModel is added in Task 8; for now define `fallbackLLM` + `isRetryable`):

```go
// Package agent assembles the dive Agent. model.go owns the multi-provider
// model: BuildModel constructs each configured provider and wraps them in an
// ordered fallback chain.
package agent

import (
	"context"
	"errors"
	"net"

	"github.com/deepnoodle-ai/dive/llm"
)

var _ llm.StreamingLLM = (*fallbackLLM)(nil)

// fallbackLLM wraps an ordered list of providers, advancing to the next on a
// retryable error. If all fail, it returns the last error.
type fallbackLLM struct {
	providers []llm.StreamingLLM
}

// Name returns the first provider's name, or "fallback" if empty.
func (f *fallbackLLM) Name() string {
	if len(f.providers) == 0 {
		return "fallback"
	}
	return f.providers[0].Name()
}

// Generate tries each provider in order until one succeeds or a non-retryable
// error occurs.
func (f *fallbackLLM) Generate(ctx context.Context, opts ...llm.Option) (*llm.Response, error) {
	var lastErr error
	for _, p := range f.providers {
		resp, err := p.Generate(ctx, opts...)
		if err == nil {
			return resp, nil
		}
		lastErr = err
		if !isRetryable(err) {
			return nil, err
		}
	}
	if lastErr == nil {
		return nil, errors.New("agent: no providers configured")
	}
	return nil, lastErr
}

// Stream tries each provider in order until one returns a stream or a
// non-retryable error occurs.
func (f *fallbackLLM) Stream(ctx context.Context, opts ...llm.Option) (llm.StreamIterator, error) {
	var lastErr error
	for _, p := range f.providers {
		it, err := p.Stream(ctx, opts...)
		if err == nil {
			return it, nil
		}
		lastErr = err
		if !isRetryable(err) {
			return nil, err
		}
	}
	if lastErr == nil {
		return nil, errors.New("agent: no providers configured")
	}
	return nil, lastErr
}

// isRetryable reports whether an error should trigger fallback to the next
// provider: HTTP 429/5xx (and Cloudflare 520) or a network timeout. Context
// cancellation/deadline errors are not retryable.
func isRetryable(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	var coder interface{ StatusCode() int }
	if errors.As(err, &coder) {
		switch coder.StatusCode() {
		case 429, 500, 502, 503, 504, 520:
			return true
		default:
			return false
		}
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return true
	}
	return false
}
```

- [ ] Run `cd /Users/cq/Dev/czcli && go test ./internal/agent/...`. Expected PASS: `ok  github.com/caxqueiroz/czcli/internal/agent`.
- [ ] Commit:
```
git add internal/agent/model.go internal/agent/model_test.go
git commit -m "$(cat <<'EOF'
feat(agent): add fallbackLLM and isRetryable

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

### Task 8: BuildModel

**Files:** `internal/agent/model.go` (append), `internal/agent/model_test.go` (append)

- [ ] Append failing tests to `internal/agent/model_test.go` (BuildModel constructs providers in fallback order; validates provider names; reads keys from env). Add imports `"os"` and `"github.com/caxqueiroz/czcli/internal/config"` to the test file:

```go
func TestBuildModelOrdersProviders(t *testing.T) {
	t.Setenv("CZCLI_TEST_BEDROCK_KEY", "bk")
	t.Setenv("CZCLI_TEST_OPENAI_KEY", "ok")
	cfg := &config.Config{
		Providers: []config.ProviderConfig{
			{Name: "bedrock", Model: "us.anthropic.claude-x", BaseURL: "https://k/bedrock", TokenEnv: "CZCLI_TEST_BEDROCK_KEY", MaxTokens: 4096},
			{Name: "openai", Model: "gpt-5.4", APIKeyEnv: "CZCLI_TEST_OPENAI_KEY", MaxTokens: 4096},
		},
	}
	model, err := BuildModel(cfg)
	if err != nil {
		t.Fatalf("BuildModel: %v", err)
	}
	fb, ok := model.(*fallbackLLM)
	if !ok {
		t.Fatalf("BuildModel returned %T, want *fallbackLLM", model)
	}
	if len(fb.providers) != 2 {
		t.Fatalf("providers len = %d, want 2", len(fb.providers))
	}
	if fb.providers[0].Name() != "bedrock" {
		t.Errorf("providers[0] = %q, want bedrock", fb.providers[0].Name())
	}
	if fb.providers[1].Name() != "openai" {
		t.Errorf("providers[1] = %q, want openai", fb.providers[1].Name())
	}
}

func TestBuildModelRejectsUnknownProvider(t *testing.T) {
	cfg := &config.Config{
		Providers: []config.ProviderConfig{{Name: "cohere", Model: "c"}},
	}
	if _, err := BuildModel(cfg); err == nil {
		t.Fatal("expected error for unknown provider, got nil")
	}
}

func TestBuildModelRequiresProviders(t *testing.T) {
	if _, err := BuildModel(&config.Config{}); err == nil {
		t.Fatal("expected error for no providers, got nil")
	}
}

var _ = os.Getenv // keep os imported for env-driven helpers above
```

- [ ] Run `cd /Users/cq/Dev/czcli && go test ./internal/agent/...`. Expected FAIL: `undefined: BuildModel`.
- [ ] Append `BuildModel` to `internal/agent/model.go`. Add these imports to the existing import block: `"fmt"`, `"os"`, `"github.com/caxqueiroz/czcli/internal/config"`, `"github.com/caxqueiroz/czcli/internal/providers/bedrock"`, `openaiprovider "github.com/deepnoodle-ai/dive/providers/openai"`. Then add:

```go
// BuildModel constructs each configured provider in order and wraps them in a
// fallback chain. Provider order in config defines fallback priority.
func BuildModel(cfg *config.Config) (llm.StreamingLLM, error) {
	if cfg == nil || len(cfg.Providers) == 0 {
		return nil, fmt.Errorf("agent: at least one provider is required")
	}
	providers := make([]llm.StreamingLLM, 0, len(cfg.Providers))
	for i, pc := range cfg.Providers {
		switch pc.Name {
		case "bedrock":
			providers = append(providers, bedrock.New(
				bedrock.WithBaseURL(pc.BaseURL),
				bedrock.WithModel(pc.Model),
				bedrock.WithAPIKey(os.Getenv(pc.TokenEnv)),
				bedrock.WithMaxTokens(pc.MaxTokens),
			))
		case "openai":
			providers = append(providers, openaiprovider.New(
				openaiprovider.WithModel(pc.Model),
				openaiprovider.WithAPIKey(os.Getenv(pc.APIKeyEnv)),
				openaiprovider.WithMaxTokens(pc.MaxTokens),
			))
		default:
			return nil, fmt.Errorf("agent: providers[%d]: unknown provider %q (want bedrock|openai)", i, pc.Name)
		}
	}
	return &fallbackLLM{providers: providers}, nil
}
```

- [ ] Run `cd /Users/cq/Dev/czcli && go test ./internal/agent/...`. Expected PASS: `ok  github.com/caxqueiroz/czcli/internal/agent`.
- [ ] Run `cd /Users/cq/Dev/czcli && go test ./...`. Expected PASS for all packages: `ok` for `internal/agent`, `internal/channel`, `internal/config`, `internal/providers/bedrock`.
- [ ] Commit:
```
git add internal/agent/model.go internal/agent/model_test.go
git commit -m "$(cat <<'EOF'
feat(agent): add BuildModel wiring openai and bedrock in fallback order

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

### Task 9: config.example.yaml + final verification

**Files:** `config.example.yaml`

- [ ] Write `config.example.yaml` matching the spec (ordered providers = fallback priority; bedrock via KrakenD with `token_env`/`x-api-key`; openai with `api_key_env`):

```yaml
# czcli example configuration.
# Providers are tried in order; the first is primary, the rest are fallbacks.

persona: "A concise, helpful personal assistant."

providers:
  # Bedrock Claude via KrakenD AI Gateway (no-op passthrough).
  # Native Anthropic payload; the API key is sent in the x-api-key header.
  - name: bedrock
    model: us.anthropic.claude-opus-4-7
    base_url: https://krakend.internal/bedrock
    token_env: CZCLI_BEDROCK_API_KEY
    max_tokens: 4096
  # OpenAI fallback (dive built-in provider).
  - name: openai
    model: gpt-5.4
    api_key_env: OPENAI_API_KEY
    max_tokens: 4096

embeddings:
  provider: openai
  model: text-embedding-3-small
  dim: 1536
  api_key_env: OPENAI_API_KEY
  # Bedrock embeddings alternative:
  # provider: bedrock
  # model: amazon.titan-embed-text-v2:0
  # dim: 1024
  # base_url: https://krakend.internal/bedrock
  # token_env: CZCLI_BEDROCK_API_KEY

memory:
  db_path: ~/.czcli/memory.db
  token_budget: 8000
  recall_k: 5

tools:
  web_enabled: true
  files_enabled: true
  bash_enabled: true
  require_confirm: true

subagents:
  enabled: true
  dir: .dive/agents

mcp:
  servers: []

schedules: []
```

- [ ] Verify the example config loads cleanly. Run:
```
cd /Users/cq/Dev/czcli && CZCLI_BEDROCK_API_KEY=x OPENAI_API_KEY=y go run -exec true ./... 2>/dev/null; go test ./internal/config/ -run TestLoadExample -count=1 2>&1 || true
```
Then add a one-off test to `internal/config/config_test.go` that loads the repo example file via a relative path and asserts no error:

```go
func TestLoadExampleConfig(t *testing.T) {
	cfg, err := Load("../../config.example.yaml")
	if err != nil {
		t.Fatalf("Load(config.example.yaml): %v", err)
	}
	if len(cfg.Providers) != 2 {
		t.Fatalf("providers = %d, want 2", len(cfg.Providers))
	}
	if cfg.Providers[0].Name != "bedrock" || cfg.Providers[1].Name != "openai" {
		t.Fatalf("provider order = %q,%q, want bedrock,openai", cfg.Providers[0].Name, cfg.Providers[1].Name)
	}
	if cfg.Memory.TokenBudget != 8000 || cfg.Memory.RecallK != 5 {
		t.Fatalf("memory defaults wrong: %+v", cfg.Memory)
	}
}
```

- [ ] Run `cd /Users/cq/Dev/czcli && go test ./internal/config/... -run TestLoadExampleConfig -count=1`. Expected PASS: `ok  github.com/caxqueiroz/czcli/internal/config`.
- [ ] Run the full suite and vet: `cd /Users/cq/Dev/czcli && go vet ./... && go test ./...`. Expected: `go vet` exits 0, and every package reports `ok`.
- [ ] Commit:
```
git add config.example.yaml internal/config/config_test.go
git commit -m "$(cat <<'EOF'
docs(config): add example config and load test

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Done criteria

- `go test ./...` passes; `go vet ./...` clean.
- `internal/config` loads/validates with defaults, `~` expansion, and env-ref tests green.
- `internal/channel/channel.go` provides the contract types (no logic) and compiles.
- `internal/providers/bedrock` implements `llm.LLM` + `llm.StreamingLLM`: native Anthropic body (no `model`, with `anthropic_version`), `x-api-key` auth, `/model/{id}/invoke` and `/model/{id}/invoke-with-response-stream` paths, and AWS event-stream decoding into `llm.Event`.
- `internal/agent/model.go` provides `fallbackLLM`, `isRetryable`, and `BuildModel` wiring OpenAI (dive built-in) and Bedrock in fallback order.
- `config.example.yaml` matches the spec.
