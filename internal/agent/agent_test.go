package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/caxqueiroz/cax/internal/channel"
	"github.com/caxqueiroz/cax/internal/config"
	"github.com/caxqueiroz/cax/internal/lsp"
	"github.com/caxqueiroz/cax/internal/mcp"
	"github.com/caxqueiroz/cax/internal/memory"
	"github.com/caxqueiroz/cax/internal/skills"
	"github.com/deepnoodle-ai/dive"
)

func buildTestAssistant(t *testing.T, reply string) (*Assistant, *scriptLLM) {
	t.Helper()
	store := newTestStore(t)
	model := newScriptLLM(reply)
	cfg := &config.Config{
		Persona: "You are cax, a helpful assistant.",
		Memory:  config.MemoryConfig{TokenBudget: 8000, RecallK: 5},
		Tools:   config.ToolsConfig{FilesEnabled: true, BashEnabled: true, RequireConfirm: false},
	}
	a, err := Build(context.Background(), cfg, store, model, nil, nil, nil)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	return a, model
}

// fakeMCPTool is a minimal dive.Tool used to assert MCP tools flow through Build.
type fakeMCPTool struct {
	name string
}

func (t *fakeMCPTool) Name() string         { return t.name }
func (t *fakeMCPTool) Description() string  { return "fake mcp tool" }
func (t *fakeMCPTool) Schema() *dive.Schema { return &dive.Schema{Type: "object"} }
func (t *fakeMCPTool) Annotations() *dive.ToolAnnotations {
	return nil
}
func (t *fakeMCPTool) Call(_ context.Context, _ any) (*dive.ToolResult, error) {
	return dive.NewToolResultText("ok"), nil
}

func TestBuildAcceptsSkillsAndMCPTools(t *testing.T) {
	store := newTestStore(t)
	model := newScriptLLM("dummy")
	cfg := &config.Config{
		Persona: "cax",
		Memory:  config.MemoryConfig{TokenBudget: 8000, RecallK: 5},
		Tools:   config.ToolsConfig{FilesEnabled: true},
	}

	// Build a skills LoadResult from a temp dir with one SKILL.md.
	sdir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(sdir, "ping"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(sdir, "ping", "SKILL.md"),
		[]byte("---\nname: ping\ndescription: Ping skill\n---\n# Ping\n"), 0o600); err != nil {
		t.Fatalf("write skill: %v", err)
	}
	skillRes, err := skills.Load(config.SkillsConfig{Enabled: true, Dirs: []string{sdir}}, nil)
	if err != nil {
		t.Fatalf("skills.Load: %v", err)
	}

	mcpTool := &fakeMCPTool{name: "mcp_echo"}

	a, err := Build(context.Background(), cfg, store, model, skillRes, []dive.Tool{mcpTool}, nil)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	st, err := a.Status(context.Background())
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	gotTool := false
	for _, n := range st.ToolNames {
		if n == "mcp_echo" {
			gotTool = true
		}
	}
	if !gotTool {
		t.Errorf("ToolNames = %v, want it to include mcp_echo", st.ToolNames)
	}
	if st.SkillCount != 1 || len(st.SkillNames) != 1 || st.SkillNames[0] != "ping" {
		t.Errorf("Skill fields = (%d,%v), want (1,[ping])", st.SkillCount, st.SkillNames)
	}
	if st.MCPServerCount != 0 {
		t.Errorf("MCPServerCount = %d, want 0 (no infos passed)", st.MCPServerCount)
	}
}

func TestBuildIncludesLSPToolsAndStatus(t *testing.T) {
	store := newTestStore(t)
	model := newScriptLLM("dummy")
	cfg := &config.Config{
		Persona: "cax",
		Memory:  config.MemoryConfig{TokenBudget: 8000, RecallK: 5},
		Tools:   config.ToolsConfig{FilesEnabled: true},
	}
	lspTools := []dive.Tool{&fakeMCPTool{name: "lsp_definition"}}
	lspInfos := []lsp.ServerInfo{
		{Name: "gopls", Languages: []string{"go"}, Running: true},
		{Name: "pyright", Languages: []string{"python"}, Running: false, LastError: "not found"},
	}
	a, err := BuildWithMCPInfos(context.Background(), cfg, store, model, nil, nil, nil, lspTools, lspInfos, nil, nil)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	st, err := a.Status(context.Background())
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	gotLSPTool := false
	for _, n := range st.ToolNames {
		if n == "lsp_definition" {
			gotLSPTool = true
		}
	}
	if !gotLSPTool {
		t.Errorf("ToolNames missing lsp_definition: %v", st.ToolNames)
	}
	if st.LSPServerCount != 2 {
		t.Errorf("LSPServerCount = %d, want 2", st.LSPServerCount)
	}
	if len(st.LSPLanguages) != 1 || st.LSPLanguages[0] != "go" {
		t.Errorf("LSPLanguages = %v, want [go] (running-only)", st.LSPLanguages)
	}
	if len(st.LSPServers) != 2 {
		t.Errorf("LSPServers len = %d, want 2", len(st.LSPServers))
	}
	if st.LSPServers[1].LastError != "not found" {
		t.Errorf("LSPServers[1].LastError = %q, want 'not found'", st.LSPServers[1].LastError)
	}
}

