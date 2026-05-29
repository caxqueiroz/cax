# Plan 3: Agent Core + Tools — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Assemble the dive.Agent over a multi-provider model with memory-injection/persistence hooks, permission-gated built-in tools, a `search_memory` recall tool, opt-in sub-agents, and best-effort MCP, plus a minimal stdin/stdout `main.go` that makes czcli end-to-end usable.

**Architecture:** `internal/agent.Build` wires `dive.NewAgent` (SystemPrompt=Persona, Model=fallback `llm.StreamingLLM` from Plan 1, Tools from `tools.Registry` + optional sub-agent tools, `dive.Hooks` registered). `Assistant.Handle` runs one turn via `(*dive.Agent).CreateResponse(ctx, WithInput, WithEventCallback, WithSession)`, forwarding `*dive.ResponseItem` events to a `channel.EventSink`; the registered hooks load the working window + top-k recall before generation (`PreGeneration`), gate Bash/Write/Edit via a `dive.Dialog` (`PreToolUse`), and persist the turn + embedding + usage + maybe-summarize after (`PostGeneration`). Memory is best-effort — failures log via `slog` and degrade, never blocking a reply.

**Tech Stack:** `github.com/deepnoodle-ai/dive` (`dive`, `llm`, `toolkit`, `subagent`, `toolkit/orchestration`), `github.com/deepnoodle-ai/dive/experimental/mcp` (separate module, experimental), plus Plan 1 (`internal/config`, `internal/agent/model.go`, `internal/channel`) and Plan 2 (`internal/memory`).

---

## Authoritative dive API notes (verified against deepnoodle-ai/dive @ main, 2026-05-30)

These were read from real source; the plan's code compiles against them. Quote them when implementing.

- **Agent construction** — `dive.NewAgent(opts dive.AgentOptions) (*dive.Agent, error)`. Relevant `AgentOptions` fields: `SystemPrompt string`, `Model llm.LLM`, `Tools []dive.Tool`, `Hooks dive.Hooks`, `Logger llm.Logger`, `Name string`. `NewAgent` errors on `nil` Model (`ErrNoLLM`) or duplicate tool names. `Model` accepts any `llm.LLM`; our fallback wrapper is an `llm.StreamingLLM` (superset), so it satisfies it.
- **Turn execution** — `(*dive.Agent).CreateResponse(ctx, ...dive.CreateResponseOption) (*dive.Response, error)`. Options: `dive.WithInput(string)`, `dive.WithEventCallback(dive.EventCallback)`, `dive.WithSession(dive.Session)`, `dive.WithMessages(...*llm.Message)`. `dive.EventCallback = func(ctx context.Context, item *dive.ResponseItem) error`. We pass `WithInput(msg.Text)` and `WithEventCallback(...)`; we do NOT pass a `dive.Session` (we manage history ourselves through hooks + `memory.Store`, keyed by `msg.SessionID` carried in `HookContext.Values`).
- **Streaming events** — text deltas arrive as `item.Type == dive.ResponseItemTypeModelEvent` with `item.Event *llm.Event`; a text delta is `item.Event.Type == llm.EventTypeContentBlockDelta && item.Event.Delta.Type == llm.EventDeltaTypeText` → `item.Event.Delta.Text`. Tool start is `item.Type == dive.ResponseItemTypeToolCall` → `item.ToolCall.Name`. Tool end is `item.Type == dive.ResponseItemTypeToolCallResult` → `item.ToolCallResult.Name`. The callback may run concurrently (parallel tools / tool streaming) — our sink must be safe under a mutex.
- **Final text / usage** — `(*dive.Response).OutputText() string` returns the last assistant text. `resp.Usage *llm.Usage` has `InputTokens`/`OutputTokens` (plus cache fields). This is the confirmed per-response usage source for `RecordUsage`.
- **Hooks** — `dive.Hooks{ PreGeneration []dive.PreGenerationHook, PostGeneration []dive.PostGenerationHook, PreToolUse []dive.PreToolUseHook, ... }`. Each is `func(ctx context.Context, hctx *dive.HookContext) error`. `HookContext` fields used:
  - PreGeneration: `hctx.SystemPrompt` (mutable), `hctx.Messages []*llm.Message` (mutable — prepend context), `hctx.Values map[string]any` (carries our session id).
  - PreToolUse: `hctx.Tool dive.Tool` (`.Name()`), `hctx.Call *llm.ToolUseContent`. Returning a non-`HookAbortError` error denies the tool (sent to the LLM as a deny message); `dive.NewUserFeedback(text)` returns a `*UserFeedbackError` for user-denial feedback.
  - PostGeneration: `hctx.Response *dive.Response`, `hctx.Usage *llm.Usage`, `hctx.Values`. PostGeneration errors are logged by dive and do not affect the returned Response (matches "best-effort memory").
  - Hook helper used: `dive.InjectContext(content ...llm.Content) dive.PreGenerationHook` exists, but we write our own PreGeneration so we can read `hctx.Values` for the session id; we still prepend via `hctx.Messages = append([]*llm.Message{llm.NewUserMessage(...)}, hctx.Messages...)`.
- **Dialog** — `dive.Dialog` interface: `Show(ctx, *dive.DialogInput) (*dive.DialogOutput, error)`. Confirm mode: `in.Confirm == true` → `out.Confirmed`. dive ships `dive.AutoApproveDialog` / `dive.DenyAllDialog`. Our `ConfirmDialog` implements `dive.Dialog`. NOTE: the Dialog is NOT auto-invoked by the agent's PreToolUse — our `PreToolUse` hook drives it explicitly.
- **Tools** — `dive.Tool` interface (`Name/Description/Schema/Annotations/Call`). `dive.FuncTool[T any](name, description string, fn func(ctx, T) (*dive.ToolResult, error), ...opts) dive.Tool` auto-generates the schema from `T` (struct tags `json`/`description`). `dive.NewToolResultText(string) *dive.ToolResult`, `dive.NewToolResultError(string) *dive.ToolResult`.
- **toolkit constructors** (each returns `*dive.TypedToolAdapter[...]`, which is a `dive.Tool`):
  - `toolkit.NewBashTool(opts ...toolkit.BashToolOptions)` → tool name `"Bash"`.
  - `toolkit.NewReadFileTool(opts ...toolkit.ReadFileToolOptions)` → `"Read"`.
  - `toolkit.NewWriteFileTool(opts ...toolkit.WriteFileToolOptions)` → `"Write"`.
  - `toolkit.NewEditTool(opts ...toolkit.EditToolOptions)` → `"Edit"`.
  - `toolkit.NewGlobTool(opts ...toolkit.GlobToolOptions)` → `"Glob"`.
  - `toolkit.NewGrepTool(opts ...toolkit.GrepToolOptions)` → `"Grep"`.
  - `toolkit.NewFetchTool(opts ...toolkit.FetchToolOptions)` → `"WebFetch"` (self-sufficient: default SSRF-safe HTTP fetcher when no Fetcher given).
  - `toolkit.NewWebSearchTool(toolkit.WebSearchToolOptions{Searcher: web.Searcher})` → `"WebSearch"`. **Requires a `web.Searcher`** (`github.com/deepnoodle-ai/wonton/web`); wonton ships no default searcher. `config.ToolsConfig` has no searcher provider, and `Registry`'s signature is fixed `(cfg, store)`. **Decision (MVP):** WebSearch is omitted from `Registry`; `WebEnabled` enables `WebFetch` only. Documented as a follow-up (add a searcher provider to config).
- **Sub-agents** — `subagent.GeneralPurpose`, `subagent.Explore`, `subagent.Plan` are `*subagent.Definition`. `subagent.FileLoader{Directories: []string{dir}}` + `.Load(ctx) (map[string]*subagent.Definition, error)` loads `*.md` (YAML frontmatter); missing dir is skipped (it `continue`s on `os.IsNotExist`). `orchestration.NewAgentTool(orchestration.AgentToolOptions{Subagents, Model, ParentTools, Runs})` → tool `"Agent"`. `orchestration.NewTaskStopTool(orchestration.TaskStopToolOptions{Runs}) ` → tool `"TaskStop"`. `orchestration.NewRuns() *orchestration.Runs`. The Agent tool's `DefaultAgentFactory(model)` filters parent tools per definition (strips `Agent` so sub-agents can't spawn). **Only the parent turn is persisted** — sub-agent calls are inner; our PostGeneration persists the top-level response only, so this holds automatically.
- **MCP (experimental, separate module `github.com/deepnoodle-ai/dive/experimental/mcp`, declares `go 1.25`)** — `mcp.NewManager(mcp.ManagerOptions{Logger}) *mcp.Manager`; `(*mcp.Manager).InitializeServers(ctx, []*mcp.ServerConfig) error`; `(*mcp.Manager).GetAllTools() map[string]dive.Tool`; `(*mcp.Manager).Close() error`. `mcp.ServerConfig{Type, Command, Args, URL, Name, Env, Headers, ...}`. **Best-effort:** the API is experimental and on a separate module/Go version; our `internal/mcp.Connect` is thin and tolerant of partial failures (logs and returns whatever tools connected). If pinning the experimental module proves infeasible at implementation time, ship `Connect` as a no-op returning `(nil, nil)` and revisit — call this out in the task.

### Contract / dive-API mismatches discovered (adapt as noted)

