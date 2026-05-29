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
func (e *fakeEmbedder) Embed(_ context.Context, texts []string) ([][]float32, error) {
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

func (noSummarizer) Summarize(_ context.Context, _ []memory.Message) (string, error) {
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
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func testDeps(_ *testing.T, store *memory.Store) *hookDeps {
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
	// Seed prior history + a memory so LoadWindow/Recall find something.
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
	hctx.Tool = dive.FuncTool("Bash", "run", func(_ context.Context, _ *struct{}) (*dive.ToolResult, error) {
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
	hctx.Tool = dive.FuncTool("Read", "read", func(_ context.Context, _ *struct{}) (*dive.ToolResult, error) {
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
		Model: "fake",
		Usage: &llm.Usage{InputTokens: 5, OutputTokens: 9},
		Items: []*dive.ResponseItem{{Type: dive.ResponseItemTypeMessage, Message: llm.NewAssistantTextMessage("hi there")}},
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