func TestStatusReportsMCPServerNames(t *testing.T) {
	store := newTestStore(t)
	model := newScriptLLM("dummy")
	cfg := &config.Config{
		Persona: "cax",
		Memory:  config.MemoryConfig{TokenBudget: 8000, RecallK: 5},
		Tools:   config.ToolsConfig{FilesEnabled: true},
	}
	infos := []mcp.ServerInfo{
		{Name: "git", Transport: "stdio", Connected: true, ToolCount: 3},
		{Name: "github", Transport: "http", Connected: false, LastError: "auth"},
	}
	a, err := BuildWithMCPInfos(context.Background(), cfg, store, model, nil, nil, infos, nil, nil, nil, nil)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	st, err := a.Status(context.Background())
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if st.MCPServerCount != 2 {
		t.Errorf("MCPServerCount = %d, want 2", st.MCPServerCount)
	}
	wantNames := map[string]bool{"git": true, "github": true}
	for _, n := range st.MCPServerNames {
		delete(wantNames, n)
	}
	if len(wantNames) != 0 {
		t.Errorf("MCPServerNames missing %v (got %v)", wantNames, st.MCPServerNames)
	}
}

func TestRebuildSwapsTools(t *testing.T) {
	store := newTestStore(t)
	model := newScriptLLM("dummy")
	cfg := &config.Config{
		Persona: "cax",
		Memory:  config.MemoryConfig{TokenBudget: 8000, RecallK: 5},
		Tools:   config.ToolsConfig{FilesEnabled: true},
	}
	a, err := Build(context.Background(), cfg, store, model, nil, []dive.Tool{&fakeMCPTool{name: "old_tool"}}, nil)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	st1, _ := a.Status(context.Background())
	hasOld := false
	for _, n := range st1.ToolNames {
		if n == "old_tool" {
			hasOld = true
		}
	}
	if !hasOld {
		t.Fatalf("expected old_tool in initial status: %v", st1.ToolNames)
	}
	if err := a.Rebuild(context.Background(), cfg, nil, []dive.Tool{&fakeMCPTool{name: "new_tool"}}, nil, nil, nil, nil, nil); err != nil {
		t.Fatalf("Rebuild: %v", err)
	}
	st2, _ := a.Status(context.Background())
	hasNew, hasOld2 := false, false
	for _, n := range st2.ToolNames {
		if n == "new_tool" {
			hasNew = true
		}
		if n == "old_tool" {
			hasOld2 = true
		}
	}
	if !hasNew {
		t.Errorf("Rebuild dropped new_tool: %v", st2.ToolNames)
	}
	if hasOld2 {
		t.Errorf("Rebuild did not drop old_tool: %v", st2.ToolNames)
	}
}

func TestBuildWithMCPInfos_AppendsCreatorToolsToRegistry(t *testing.T) {
	store := newTestStore(t)
	model := newScriptLLM("dummy")
	cfg := &config.Config{
		Persona: "cax",
		Memory:  config.MemoryConfig{TokenBudget: 8000, RecallK: 5},
		Tools:   config.ToolsConfig{FilesEnabled: true},
	}
	creatorTool := &fakeMCPTool{name: "create_skill"}
	a, err := BuildWithMCPInfos(context.Background(), cfg, store, model,
		nil, nil, nil, nil, nil, nil, []dive.Tool{creatorTool})
	if err != nil {
		t.Fatalf("BuildWithMCPInfos: %v", err)
	}
	st, err := a.Status(context.Background())
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	var found bool
	for _, n := range st.ToolNames {
		if n == "create_skill" {
			found = true
		}
	}
	if !found {
		t.Fatalf("create_skill not in tool names: %v", st.ToolNames)
	}
}

func TestRebuild_PicksUpNewCreatorTools(t *testing.T) {
	store := newTestStore(t)
	model := newScriptLLM("dummy")
	cfg := &config.Config{
		Persona: "cax",
		Memory:  config.MemoryConfig{TokenBudget: 8000, RecallK: 5},
		Tools:   config.ToolsConfig{FilesEnabled: true},
	}
	a, err := BuildWithMCPInfos(context.Background(), cfg, store, model,
		nil, nil, nil, nil, nil, nil, nil)
	if err != nil {
		t.Fatalf("BuildWithMCPInfos: %v", err)
	}
	creatorTool := &fakeMCPTool{name: "create_command"}
	if err := a.Rebuild(context.Background(), cfg, nil, nil, nil, nil, nil, nil, []dive.Tool{creatorTool}); err != nil {
		t.Fatalf("Rebuild: %v", err)
	}
	st, _ := a.Status(context.Background())
	var found bool
	for _, n := range st.ToolNames {
		if n == "create_command" {
			found = true
		}
	}
	if !found {
		t.Fatalf("create_command not present after Rebuild: %v", st.ToolNames)
	}
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
	a, _ := buildTestAssistant(t, "hello from cax")
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
	if reply.Text != "hello from cax" {
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