1. **Go version:** shared contract pins czcli at Go `1.24`; dive declares `go 1.25.0` and `experimental/mcp` declares `go 1.25.0`. **Adaptation:** bump czcli's `go.mod` to `go 1.25` (Plan 1 sets up `go.mod`; if it landed at 1.24, this plan's Task 0 bumps it). Note for Plan 1 owner.
2. **No `dive.Session` used:** the contract's `Assistant` struct comment lists no session; we deliberately keep dive stateless per call and own history in `memory.Store`, passing `msg.SessionID` through `dive.WithValue`/`HookContext.Values`. The agent loads/saves nothing on its own.
3. **WebSearch needs a `web.Searcher` not present in config** — omitted in MVP (see above). WebFetch covers the web case.
4. **Permission gate is a hook, not the dive Dialog auto-path** — `dive.Dialog` is plumbed by us inside the `PreToolUse` hook; dive does not call our Dialog automatically.
5. **`tools.RecallTool` returns `dive.Tool` (not error)** per contract; `Registry` returns `([]dive.Tool, error)`.

---

## File Structure

```
czcli/
├── cmd/czcli/main.go              # Task 9 — load config, wire embedder+store+model+assistant, stdin/stdout loop
├── internal/
│   ├── agent/
│   │   ├── agent.go               # Tasks 6,7,8 — Build, Handle, Status, Summarizer
│   │   └── hooks.go               # Task 5 — PreGeneration / PreToolUse / PostGeneration
│   ├── tools/
│   │   ├── permission.go          # Task 2 — ConfirmDialog (dive.Dialog)
│   │   ├── recall.go              # Task 3 — RecallTool (search_memory FuncTool)
│   │   └── registry.go            # Task 4 — Registry(cfg.Tools, store)
│   └── mcp/
│       └── mcp.go                 # Task 8 — Connect(ctx, cfg.MCP) → []dive.Tool (best-effort)
└── internal/agent/testsupport_test.go   # Task 1 — fakeLLM + helpers (test-only, package agent)
```

Owned by this plan: `internal/agent/agent.go`, `internal/agent/hooks.go`, all of `internal/tools`, `internal/mcp`, and `cmd/czcli/main.go`. Depends on Plan 1 (`internal/config`, `internal/agent/model.go` `BuildModel`+`fallbackLLM`, `internal/channel`) and Plan 2 (`internal/memory`).

---

## Task 0: Verify dependencies build

**Files:** `go.mod`, `go.sum`

- [ ] Confirm Plan 1 + Plan 2 are merged: `go build ./internal/config/... ./internal/memory/... ./internal/channel/... ./internal/agent/...` (model.go) succeeds. If `internal/agent` has only `model.go`, that's fine.
- [ ] Ensure `go.mod` declares `go 1.25` (dive + experimental/mcp require it). If it says `1.24`, edit it to `go 1.25`. Run `go mod tidy`.
- [ ] Add deps: `go get github.com/deepnoodle-ai/dive@latest github.com/deepnoodle-ai/dive/experimental/mcp@latest`. Run `go build ./...` to confirm the toolchain resolves both modules. Commit: `chore(deps): add dive toolkit, subagent, orchestration, experimental mcp`.

---

## Task 1: Test scaffolding — fake StreamingLLM + helpers (package `agent`)

A deterministic `llm.StreamingLLM` the hooks/agent tests drive. Lives in `internal/agent/testsupport_test.go` (compiled only for tests, package `agent`).

**Files:** `internal/agent/testsupport_test.go`

- [ ] Write the test-support file (it has no `Test*` funcs of its own; it's consumed by later tasks). Add a trivial smoke test so `go test` exercises it.

```go
package agent

import (
	"context"
	"sync"

	"github.com/deepnoodle-ai/dive/llm"
)

// fakeStream replays a fixed slice of events as an llm.StreamIterator.
type fakeStream struct {
	events []*llm.Event
	i      int
}

func (s *fakeStream) Next() bool {
	if s.i >= len(s.events) {
		return false
	}
	s.i++
	return true
}
func (s *fakeStream) Event() *llm.Event { return s.events[s.i-1] }
func (s *fakeStream) Err() error        { return nil }
func (s *fakeStream) Close() error      { return nil }

// fakeLLM is a deterministic llm.StreamingLLM. It returns replyText as a
// single assistant message and reports usage. It records the system prompt
// and messages it last saw so tests can assert what hooks injected.
type fakeLLM struct {
	mu         sync.Mutex
	replyText  string
	usage      *llm.Usage
	lastSystem string
	lastMsgs   []*llm.Message
}

func newFakeLLM(reply string) *fakeLLM {
	return &fakeLLM{
		replyText: reply,
		usage:     &llm.Usage{InputTokens: 7, OutputTokens: 11},
	}
}

func (f *fakeLLM) Name() string { return "fake" }

func (f *fakeLLM) capture(opts []llm.Option) {
	var cfg llm.Config
	cfg.Apply(opts)
	f.mu.Lock()
	f.lastSystem = cfg.SystemPrompt
	f.lastMsgs = cfg.Messages
	f.mu.Unlock()
}

func (f *fakeLLM) response() *llm.Response {
	return &llm.Response{
		ID:      "resp_fake",
		Model:   "fake",
		Role:    llm.Assistant,
		Content: []llm.Content{&llm.TextContent{Text: f.replyText}},
		Usage:   *f.usage,
	}
}

func (f *fakeLLM) Generate(ctx context.Context, opts ...llm.Option) (*llm.Response, error) {
	f.capture(opts)
	return f.response(), nil
}

func (f *fakeLLM) Stream(ctx context.Context, opts ...llm.Option) (llm.StreamIterator, error) {
	f.capture(opts)
	idx := 0
	events := []*llm.Event{
		{Type: llm.EventTypeMessageStart, Message: &llm.Response{ID: "resp_fake", Model: "fake", Role: llm.Assistant}},
		{Type: llm.EventTypeContentBlockStart, Index: &idx, ContentBlock: &llm.EventContentBlock{Type: llm.ContentTypeText}},
		{Type: llm.EventTypeContentBlockDelta, Index: &idx, Delta: &llm.EventDelta{Type: llm.EventDeltaTypeText, Text: f.replyText}},
		{Type: llm.EventTypeContentBlockStop, Index: &idx},
		{Type: llm.EventTypeMessageDelta, Delta: &llm.EventDelta{StopReason: "end_turn"}, Usage: f.usage},
		{Type: llm.EventTypeMessageStop},
	}
	return &fakeStream{events: events}, nil
}

func (f *fakeLLM) seenSystem() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.lastSystem
}

func (f *fakeLLM) seenMessages() []*llm.Message {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.lastMsgs
}
```

- [ ] **Run + verify it compiles:** `go vet ./internal/agent/...`. Confirm `llm.Config`, `llm.Config.Apply`, `llm.Response{ID,Model,Role,Content,Usage}`, `llm.ContentTypeText` resolve. If a field/name differs in the installed dive, fix it here (this is the single place test fakes touch deep `llm` internals). FAIL expected until `agent` package has at least one non-test file — add the smoke test below which references only the fake.

```go
// in testsupport_test.go, append:
import "testing"

func TestFakeLLMStreamsReply(t *testing.T) {
	f := newFakeLLM("hello world")
	it, err := f.Stream(context.Background())
	if err != nil {
		t.Fatalf("stream: %v", err)
	}
	defer it.Close()
	acc := llm.NewResponseAccumulator()
	for it.Next() {
		if err := acc.AddEvent(it.Event()); err != nil {
			t.Fatalf("add event: %v", err)
		}
	}
	if got := acc.Response().Message().Text(); got != "hello world" {
		t.Fatalf("got %q want %q", got, "hello world")
	}
}
```

- [ ] **Run + PASS:** `go test ./internal/agent/...` (only this smoke test runs). If `acc.Response().Message()` is not a method, use `acc.Response().Content` text extraction instead — adjust per the installed `llm.Response` shape (Task 0 confirmed it builds). Commit: `test(agent): add fake StreamingLLM scaffolding`.

---

## Task 2: Permission Dialog — `tools.ConfirmDialog`

**Files:** `internal/tools/permission.go`, `internal/tools/permission_test.go`

- [ ] **Failing test:** `internal/tools/permission_test.go`

```go
package tools

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/deepnoodle-ai/dive"
)

func TestConfirmDialog_AutoApprove(t *testing.T) {
	d := ConfirmDialog(false, strings.NewReader(""), &bytes.Buffer{})
	out, err := d.Show(context.Background(), &dive.DialogInput{Confirm: true, Title: "Bash"})
	if err != nil {
		t.Fatalf("show: %v", err)
	}
	if !out.Confirmed {
		t.Fatal("expected auto-approve when requireConfirm=false")
	}
}

func TestConfirmDialog_PromptYes(t *testing.T) {
	var out bytes.Buffer
	d := ConfirmDialog(true, strings.NewReader("y\n"), &out)
	res, err := d.Show(context.Background(), &dive.DialogInput{Confirm: true, Title: "Bash", Message: "rm -rf /tmp/x"})
	if err != nil {
		t.Fatalf("show: %v", err)
	}
	if !res.Confirmed {
		t.Fatal("expected confirmed on 'y'")
	}
	if !strings.Contains(out.String(), "rm -rf /tmp/x") {
		t.Fatalf("prompt missing message: %q", out.String())
	}
}

func TestConfirmDialog_PromptNo(t *testing.T) {
	d := ConfirmDialog(true, strings.NewReader("n\n"), &bytes.Buffer{})
	res, err := d.Show(context.Background(), &dive.DialogInput{Confirm: true, Title: "Write"})
	if err != nil {
		t.Fatalf("show: %v", err)
	}
	if res.Confirmed {
		t.Fatal("expected not confirmed on 'n'")
	}
}
```

- [ ] **Run + FAIL:** `go test ./internal/tools/...` → undefined `ConfirmDialog`.
- [ ] **Minimal impl:** `internal/tools/permission.go`

```go
// Package tools assembles the dive built-in tools czcli exposes, plus the
// search_memory recall tool and the CLI permission dialog.
package tools

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/deepnoodle-ai/dive"
)

// confirmDialog implements dive.Dialog. When requireConfirm is false it
// auto-approves every confirm prompt; otherwise it prompts on out and reads
// a yes/no answer from in. Non-confirm dialogs (select/input) are not used by
// the permission gate, so they are answered with safe defaults.
type confirmDialog struct {
	requireConfirm bool
	in             io.Reader
	out            io.Writer
}

var _ dive.Dialog = (*confirmDialog)(nil)

// ConfirmDialog returns a dive.Dialog that prompts on stdin/stdout (or
// auto-approves when requireConfirm is false).
func ConfirmDialog(requireConfirm bool, in io.Reader, out io.Writer) dive.Dialog {
	return &confirmDialog{requireConfirm: requireConfirm, in: in, out: out}
}

func (d *confirmDialog) Show(ctx context.Context, in *dive.DialogInput) (*dive.DialogOutput, error) {
	if !d.requireConfirm {
		if in.Confirm {
			return &dive.DialogOutput{Confirmed: true}, nil
		}
		return &dive.DialogOutput{Text: in.Default}, nil
	}
	if !in.Confirm {
		// Permission gate only issues confirm prompts; default-allow others.
		return &dive.DialogOutput{Text: in.Default}, nil
	}

	title := in.Title
	if title == "" {
		title = "tool"
	}
	if in.Message != "" {
		fmt.Fprintf(d.out, "\n[permission] %s wants to run:\n  %s\n", title, in.Message)
	} else {
		fmt.Fprintf(d.out, "\n[permission] allow %s? ", title)
	}
	fmt.Fprint(d.out, "Allow? [y/N]: ")

	reader := bufio.NewReader(d.in)
	line, err := reader.ReadString('\n')
	if err != nil && line == "" {
		// EOF / no input → deny by default.
		return &dive.DialogOutput{Confirmed: false}, nil
	}
	answer := strings.ToLower(strings.TrimSpace(line))
	confirmed := answer == "y" || answer == "yes"
	return &dive.DialogOutput{Confirmed: confirmed}, nil
}
```

- [ ] **Run + PASS:** `go test ./internal/tools/...`. Commit: `feat(tools): add CLI permission ConfirmDialog implementing dive.Dialog`.

---

## Task 3: Recall tool — `tools.RecallTool` (`search_memory`)

**Files:** `internal/tools/recall.go`, `internal/tools/recall_test.go`

The tool's input needs a session id; we read it from the recall input field `session_id` (the agent fills it via the system prompt / or it is optional and falls back to a default). To keep recall scoped, the input carries `query`, `k`, and optional `session_id`. Tests inject a real `*memory.Store` with a fake embedder (the deterministic hash embedder Plan 2's tests define — re-declare a tiny one here to avoid cross-package test imports).

