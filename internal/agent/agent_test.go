package agent

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/caxqueiroz/czcli/internal/channel"
	"github.com/caxqueiroz/czcli/internal/config"
	"github.com/caxqueiroz/czcli/internal/memory"
)

func buildTestAssistant(t *testing.T, reply string) (*Assistant, *scriptLLM) {
	t.Helper()
	store := newTestStore(t)
	model := newScriptLLM(reply)
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