- [ ] **Failing test:** `internal/tools/recall_test.go`

```go
package tools

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/caxqueiroz/czcli/internal/config"
	"github.com/caxqueiroz/czcli/internal/memory"
	"github.com/deepnoodle-ai/dive"
)

// fakeEmbedder deterministically maps text → fixed-dim vector via SHA-256.
type fakeEmbedder struct{ dim int }

func (e *fakeEmbedder) Dim() int      { return e.dim }
func (e *fakeEmbedder) Model() string { return "fake-embed" }
func (e *fakeEmbedder) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	out := make([][]float32, len(texts))
	for i, t := range texts {
		sum := sha256.Sum256([]byte(t))
		v := make([]float32, e.dim)
		for j := range v {
			v[j] = float32(binary.BigEndian.Uint32(sum[(j*4)%len(sum):][:4]) % 1000)
		}
		out[i] = v
	}
	return out, nil
}

func openTestStore(t *testing.T) *memory.Store {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "mem.db")
	store, err := memory.Open(config.MemoryConfig{DBPath: dbPath, TokenBudget: 8000, RecallK: 5}, &fakeEmbedder{dim: 8})
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	return store
}

func callRecall(t *testing.T, tool dive.Tool, input map[string]any) string {
	t.Helper()
	raw, _ := json.Marshal(input)
	res, err := tool.Call(context.Background(), json.RawMessage(raw))
	if err != nil {
		t.Fatalf("recall call: %v", err)
	}
	if len(res.Content) == 0 {
		return ""
	}
	return res.Content[0].Text
}

func TestRecallTool_NameAndSchema(t *testing.T) {
	tool := RecallTool(openTestStore(t))
	if tool.Name() != "search_memory" {
		t.Fatalf("name = %q want search_memory", tool.Name())
	}
	if tool.Schema() == nil {
		t.Fatal("nil schema")
	}
}

func TestRecallTool_ReturnsStoredMemory(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	if err := store.AddMemory(ctx, "s1", "the cat sat on the mat", 0); err != nil {
		t.Fatalf("add memory: %v", err)
	}
	tool := RecallTool(store)
	out := callRecall(t, tool, map[string]any{"query": "cat", "k": 3, "session_id": "s1"})
	if out == "" {
		t.Fatal("expected non-empty recall output")
	}
	if !contains(out, "cat") {
		t.Fatalf("recall output missing match: %q", out)
	}
}

func TestRecallTool_EmptyQuery(t *testing.T) {
	tool := RecallTool(openTestStore(t))
	out := callRecall(t, tool, map[string]any{"query": "", "k": 3})
	if out == "" {
		t.Fatal("expected a message for empty query")
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || indexOf(s, sub) >= 0)
}
func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
```

- [ ] **Run + FAIL:** `go test ./internal/tools/...` → undefined `RecallTool`.
- [ ] **Minimal impl:** `internal/tools/recall.go`

```go
package tools

import (
	"context"
	"fmt"
	"strings"

	"github.com/caxqueiroz/czcli/internal/memory"
	"github.com/deepnoodle-ai/dive"
)

// recallInput is the search_memory tool input.
type recallInput struct {
	Query     string `json:"query" description:"What to search long-term memory for."`
	K         int    `json:"k,omitempty" description:"Max number of memories to return (default 5)."`
	SessionID string `json:"session_id,omitempty" description:"Optional session to scope the search to."`
}

const defaultRecallK = 5

// RecallTool is the search_memory FuncTool: it embeds the query, runs a KNN
// search over stored memories, and returns formatted snippets. Recall failures
// are reported as an error ToolResult (not a Go error) so the model can adapt.
func RecallTool(store *memory.Store) dive.Tool {
	return dive.FuncTool(
		"search_memory",
		"Search long-term memory for relevant past information. Use this for explicit deep recall beyond the context automatically provided each turn.",
		func(ctx context.Context, in *recallInput) (*dive.ToolResult, error) {
			query := strings.TrimSpace(in.Query)
			if query == "" {
				return dive.NewToolResultText("No query provided; nothing to search."), nil
			}
			k := in.K
			if k <= 0 {
				k = defaultRecallK
			}
			results, err := store.Recall(ctx, in.SessionID, query, k)
			if err != nil {
				return dive.NewToolResultError(fmt.Sprintf("memory recall failed: %s", err.Error())), nil
			}
			if len(results) == 0 {
				return dive.NewToolResultText("No relevant memories found."), nil
			}
			var sb strings.Builder
			sb.WriteString(fmt.Sprintf("Found %d relevant memories:\n", len(results)))
			for i, r := range results {
				sb.WriteString(fmt.Sprintf("%d. (distance %.4f) %s\n", i+1, r.Distance, strings.TrimSpace(r.Text)))
			}
			return dive.NewToolResultText(sb.String()), nil
		},
	)
}
```

- [ ] **Run + PASS:** `go test ./internal/tools/...`. If `store.Recall` with an empty `sessionID` errors in Plan 2's impl, the empty-query test still passes (it short-circuits) and the scoped test uses `"s1"`. Commit: `feat(tools): add search_memory recall FuncTool`.

---

## Task 4: Tool registry — `tools.Registry`

**Files:** `internal/tools/registry.go`, `internal/tools/registry_test.go`

- [ ] **Failing test:** `internal/tools/registry_test.go`

```go
package tools

import (
	"testing"

	"github.com/caxqueiroz/czcli/internal/config"
)

func toolNames(t *testing.T, cfg config.ToolsConfig) map[string]bool {
	t.Helper()
	store := openTestStore(t)
	tools, err := Registry(cfg, store)
	if err != nil {
		t.Fatalf("registry: %v", err)
	}
	names := map[string]bool{}
	for _, tl := range tools {
		names[tl.Name()] = true
	}
	return names
}

func TestRegistry_AlwaysIncludesRecall(t *testing.T) {
	names := toolNames(t, config.ToolsConfig{})
	if !names["search_memory"] {
		t.Fatal("search_memory must always be present")
	}
}

func TestRegistry_FilesEnabled(t *testing.T) {
	names := toolNames(t, config.ToolsConfig{FilesEnabled: true})
	for _, want := range []string{"Read", "Write", "Edit", "Glob", "Grep"} {
		if !names[want] {
			t.Fatalf("files enabled but %s missing", want)
		}
	}
}

func TestRegistry_FilesDisabled(t *testing.T) {
	names := toolNames(t, config.ToolsConfig{FilesEnabled: false})
	for _, absent := range []string{"Read", "Write", "Edit", "Glob", "Grep"} {
		if names[absent] {
			t.Fatalf("files disabled but %s present", absent)
		}
	}
}

func TestRegistry_WebEnabledAddsFetch(t *testing.T) {
	names := toolNames(t, config.ToolsConfig{WebEnabled: true})
	if !names["WebFetch"] {
		t.Fatal("web enabled but WebFetch missing")
	}
}

func TestRegistry_BashEnabled(t *testing.T) {
	on := toolNames(t, config.ToolsConfig{BashEnabled: true})
	if !on["Bash"] {
		t.Fatal("bash enabled but Bash missing")
	}
	off := toolNames(t, config.ToolsConfig{BashEnabled: false})
	if off["Bash"] {
		t.Fatal("bash disabled but Bash present")
	}
}
```

- [ ] **Run + FAIL:** `go test ./internal/tools/...` → undefined `Registry`.
- [ ] **Minimal impl:** `internal/tools/registry.go`

```go
package tools

import (
	"github.com/caxqueiroz/czcli/internal/config"
	"github.com/caxqueiroz/czcli/internal/memory"
	"github.com/deepnoodle-ai/dive"
	"github.com/deepnoodle-ai/dive/toolkit"
)

// Registry assembles the dive built-in tools czcli exposes per config, plus the
// search_memory recall tool. Bash/Write/Edit are returned as-is; gating happens
// in the agent's PreToolUse hook via a dive.Dialog (see permission.go).
//
// WebSearch is intentionally omitted: it requires a web.Searcher that
// config.ToolsConfig does not provide yet. WebFetch (self-sufficient) covers
// the web case for the MVP.
func Registry(cfg config.ToolsConfig, store *memory.Store) ([]dive.Tool, error) {
	tools := []dive.Tool{RecallTool(store)}

	if cfg.FilesEnabled {
		tools = append(tools,
			toolkit.NewReadFileTool(),
			toolkit.NewWriteFileTool(),
			toolkit.NewEditTool(),
			toolkit.NewGlobTool(),
			toolkit.NewGrepTool(),
		)
	}
	if cfg.WebEnabled {
		tools = append(tools, toolkit.NewFetchTool())
	}
	if cfg.BashEnabled {
		tools = append(tools, toolkit.NewBashTool())
	}
	return tools, nil
}
```

- [ ] **Run + PASS:** `go test ./internal/tools/...`. Commit: `feat(tools): add tool registry assembling dive built-ins + recall`.

---

## Task 5: Memory hooks — `agent.hooks.go`

The hooks read the session id from `hctx.Values["session_id"]` (set by `Handle` via `dive.WithValue`). They use a small `hookDeps` struct so `Build` can wire the store, config, dialog, summarizer, and embedder once. The summarizer is the `memory.Summarizer` returned by `Assistant.Summarizer()` (Task 7) — to break the ordering, hooks take a `func() memory.Summarizer` getter so the assistant can be fully built first.

**Files:** `internal/agent/hooks.go`, `internal/agent/hooks_test.go`

- [ ] **Failing test:** `internal/agent/hooks_test.go` (drives hooks directly against a real `memory.Store` with the fake embedder; asserts injection + persistence).

```go
package agent

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"path/filepath"
	"strings"
	"testing"

	"github.com/caxqueiroz/czcli/internal/config"
	"github.com/caxqueiroz/czcli/internal/memory"
	"github.com/deepnoodle-ai/dive"
	"github.com/deepnoodle-ai/dive/llm"
)

type fakeEmbedder struct{ dim int }

func (e *fakeEmbedder) Dim() int      { return e.dim }
func (e *fakeEmbedder) Model() string { return "fake-embed" }
func (e *fakeEmbedder) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	out := make([][]float32, len(texts))
	for i, t := range texts {
		sum := sha256.Sum256([]byte(t))
		v := make([]float32, e.dim)
		for j := range v {
			v[j] = float32(binary.BigEndian.Uint32(sum[(j*4)%len(sum):][:4]) % 1000)
		}
		out[i] = v
	}
	return out, nil
}

// noSummarizer is a memory.Summarizer that returns a fixed string.
type noSummarizer struct{}

func (noSummarizer) Summarize(ctx context.Context, msgs []memory.Message) (string, error) {
	return "SUMMARY", nil
}

func newTestStore(t *testing.T) *memory.Store {
	t.Helper()
	store, err := memory.Open(
		config.MemoryConfig{DBPath: filepath.Join(t.TempDir(), "m.db"), TokenBudget: 8000, RecallK: 5},
		&fakeEmbedder{dim: 8},
	)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	return store
}

func testDeps(t *testing.T, store *memory.Store) *hookDeps {
	return &hookDeps{
		store:        store,
		cfg:          &config.Config{Memory: config.MemoryConfig{TokenBudget: 8000, RecallK: 5}},
		dialog:       &dive.AutoApproveDialog{},
		summarizerFn: func() memory.Summarizer { return noSummarizer{} },
	}
}

func TestPreGeneration_InjectsSummaryAndRecall(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	// Seed prior history + a memory so LoadWindow returns a summary/messages and Recall finds something.
	if _, err := store.AppendMessage(ctx, memory.Message{SessionID: "s1", Role: memory.RoleUser, Content: "remember the launch code is 1234", Tokens: 8}); err != nil {
		t.Fatalf("append: %v", err)
	}
	if err := store.AddMemory(ctx, "s1", "launch code is 1234", 0); err != nil {
		t.Fatalf("memory: %v", err)
	}

	deps := testDeps(t, store)
	hctx := dive.NewHookContext()
	hctx.Values["session_id"] = "s1"
	hctx.Messages = []*llm.Message{llm.NewUserTextMessage("what is the launch code?")}

	if err := deps.preGeneration(ctx, hctx); err != nil {
		t.Fatalf("preGeneration: %v", err)
	}
	// A context message must be prepended ahead of the user input.
	if len(hctx.Messages) < 2 {
		t.Fatalf("expected injected context message, got %d messages", len(hctx.Messages))
	}
	injected := hctx.Messages[0].Text()
	if !strings.Contains(strings.ToLower(injected), "launch code") {
		t.Fatalf("recall not injected: %q", injected)
	}
}

func TestPreToolUse_DeniesWhenDialogRejects(t *testing.T) {
	store := newTestStore(t)
	deps := testDeps(t, store)
	deps.dialog = &dive.DenyAllDialog{} // reject everything

	hctx := dive.NewHookContext()
	hctx.Tool = dive.FuncTool("Bash", "run", func(ctx context.Context, _ *struct{}) (*dive.ToolResult, error) {
		return dive.NewToolResultText("ok"), nil
	})
	hctx.Call = llm.NewToolUseContent("call_1", "Bash", []byte(`{"command":"ls"}`))

	err := deps.preToolUse(context.Background(), hctx)
	if err == nil {
		t.Fatal("expected denial error for Bash with DenyAllDialog")
	}
}

func TestPreToolUse_AllowsReadOnlyTool(t *testing.T) {
	store := newTestStore(t)
	deps := testDeps(t, store)
	deps.dialog = &dive.DenyAllDialog{} // even deny-all must not be consulted for non-gated tools

	hctx := dive.NewHookContext()
	hctx.Tool = dive.FuncTool("Read", "read", func(ctx context.Context, _ *struct{}) (*dive.ToolResult, error) {
		return dive.NewToolResultText("ok"), nil
	})
	hctx.Call = llm.NewToolUseContent("call_2", "Read", []byte(`{}`))

	if err := deps.preToolUse(context.Background(), hctx); err != nil {
		t.Fatalf("read-only tool should not be gated: %v", err)
	}
}

func TestPostGeneration_PersistsTurnAndUsage(t *testing.T) {
	store := newTestStore(t)
	deps := testDeps(t, store)
	ctx := context.Background()

	hctx := dive.NewHookContext()
	hctx.Values["session_id"] = "s1"
	hctx.Values["user_input"] = "hello assistant"
	hctx.Response = &dive.Response{
		Model:   "fake",
		Usage:   &llm.Usage{InputTokens: 5, OutputTokens: 9},
		Items:   []*dive.ResponseItem{{Type: dive.ResponseItemTypeMessage, Message: llm.NewAssistantTextMessage("hi there")}},
	}
	hctx.Usage = hctx.Response.Usage

	if err := deps.postGeneration(ctx, hctx); err != nil {
		t.Fatalf("postGeneration: %v", err)
	}

	stats, err := store.Stats(ctx)
	if err != nil {
		t.Fatalf("stats: %v", err)
	}
	if stats.MessageCount < 2 {
		t.Fatalf("expected user+assistant persisted, got %d", stats.MessageCount)
	}
	roll, err := store.UsageRollups(ctx)
	if err != nil {
		t.Fatalf("usage: %v", err)
	}
	if roll.Day.OutputTokens < 9 {
		t.Fatalf("usage not recorded: %+v", roll.Day)
	}
}
```

- [ ] **Run + FAIL:** `go test ./internal/agent/...` → undefined `hookDeps`/methods.
- [ ] **Minimal impl:** `internal/agent/hooks.go`

```go
package agent

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/caxqueiroz/czcli/internal/config"
	"github.com/caxqueiroz/czcli/internal/memory"
	"github.com/deepnoodle-ai/dive"
	"github.com/deepnoodle-ai/dive/llm"
)

// gatedTools are the tools that require user confirmation before running.
var gatedTools = map[string]bool{"Bash": true, "Write": true, "Edit": true}

// hookDeps bundles everything the memory hooks need. Built once in Build and
// shared by all three hook closures.
type hookDeps struct {
	store        *memory.Store
	cfg          *config.Config
	dialog       dive.Dialog
	summarizerFn func() memory.Summarizer
}

// sessionID reads the session id placed in HookContext.Values by Handle.
func sessionID(hctx *dive.HookContext) string {
	if v, ok := hctx.Values["session_id"].(string); ok {
		return v
	}
	return "default"
}

// preGeneration loads the rolling summary + recent window and the top-k
// semantic recall for the user's query, and injects them ahead of the input.
// Memory is best-effort: any failure logs and degrades without aborting.
func (d *hookDeps) preGeneration(ctx context.Context, hctx *dive.HookContext) error {
	sid := sessionID(hctx)
	budget := d.cfg.Memory.TokenBudget
	if budget <= 0 {
		budget = 8000
	}

	var blocks []llm.Content

	summary, _, err := d.store.LoadWindow(ctx, sid, budget)
	if err != nil {
		slog.Warn("loadWindow failed", "err", err, "session_id", sid)
	} else if strings.TrimSpace(summary) != "" {
		blocks = append(blocks, llm.NewTextContent("Conversation summary so far:\n"+summary))
	}

	query := lastUserText(hctx.Messages)
	if query != "" {
		k := d.cfg.Memory.RecallK
		if k <= 0 {
			k = 5
		}
		recalled, rerr := d.store.Recall(ctx, sid, query, k)
		if rerr != nil {
			slog.Warn("recall failed", "err", rerr, "session_id", sid)
		} else if len(recalled) > 0 {
			var sb strings.Builder
			sb.WriteString("Relevant memories:\n")
			for _, r := range recalled {
				sb.WriteString("- " + strings.TrimSpace(r.Text) + "\n")
			}
			blocks = append(blocks, llm.NewTextContent(sb.String()))
		}
	}

	if len(blocks) > 0 {
		ctxMsg := llm.NewUserMessage(blocks...)
		hctx.Messages = append([]*llm.Message{ctxMsg}, hctx.Messages...)
	}
	return nil
}

// preToolUse gates Bash/Write/Edit through the permission dialog. A denial
// returns an error, which dive converts into a deny message sent to the LLM
// (so the model can adapt instead of crashing).
func (d *hookDeps) preToolUse(ctx context.Context, hctx *dive.HookContext) error {
	if hctx.Tool == nil {
		return nil
	}
	name := hctx.Tool.Name()
	if !gatedTools[name] {
		return nil
	}

	message := name
	if hctx.Call != nil && len(hctx.Call.Input) > 0 {
		message = name + " " + string(hctx.Call.Input)
	}
	out, err := d.dialog.Show(ctx, &dive.DialogInput{
		Title:   name,
		Message: message,
		Confirm: true,
		Tool:    hctx.Tool,
		Call:    hctx.Call,
	})
	if err != nil {
		return fmt.Errorf("permission dialog failed: %w", err)
	}
	if !out.Confirmed {
		return dive.NewUserFeedback(fmt.Sprintf("Permission denied by user for %s.", name))
	}
	return nil
}

// postGeneration persists the user+assistant turn, embeds the assistant turn
// into memory, records token usage, and triggers rolling summarization when the
// window exceeds the budget. All steps are best-effort.
func (d *hookDeps) postGeneration(ctx context.Context, hctx *dive.HookContext) error {
	sid := sessionID(hctx)

	userInput, _ := hctx.Values["user_input"].(string)
	if userInput != "" {
		if _, err := d.store.AppendMessage(ctx, memory.Message{
			SessionID: sid,
			Role:      memory.RoleUser,
			Content:   userInput,
			Tokens:    memory.EstimateTokens(userInput),
		}); err != nil {
			slog.Warn("append user message failed", "err", err, "session_id", sid)
		}
	}

	reply := ""
	if hctx.Response != nil {
		reply = hctx.Response.OutputText()
	}
	var assistantMsgID int64
	if reply != "" {
		id, err := d.store.AppendMessage(ctx, memory.Message{
			SessionID: sid,
			Role:      memory.RoleAssistant,
			Content:   reply,
			Tokens:    memory.EstimateTokens(reply),
		})
		if err != nil {
			slog.Warn("append assistant message failed", "err", err, "session_id", sid)
		} else {
			assistantMsgID = id
		}
		if err := d.store.AddMemory(ctx, sid, reply, assistantMsgID); err != nil {
			slog.Warn("add memory failed", "err", err, "session_id", sid)
		}
	}

	if hctx.Usage != nil {
		model := ""
		if hctx.Response != nil {
			model = hctx.Response.Model
		}
		if err := d.store.RecordUsage(ctx, "agent", model, hctx.Usage.InputTokens, hctx.Usage.OutputTokens, memory.UsageChat); err != nil {
			slog.Warn("record usage failed", "err", err, "session_id", sid)
		}
	}

	budget := d.cfg.Memory.TokenBudget
	if budget <= 0 {
		budget = 8000
	}
	if err := d.store.MaybeSummarize(ctx, sid, d.summarizerFn(), budget); err != nil {
		slog.Warn("maybe summarize failed", "err", err, "session_id", sid)
	}
	return nil
}

// lastUserText returns the text of the last user message in msgs, or "".
func lastUserText(msgs []*llm.Message) string {
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role == llm.User {
			if t := strings.TrimSpace(msgs[i].Text()); t != "" {
				return t
			}
		}
	}
	return ""
}
```

- [ ] **Run + PASS:** `go test ./internal/agent/...`. If `dive.Response{Items:...}` field names differ, adjust the test's response construction; `OutputText()` is the load-bearing API. Commit: `feat(agent): add memory injection/persistence and permission hooks`.

---

## Task 6: Assemble the agent — `agent.Build` (no sub-agents yet)

**Files:** `internal/agent/agent.go`, `internal/agent/agent_test.go`

`Build` constructs the dive.Agent with persona, model, tools, and the three hooks. It also stores everything the `Assistant` needs for `Handle`/`Status`/`Summarizer`. Sub-agents and MCP are layered in Task 8.

- [ ] **Failing test:** `internal/agent/agent_test.go`

```go
package agent

import (
	"context"
	"testing"

	"github.com/caxqueiroz/czcli/internal/config"
)

func buildTestAssistant(t *testing.T, reply string) (*Assistant, *fakeLLM) {
	t.Helper()
	store := newTestStore(t)
	model := newFakeLLM(reply)
	cfg := &config.Config{
		Persona: "You are czcli, a helpful assistant.",
		Memory:  config.MemoryConfig{TokenBudget: 8000, RecallK: 5},
		Tools:   config.ToolsConfig{FilesEnabled: true, BashEnabled: true, RequireConfirm: false},
	}
	a, err := Build(context.Background(), cfg, store, model)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	return a, model
}

func TestBuild_AssemblesAgentWithTools(t *testing.T) {
	a, _ := buildTestAssistant(t, "ok")
	if a.agent == nil {
		t.Fatal("nil dive agent")
	}
	names := map[string]bool{}
	for _, tl := range a.tools {
		names[tl.Name()] = true
	}
	if !names["search_memory"] {
		t.Fatal("recall tool missing from assembled agent")
	}
	if !names["Bash"] {
		t.Fatal("Bash missing despite BashEnabled")
	}
}
```

- [ ] **Run + FAIL:** `go test ./internal/agent/...` → undefined `Build`/`Assistant`.
- [ ] **Minimal impl:** `internal/agent/agent.go`

```go
// Package agent assembles the dive.Agent that powers czcli: a multi-provider
// model, permission-gated tools, a recall tool, opt-in sub-agents, MCP, and
// memory hooks that inject context and persist each turn.
package agent

import (
	"context"
	"fmt"
	"os"

	"github.com/caxqueiroz/czcli/internal/channel"
	"github.com/caxqueiroz/czcli/internal/config"
	"github.com/caxqueiroz/czcli/internal/memory"
	"github.com/caxqueiroz/czcli/internal/tools"
	"github.com/deepnoodle-ai/dive"
	"github.com/deepnoodle-ai/dive/llm"
	"github.com/deepnoodle-ai/dive/toolkit/orchestration"
)

// Assistant wraps a configured dive.Agent plus the dependencies needed to run
// turns, report status, and summarize memory.
type Assistant struct {
	agent *dive.Agent
	store *memory.Store
	model llm.StreamingLLM
	cfg   *config.Config
	tools []dive.Tool
	runs  *orchestration.Runs

	subagentNames []string
}

// Build assembles the dive.Agent: model (from BuildModel), tools (tools.Registry
// plus optional sub-agent tools and MCP tools), and the memory hooks
// (PreGeneration / PreToolUse / PostGeneration).
func Build(ctx context.Context, cfg *config.Config, store *memory.Store, model llm.StreamingLLM) (*Assistant, error) {
	if model == nil {
		return nil, fmt.Errorf("agent: nil model")
	}

	builtins, err := tools.Registry(cfg.Tools, store)
	if err != nil {
		return nil, fmt.Errorf("agent: build tool registry: %w", err)
	}

	a := &Assistant{
		store: store,
		model: model,
		cfg:   cfg,
		tools: builtins,
	}

	// Sub-agents + MCP are layered in by augmentTools (Task 8). It is a no-op
	// when both are disabled.
	if err := a.augmentTools(ctx, model); err != nil {
		return nil, fmt.Errorf("agent: augment tools: %w", err)
	}

	dialog := tools.ConfirmDialog(cfg.Tools.RequireConfirm, os.Stdin, os.Stdout)
	deps := &hookDeps{
		store:        store,
		cfg:          cfg,
		dialog:       dialog,
		summarizerFn: func() memory.Summarizer { return a.Summarizer() },
	}

	diveAgent, err := dive.NewAgent(dive.AgentOptions{
		Name:         "czcli",
		SystemPrompt: cfg.Persona,
		Model:        model,
		Tools:        a.tools,
		Hooks: dive.Hooks{
			PreGeneration:  []dive.PreGenerationHook{deps.preGeneration},
			PreToolUse:     []dive.PreToolUseHook{deps.preToolUse},
			PostGeneration: []dive.PostGenerationHook{deps.postGeneration},
		},
	})
	if err != nil {
		return nil, fmt.Errorf("agent: new dive agent: %w", err)
	}
	a.agent = diveAgent
	return a, nil
}

// augmentTools is defined in subagents.go (Task 8). Declared here for clarity.
var _ = (*Assistant)(nil)

// Ensure Assistant satisfies the channel.Handler/StatusFunc shapes at compile
// time once Handle/Status are added (Task 7).
var (
	_ channel.Handler    = (*Assistant)(nil).Handle
	_ channel.StatusFunc = (*Assistant)(nil).Status
)
```

> Note: the two `var _` interface-shape assertions reference `Handle`/`Status` (Task 7) and `augmentTools` (Task 8). To keep this task compiling on its own, add temporary stubs in this file and replace them in Tasks 7/8 — OR (preferred) implement Tasks 6–8 as one PR and only assert shapes at the end. The plan below assumes a single PR for `agent.go`; remove the `var _` block until Tasks 7 & 8 land, then add it.

- [ ] To keep Task 6 green standalone, temporarily add minimal stubs at the bottom of `agent.go`:

```go
func (a *Assistant) augmentTools(ctx context.Context, model llm.StreamingLLM) error { return nil }
func (a *Assistant) Summarizer() memory.Summarizer                                  { return summarizer{model: a.model} }
```

and a placeholder `summarizer` type (replaced properly in Task 7):

```go
type summarizer struct{ model llm.StreamingLLM }

func (s summarizer) Summarize(ctx context.Context, msgs []memory.Message) (string, error) {
	return "", nil
}
```

Also drop the `var (_ channel.Handler ...)` block for now (re-add in Task 7).

- [ ] **Run + PASS:** `go test ./internal/agent/...`. Commit: `feat(agent): assemble dive.Agent with tools and memory hooks`.

---

## Task 7: Turn execution, status, summarizer — `Handle`, `Status`, `Summarizer`

**Files:** `internal/agent/agent.go` (extend)

- [ ] **Failing test:** append to `internal/agent/agent_test.go`

```go
func TestHandle_EmitsTextAndReturnsReply(t *testing.T) {
	a, _ := buildTestAssistant(t, "hello from czcli")
	ctx := context.Background()

	var events []channel.StreamEvent
	var mu sync.Mutex
	emit := func(ev channel.StreamEvent) {
		mu.Lock()
		events = append(events, ev)
		mu.Unlock()
	}

	reply, err := a.Handle(ctx, channel.Message{SessionID: "s1", Text: "hi"}, emit)
	if err != nil {
		t.Fatalf("handle: %v", err)
	}
	if reply.Text != "hello from czcli" {
		t.Fatalf("reply = %q", reply.Text)
	}

	mu.Lock()
	defer mu.Unlock()
	var gotText bool
	for _, ev := range events {
		if ev.Type == "text" && strings.Contains(ev.Text, "hello") {
			gotText = true
		}
	}
	if !gotText {
		t.Fatalf("expected a text delta event, got %+v", events)
	}
}

func TestHandle_PersistsTurn(t *testing.T) {
	a, _ := buildTestAssistant(t, "persisted reply")
	ctx := context.Background()
	if _, err := a.Handle(ctx, channel.Message{SessionID: "s2", Text: "remember this"}, func(channel.StreamEvent) {}); err != nil {
		t.Fatalf("handle: %v", err)
	}
	stats, err := a.store.Stats(ctx)
	if err != nil {
		t.Fatalf("stats: %v", err)
	}
	if stats.MessageCount < 2 {
		t.Fatalf("expected user+assistant persisted, got %d", stats.MessageCount)
	}
}

func TestStatus_ReportsModelAndTools(t *testing.T) {
	a, _ := buildTestAssistant(t, "ok")
	st, err := a.Status(context.Background())
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if st.Model == "" {
		t.Fatal("status missing model")
	}
	if st.ContextBudget != 8000 {
		t.Fatalf("budget = %d", st.ContextBudget)
	}
	found := false
	for _, n := range st.ToolNames {
		if n == "search_memory" {
			found = true
		}
	}
	if !found {
		t.Fatalf("tool names missing search_memory: %v", st.ToolNames)
	}
}

func TestSummarizer_UsesModel(t *testing.T) {
	a, model := buildTestAssistant(t, "ignored")
	model.replyText = "a concise summary"
	got, err := a.Summarizer().Summarize(context.Background(), []memory.Message{
		{Role: memory.RoleUser, Content: "long conversation about cats"},
		{Role: memory.RoleAssistant, Content: "yes cats are great"},
	})
	if err != nil {
		t.Fatalf("summarize: %v", err)
	}
	if got != "a concise summary" {
		t.Fatalf("summary = %q", got)
	}
}
```

Add imports `"strings"`, `"sync"`, and `"github.com/caxqueiroz/czcli/internal/channel"`, `"github.com/caxqueiroz/czcli/internal/memory"` to the test file.

- [ ] **Run + FAIL:** `go test ./internal/agent/...` → `Handle`/`Status` undefined (stubs from Task 6 don't match).
- [ ] **Minimal impl:** replace the Task-6 stubs and add to `internal/agent/agent.go`:

```go
import (
	// add to the existing import block:
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/deepnoodle-ai/dive/llm"
)

// Handle is a channel.Handler: it runs one turn through dive with streaming and
// forwards text deltas and tool events to emit, returning the final reply.
func (a *Assistant) Handle(ctx context.Context, msg channel.Message, emit channel.EventSink) (channel.Reply, error) {
	if emit == nil {
		emit = func(channel.StreamEvent) {}
	}
	var mu sync.Mutex // EventCallback may fire from multiple goroutines.

	callback := func(_ context.Context, item *dive.ResponseItem) error {
		mu.Lock()
		defer mu.Unlock()
		switch item.Type {
		case dive.ResponseItemTypeModelEvent:
			ev := item.Event
			if ev != nil && ev.Type == llm.EventTypeContentBlockDelta && ev.Delta != nil &&
				ev.Delta.Type == llm.EventDeltaTypeText && ev.Delta.Text != "" {
				emit(channel.StreamEvent{Type: "text", Text: ev.Delta.Text})
			}
		case dive.ResponseItemTypeToolCall:
			if item.ToolCall != nil {
				emit(channel.StreamEvent{Type: "tool_start", Text: item.ToolCall.Name})
			}
		case dive.ResponseItemTypeToolCallResult:
			if item.ToolCallResult != nil {
				emit(channel.StreamEvent{Type: "tool_end", Text: item.ToolCallResult.Name})
			}
		}
		return nil
	}

	resp, err := a.agent.CreateResponse(ctx,
		dive.WithInput(msg.Text),
		dive.WithValue("session_id", msg.SessionID),
		dive.WithValue("user_input", msg.Text),
		dive.WithEventCallback(callback),
	)
	if err != nil {
		emit(channel.StreamEvent{Type: "error", Text: err.Error()})
		return channel.Reply{}, fmt.Errorf("agent: create response: %w", err)
	}
	return channel.Reply{Text: resp.OutputText()}, nil
}

// Status is a channel.StatusFunc: it composes current model/fallback state with
// memory stats and usage and the set of available tools and sub-agents.
func (a *Assistant) Status(ctx context.Context) (channel.Status, error) {
	st := channel.Status{
		Model:         a.model.Name(),
		Provider:      a.model.Name(),
		ContextBudget: a.cfg.Memory.TokenBudget,
		ToolNames:     a.toolNames(),
		SubagentNames: a.subagentNames,
	}
	if st.ContextBudget == 0 {
		st.ContextBudget = 8000
	}

	if stats, err := a.store.Stats(ctx); err == nil {
		st.MemSizeBytes = stats.DBSizeBytes
		st.MessageCount = stats.MessageCount
		st.MemoryCount = stats.MemoryCount
	}
	if roll, err := a.store.UsageRollups(ctx); err == nil {
		st.Usage = channel.UsageRollup{
			Day:   channel.UsageTotals{InputTokens: roll.Day.InputTokens, OutputTokens: roll.Day.OutputTokens},
			Week:  channel.UsageTotals{InputTokens: roll.Week.InputTokens, OutputTokens: roll.Week.OutputTokens},
			Month: channel.UsageTotals{InputTokens: roll.Month.InputTokens, OutputTokens: roll.Month.OutputTokens},
		}
	}
	if a.runs != nil {
		st.RunningSubagents = a.runs.Descriptions()
	}
	return st, nil
}

func (a *Assistant) toolNames() []string {
	names := make([]string, 0, len(a.tools))
	for _, tl := range a.tools {
		names = append(names, tl.Name())
	}
	return names
}

// Summarizer returns a memory.Summarizer backed by the agent's model. It is
// used by the PostGeneration hook to condense old turns.
func (a *Assistant) Summarizer() memory.Summarizer {
	return modelSummarizer{model: a.model}
}

// modelSummarizer condenses messages with a single non-streaming model call.
type modelSummarizer struct {
	model llm.StreamingLLM
}

const summarizePrompt = "Summarize the following conversation concisely, preserving facts, decisions, names, and any details worth remembering. Output only the summary."

func (s modelSummarizer) Summarize(ctx context.Context, msgs []memory.Message) (string, error) {
	if len(msgs) == 0 {
		return "", nil
	}
	var sb strings.Builder
	for _, m := range msgs {
		sb.WriteString(string(m.Role))
		sb.WriteString(": ")
		sb.WriteString(m.Content)
		sb.WriteString("\n")
	}
	resp, err := s.model.Generate(ctx,
		llm.WithSystemPrompt(summarizePrompt),
		llm.WithMessages(llm.NewUserTextMessage(sb.String())),
	)
	if err != nil {
		return "", fmt.Errorf("summarizer: generate: %w", err)
	}
	return strings.TrimSpace(resp.Message().Text()), nil
}
```

- [ ] Remove the Task-6 placeholder `summarizer` type and stub `Summarizer`. Re-add the interface-shape assertions to `agent.go`:

```go
var (
	_ channel.Handler    = (*Assistant)(nil).Handle
	_ channel.StatusFunc = (*Assistant)(nil).Status
)
```

- [ ] If `a.runs.Descriptions()` does not exist (Runs has no exported accessor — confirmed: it does not), drop `st.RunningSubagents` here and leave it empty (the live-subagent display is a Plan 4 concern). Use `_ = a.runs`. Verify `llm.WithSystemPrompt`, `llm.WithMessages`, and `resp.Message()` against the installed `llm` package (Task 0); if `resp.Message()` is absent, extract text from `resp.Content` directly.
- [ ] **Run + PASS:** `go test ./internal/agent/...`. Commit: `feat(agent): add streaming Handle, Status, and model-backed Summarizer`.

---

## Task 8: Sub-agents + MCP — `augmentTools`

**Files:** `internal/agent/subagents.go`, `internal/agent/subagents_test.go`, `internal/mcp/mcp.go`, `internal/mcp/mcp_test.go`

### 8a — MCP connect (best-effort, experimental)

- [ ] **Failing test:** `internal/mcp/mcp_test.go`

```go
package mcp

import (
	"context"
	"testing"

	"github.com/caxqueiroz/czcli/internal/config"
)

func TestConnect_NoServersReturnsNil(t *testing.T) {
	got, err := Connect(context.Background(), config.MCPConfig{})
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("expected no tools, got %d", len(got))
	}
}

func TestConnect_BadServerDegradesGracefully(t *testing.T) {
	// A server that cannot start must not fail Connect; it returns whatever
	// connected (here, nothing) and logs.
	got, err := Connect(context.Background(), config.MCPConfig{
		Servers: []config.MCPServerConfig{
			{Name: "broken", Command: "/nonexistent/binary-xyz"},
		},
	})
	if err != nil {
		t.Fatalf("connect should be best-effort, got err: %v", err)
	}
	_ = got // may be empty; the point is no error
}
```

- [ ] **Run + FAIL:** `go test ./internal/mcp/...` → undefined `Connect`.
- [ ] **Minimal impl:** `internal/mcp/mcp.go`

```go
// Package mcp connects configured MCP servers and exposes their tools to the
// agent. The dive MCP support is experimental (separate module); this package
// is intentionally thin and best-effort — a server that fails to start logs a
// warning and is skipped rather than failing startup.
package mcp

import (
	"context"
	"log/slog"

	"github.com/caxqueiroz/czcli/internal/config"
	"github.com/deepnoodle-ai/dive"
	divemcp "github.com/deepnoodle-ai/dive/experimental/mcp"
)

// Connect initializes the configured MCP servers and returns their tools. It is
// best-effort: errors from individual servers are logged and the remaining
// tools are returned. Returns (nil, nil) when no servers are configured.
func Connect(ctx context.Context, cfg config.MCPConfig) ([]dive.Tool, error) {
	if len(cfg.Servers) == 0 {
		return nil, nil
	}

	serverConfigs := make([]*divemcp.ServerConfig, 0, len(cfg.Servers))
	for _, s := range cfg.Servers {
		typ := "stdio"
		if s.URL != "" {
			typ = "http"
		}
		serverConfigs = append(serverConfigs, &divemcp.ServerConfig{
			Type:    typ,
			Name:    s.Name,
			Command: s.Command,
			Args:    s.Args,
			URL:     s.URL,
		})
	}

	mgr := divemcp.NewManager()
	if err := mgr.InitializeServers(ctx, serverConfigs); err != nil {
		// Best-effort: log and continue with whatever connected.
		slog.Warn("mcp: one or more servers failed to initialize", "err", err)
	}

	var out []dive.Tool
	for _, tool := range mgr.GetAllTools() {
		out = append(out, tool)
	}
	return out, nil
}
```

- [ ] **Run + verify:** `go test ./internal/mcp/...`. If the `experimental/mcp` module cannot be added (separate go.mod / version conflict), make `Connect` a no-op returning `(nil, nil)` and add a `// TODO(mcp): experimental module pinning` comment — the tests above still pass. Note this in the commit.
- [ ] Commit: `feat(mcp): best-effort connect MCP servers to dive tools`.

### 8b — Sub-agent tools + `augmentTools`

- [ ] **Failing test:** `internal/agent/subagents_test.go`

```go
package agent

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/caxqueiroz/czcli/internal/config"
)

func buildWithSubagents(t *testing.T, dir string) *Assistant {
	t.Helper()
	store := newTestStore(t)
	cfg := &config.Config{
		Persona:   "czcli",
		Memory:    config.MemoryConfig{TokenBudget: 8000, RecallK: 5},
		Tools:     config.ToolsConfig{FilesEnabled: true},
		Subagents: config.SubagentsConfig{Enabled: true, Dir: dir},
	}
	a, err := Build(context.Background(), cfg, store, newFakeLLM("ok"))
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	return a
}

func TestAugmentTools_AddsAgentAndTaskStop(t *testing.T) {
	a := buildWithSubagents(t, t.TempDir()) // empty dir → built-ins only
	names := map[string]bool{}
	for _, tl := range a.tools {
		names[tl.Name()] = true
	}
	if !names["Agent"] {
		t.Fatal("Agent tool missing when subagents enabled")
	}
	if !names["TaskStop"] {
		t.Fatal("TaskStop tool missing when subagents enabled")
	}
	// Built-in personas present.
	want := map[string]bool{"GeneralPurpose": true, "Explore": true, "Plan": true}
	got := map[string]bool{}
	for _, n := range a.subagentNames {
		got[n] = true
	}
	for n := range want {
		if !got[n] {
			t.Fatalf("subagent %s missing from catalog: %v", n, a.subagentNames)
		}
	}
}

func TestAugmentTools_LoadsFileLoaderDefinitions(t *testing.T) {
	dir := t.TempDir()
	md := "---\ndescription: A custom reviewer agent.\n---\nYou are a code reviewer.\n"
	if err := os.WriteFile(filepath.Join(dir, "Reviewer.md"), []byte(md), 0o644); err != nil {
		t.Fatalf("write agent file: %v", err)
	}
	a := buildWithSubagents(t, dir)
	found := false
	for _, n := range a.subagentNames {
		if n == "Reviewer" {
			found = true
		}
	}
	if !found {
		t.Fatalf("file-loaded subagent missing: %v", a.subagentNames)
	}
}

func TestAugmentTools_DisabledByDefault(t *testing.T) {
	a, _ := buildTestAssistant(t, "ok") // Subagents.Enabled = false
	for _, tl := range a.tools {
		if tl.Name() == "Agent" {
			t.Fatal("Agent tool present though subagents disabled")
		}
	}
}
```

- [ ] **Run + FAIL:** `go test ./internal/agent/...` → real `augmentTools` not yet implemented (only the Task-6 stub, which adds nothing).
- [ ] **Minimal impl:** `internal/agent/subagents.go` (and delete the Task-6 stub `augmentTools` from `agent.go`).

```go
package agent

import (
	"context"
	"fmt"
	"log/slog"
	"sort"

	"github.com/caxqueiroz/czcli/internal/mcp"
	"github.com/deepnoodle-ai/dive"
	"github.com/deepnoodle-ai/dive/llm"
	"github.com/deepnoodle-ai/dive/subagent"
	"github.com/deepnoodle-ai/dive/toolkit/orchestration"
)

// augmentTools layers MCP tools and (when enabled) sub-agent tools onto the
// assistant's tool set. The catalog merges the built-in personas with any
// markdown definitions found via FileLoader over cfg.Subagents.Dir.
func (a *Assistant) augmentTools(ctx context.Context, model llm.StreamingLLM) error {
	// MCP tools (best-effort).
	mcpTools, err := mcp.Connect(ctx, a.cfg.MCP)
	if err != nil {
		slog.Warn("mcp connect failed", "err", err)
	}
	a.tools = append(a.tools, mcpTools...)

	if !a.cfg.Subagents.Enabled {
		return nil
	}

	catalog := map[string]*subagent.Definition{
		"GeneralPurpose": subagent.GeneralPurpose,
		"Explore":        subagent.Explore,
		"Plan":           subagent.Plan,
	}

	if dir := a.cfg.Subagents.Dir; dir != "" {
		loader := &subagent.FileLoader{Directories: []string{dir}}
		loaded, lerr := loader.Load(ctx)
		if lerr != nil {
			slog.Warn("subagent file loader failed", "dir", dir, "err", lerr)
		} else {
			for name, def := range loaded {
				catalog[name] = def
			}
		}
	}

	a.runs = orchestration.NewRuns()

	// The Agent tool filters the parent's tools per definition. Pass the current
	// tool slice (built-ins + recall + MCP) as ParentTools; FilterTools strips
	// the Agent tool itself so sub-agents cannot spawn.
	agentTool := orchestration.NewAgentTool(orchestration.AgentToolOptions{
		Subagents:   catalog,
		Model:       model,
		ParentTools: a.tools,
		Runs:        a.runs,
	})
	taskStop := orchestration.NewTaskStopTool(orchestration.TaskStopToolOptions{Runs: a.runs})

	a.tools = append(a.tools, agentTool, taskStop)

	names := make([]string, 0, len(catalog))
	for name := range catalog {
		names = append(names, name)
	}
	sort.Strings(names)
	a.subagentNames = names
	return nil
}

var _ = fmt.Sprintf // keep fmt import if unused after edits
```

- [ ] Remove `var _ = fmt.Sprintf` once `fmt` is actually used; if not used, drop the import. (Listed only to avoid an accidental unused-import break during iteration.)
- [ ] **Run + PASS:** `go test ./internal/agent/... ./internal/mcp/...`. Commit: `feat(agent): add opt-in sub-agents and MCP tool augmentation`.

---

## Task 9: stdin/stdout entrypoint — `cmd/czcli/main.go`

A minimal read-line loop that wires config → embedder → store → model → assistant and prints replies. Plan 4 replaces this with a TUI. Keep the wiring logic in a testable `run` function; `main` is a thin shell.

**Files:** `cmd/czcli/main.go`, `cmd/czcli/main_test.go`

- [ ] **Failing test:** `cmd/czcli/main_test.go`

```go
package main

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/caxqueiroz/czcli/internal/channel"
)

// fakeHandler stands in for the assistant in the loop test.
func fakeHandler(_ context.Context, msg channel.Message, emit channel.EventSink) (channel.Reply, error) {
	emit(channel.StreamEvent{Type: "text", Text: "echo: " + msg.Text})
	return channel.Reply{Text: "echo: " + msg.Text}, nil
}

func TestReadLoop_EchoesReplies(t *testing.T) {
	in := strings.NewReader("hello\nbye\n")
	var out bytes.Buffer
	if err := readLoop(context.Background(), "sess", in, &out, fakeHandler); err != nil {
		t.Fatalf("readLoop: %v", err)
	}
	got := out.String()
	if !strings.Contains(got, "echo: hello") {
		t.Fatalf("missing first reply: %q", got)
	}
	if !strings.Contains(got, "echo: bye") {
		t.Fatalf("missing second reply: %q", got)
	}
}
```

- [ ] **Run + FAIL:** `go test ./cmd/czcli/...` → undefined `readLoop`.
- [ ] **Minimal impl:** `cmd/czcli/main.go`

```go
// Command czcli runs the assistant over a simple stdin/stdout loop. Plan 4
// replaces this entrypoint with a Bubble Tea TUI.
package main

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"

	"github.com/caxqueiroz/czcli/internal/agent"
	"github.com/caxqueiroz/czcli/internal/channel"
	"github.com/caxqueiroz/czcli/internal/config"
	"github.com/caxqueiroz/czcli/internal/memory"
)

func main() {
	if err := run(context.Background()); err != nil {
		slog.Error("czcli exited with error", "err", err)
		os.Exit(1)
	}
}

// run loads config, wires dependencies, and starts the read loop.
func run(ctx context.Context) error {
	path := os.Getenv("CZCLI_CONFIG")
	if path == "" {
		path = "config.yaml"
	}
	cfg, err := config.Load(path)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	embedder, err := memory.NewEmbedder(cfg.Embeddings)
	if err != nil {
		return fmt.Errorf("build embedder: %w", err)
	}

	store, err := memory.Open(cfg.Memory, embedder)
	if err != nil {
		return fmt.Errorf("open memory: %w", err)
	}
	defer store.Close()

	model, err := agent.BuildModel(cfg)
	if err != nil {
		return fmt.Errorf("build model: %w", err)
	}

	assistant, err := agent.Build(ctx, cfg, store, model)
	if err != nil {
		return fmt.Errorf("build assistant: %w", err)
	}

	fmt.Fprintln(os.Stdout, "czcli ready. Type a message (Ctrl-D to exit).")
	return readLoop(ctx, "cli", os.Stdin, os.Stdout, assistant.Handle)
}

// readLoop reads one line per turn, runs it through handle, streams text deltas
// inline, and prints the final reply. It returns nil on EOF.
func readLoop(ctx context.Context, sessionID string, in io.Reader, out io.Writer, handle channel.Handler) error {
	scanner := bufio.NewScanner(in)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for {
		fmt.Fprint(out, "> ")
		if !scanner.Scan() {
			break
		}
		text := strings.TrimSpace(scanner.Text())
		if text == "" {
			continue
		}

		emit := func(ev channel.StreamEvent) {
			switch ev.Type {
			case "text":
				fmt.Fprint(out, ev.Text)
			case "tool_start":
				fmt.Fprintf(out, "\n[tool: %s]\n", ev.Text)
			case "error":
				fmt.Fprintf(out, "\n[error: %s]\n", ev.Text)
			}
		}

		reply, err := handle(ctx, channel.Message{SessionID: sessionID, Text: text}, emit)
		if err != nil {
			fmt.Fprintf(out, "\nerror: %v\n", err)
			continue
		}
		// Ensure a clean line after streamed deltas; print reply if nothing streamed.
		fmt.Fprintln(out)
		_ = reply
	}
	return scanner.Err()
}
```

- [ ] **Run + PASS:** `go test ./cmd/czcli/...`. Verify `memory.NewEmbedder(cfg.Embeddings)` matches Plan 2's embedder constructor name; if Plan 2 named it differently (e.g. `memory.BuildEmbedder`), adjust the call. `config.Load`, `memory.Open`, `agent.BuildModel`, `agent.Build` are all contract-pinned.
- [ ] Commit: `feat(cmd): add stdin/stdout entrypoint wiring assistant`.

---

## Task 10: Full build + lint + end-to-end smoke

**Files:** none (verification)

- [ ] `go build ./...` — whole tree compiles.
- [ ] `go test ./...` — all unit tests pass (no live network; fakes only).
- [ ] `golangci-lint run ./internal/agent/... ./internal/tools/... ./internal/mcp/... ./cmd/czcli/...` — no lint errors. Fix any `slog` usage, acronym casing (`sessionID`), or error-wrapping nits.
- [ ] Optional manual smoke (gated, not in CI): with a real `config.yaml` and provider keys, `go run ./cmd/czcli` and exchange a message; confirm the reply prints and `~/.czcli/memory.db` gains rows. Skip if no credentials.
- [ ] Commit (if any fixes): `chore(agent): satisfy linters and final build`.

---

## Cross-task assumptions (call out if violated during implementation)

1. **Plan 1 surface:** `agent.BuildModel(cfg *config.Config) (llm.StreamingLLM, error)`, `fallbackLLM.Name()` returns a useful provider label, and `config.Load` exist as pinned. `Status.Provider`/`Model` use `model.Name()`; richer fallback-index reporting (`OnFallback`, `FallbackIndex`) requires an accessor on `fallbackLLM` that the contract does not define — leave those zero-valued for the MVP and revisit with Plan 1 if the TUI (Plan 4) needs them.
2. **Plan 2 surface:** `memory.Open`, `memory.NewEmbedder` (constructor name — verify), `Store.AppendMessage/LoadWindow/MaybeSummarize/AddMemory/Recall/RecordUsage/UsageRollups/Stats`, `memory.EstimateTokens`, and the `Summarizer`/`Embedder` interfaces, all exactly as in the contract.
3. **`llm` helpers used:** `llm.NewUserTextMessage`, `llm.NewUserMessage`, `llm.NewTextContent`, `llm.NewAssistantTextMessage`, `llm.NewToolUseContent`, `llm.WithSystemPrompt`, `llm.WithMessages`, `llm.Config`/`(*Config).Apply`, `llm.NewResponseAccumulator`, `(*Message).Text()`, `(*Response).Message()` (or `.Content`). Confirm names in Task 0; the `dive` types (`dive.NewAgent`, `dive.AgentOptions`, `dive.Hooks`, `dive.HookContext`, `dive.WithInput/WithValue/WithEventCallback`, `dive.ResponseItem*`, `dive.FuncTool`, `dive.NewToolResultText/Error`, `dive.Dialog`, `dive.AutoApproveDialog/DenyAllDialog`, `dive.NewUserFeedback`, `toolkit.New*Tool`, `subagent.*`, `orchestration.*`) were all read from source and are stable.
4. **Single-PR option for Tasks 6–8:** because `agent.go` grows across three tasks with temporary stubs, prefer landing Tasks 6, 7, and 8 in one PR (still TDD step-by-step) to avoid churn from the stub/assertion juggling. The plan keeps them as separate tasks for review granularity; collapse if your workflow prefers.
