// Package agent assembles the dive.Agent that powers czcli: a multi-provider
// model, permission-gated tools, a recall tool, opt-in sub-agents, MCP, and
// memory hooks that inject context and persist each turn. model.go owns the
// multi-provider model; this file owns the agent assembly and turn execution.
package agent

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"

	"github.com/caxqueiroz/czcli/internal/channel"
	"github.com/caxqueiroz/czcli/internal/config"
	"github.com/caxqueiroz/czcli/internal/mcp"
	"github.com/caxqueiroz/czcli/internal/memory"
	"github.com/caxqueiroz/czcli/internal/skills"
	"github.com/caxqueiroz/czcli/internal/tools"
	"github.com/deepnoodle-ai/dive"
	"github.com/deepnoodle-ai/dive/llm"
)

var (
	_ channel.Handler    = (*Assistant)(nil).Handle
	_ channel.StatusFunc = (*Assistant)(nil).Status
)

// Assistant wraps a configured dive.Agent plus the dependencies needed to run
// turns, report status, and summarize memory. The inner *dive.Agent is
// swappable via Rebuild under buildMu so hot-reload survives in-flight turns.
type Assistant struct {
	store *memory.Store
	model llm.StreamingLLM
	cfg   *config.Config

	buildMu        sync.RWMutex
	agent          *dive.Agent
	tools          []dive.Tool
	skillRes       *skills.LoadResult
	subagentNames  []string
	mcpServerNames []string

	mu      sync.Mutex
	running map[string]int // running sub-agent task descriptions -> ref count
}

// Build assembles the dive.Agent with skills registered as a dive.Extension
// (v1.7 wires skill.Loader through AgentOptions.Extensions), MCP tools, and
// the existing memory hooks. Plan 6 introduces the skills + mcpTools params;
// Plans 7–9 extend this further with plugin contributions, LSP tools, and
// the hook dispatcher.
//
// Callers that already have MCP ServerInfos should prefer BuildWithMCPInfos.
func Build(
	ctx context.Context,
	cfg *config.Config,
	store *memory.Store,
	model llm.StreamingLLM,
	skillRes *skills.LoadResult,
	mcpTools []dive.Tool,
) (*Assistant, error) {
	return BuildWithMCPInfos(ctx, cfg, store, model, skillRes, mcpTools, nil)
}

// BuildWithMCPInfos is Build plus the MCP ServerInfos so Status can render
// server names without re-querying the manager. cmd/czcli/main.go uses this
// from Task 9; Plans 7+ also use it for plugin-contributed MCP servers.
func BuildWithMCPInfos(
	ctx context.Context,
	cfg *config.Config,
	store *memory.Store,
	model llm.StreamingLLM,
	skillRes *skills.LoadResult,
	mcpTools []dive.Tool,
	mcpInfos []mcp.ServerInfo,
) (*Assistant, error) {
	if cfg == nil {
		return nil, fmt.Errorf("agent: nil config")
	}
	if model == nil {
		return nil, fmt.Errorf("agent: nil model")
	}

	builtins, err := tools.Registry(cfg.Tools, store)
	if err != nil {
		return nil, fmt.Errorf("agent: build tool registry: %w", err)
	}

	a := &Assistant{
		store:    store,
		model:    model,
		cfg:      cfg,
		tools:    builtins,
		skillRes: skillRes,
		running:  make(map[string]int),
	}

	// MCP tools come from cmd/czcli/main.go via mcp.Connect; mcp.Connect is
	// no longer called inside augmentTools.
	a.tools = append(a.tools, mcpTools...)

	// Sub-agents stay inside augmentTools but no longer reach into mcp.
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

	opts := dive.AgentOptions{
		Name:         "czcli",
		SystemPrompt: cfg.Persona,
		Model:        model,
		Tools:        a.tools,
		Hooks: dive.Hooks{
			PreGeneration:  []dive.PreGenerationHook{deps.preGeneration},
			PreToolUse:     []dive.PreToolUseHook{deps.preToolUse},
			PostGeneration: []dive.PostGenerationHook{deps.postGeneration},
		},
	}
	if skillRes != nil && skillRes.Loader != nil {
		opts.Extensions = append(opts.Extensions, skillRes.Loader)
	}

	diveAgent, err := dive.NewAgent(opts)
	if err != nil {
		return nil, fmt.Errorf("agent: new dive agent: %w", err)
	}
	a.agent = diveAgent

	names := make([]string, 0, len(mcpInfos))
	for _, info := range mcpInfos {
		names = append(names, info.Name)
	}
	a.mcpServerNames = names
	return a, nil
}

// Rebuild swaps the inner *dive.Agent atomically. In-flight turns finish
// under the existing agent; the next turn picks up the new one. Plans 7–9
// call this after /plugin install|enable|disable mutations.
func (a *Assistant) Rebuild(
	ctx context.Context,
	cfg *config.Config,
	skillRes *skills.LoadResult,
	mcpTools []dive.Tool,
	mcpInfos []mcp.ServerInfo,
) error {
	next, err := BuildWithMCPInfos(ctx, cfg, a.store, a.model, skillRes, mcpTools, mcpInfos)
	if err != nil {
		return fmt.Errorf("agent: rebuild: %w", err)
	}
	a.buildMu.Lock()
	a.agent = next.agent
	a.tools = next.tools
	a.skillRes = next.skillRes
	a.subagentNames = next.subagentNames
	a.mcpServerNames = next.mcpServerNames
	a.cfg = cfg
	a.buildMu.Unlock()
	return nil
}

// subagentToolName is the tool the orchestration package exposes for spawning
// sub-agents (renamed from "Task" in v1.5 to "Agent" in v1.7). Tracking its
// start/end events drives RunningSubagents.
const subagentToolName = "Agent"

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
				if item.ToolCall.Name == subagentToolName {
					a.subagentStarted(item.ToolCall.Name)
					emit(channel.StreamEvent{Type: "subagent_start", Text: item.ToolCall.Name})
				} else {
					emit(channel.StreamEvent{Type: "tool_start", Text: item.ToolCall.Name})
				}
			}
		case dive.ResponseItemTypeToolCallResult:
			if item.ToolCallResult != nil {
				if item.ToolCallResult.Name == subagentToolName {
					a.subagentEnded(item.ToolCallResult.Name)
					emit(channel.StreamEvent{Type: "subagent_end", Text: item.ToolCallResult.Name})
				} else {
					emit(channel.StreamEvent{Type: "tool_end", Text: item.ToolCallResult.Name})
				}
			}
		}
		return nil
	}

	a.buildMu.RLock()
	diveAgent := a.agent
	a.buildMu.RUnlock()

	resp, err := diveAgent.CreateResponse(ctx,
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

// subagentStarted / subagentEnded maintain the mutex-guarded set of running
// sub-agents surfaced via Status.RunningSubagents.
func (a *Assistant) subagentStarted(name string) {
	a.mu.Lock()
	a.running[name]++
	a.mu.Unlock()
}

func (a *Assistant) subagentEnded(name string) {
	a.mu.Lock()
	if a.running[name] > 0 {
		a.running[name]--
		if a.running[name] == 0 {
			delete(a.running, name)
		}
	}
	a.mu.Unlock()
}

func (a *Assistant) runningSubagents() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	names := make([]string, 0, len(a.running))
	for n := range a.running {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// Status is a channel.StatusFunc: it composes current model/fallback state with
// memory stats and usage and the set of available tools and sub-agents, plus
// Plan-6 skill/MCP fields.
func (a *Assistant) Status(ctx context.Context) (channel.Status, error) {
	a.buildMu.RLock()
	tools := a.tools
	skillRes := a.skillRes
	subagentNames := a.subagentNames
	mcpServerNames := a.mcpServerNames
	a.buildMu.RUnlock()

	st := channel.Status{
		Provider:         a.model.Name(),
		Model:            a.model.Name(),
		ContextBudget:    a.cfg.Memory.TokenBudget,
		ToolNames:        toolNamesOf(tools),
		SubagentNames:    subagentNames,
		RunningSubagents: a.runningSubagents(),
	}
	if st.ContextBudget == 0 {
		st.ContextBudget = 8000
	}

	// Populate fallback state when the model reports an active provider.
	if reporter, ok := a.model.(ActiveReporter); ok {
		idx, name := reporter.Active()
		st.Model = name
		st.Provider = name
		st.FallbackIndex = idx
		st.OnFallback = idx > 0
	}

	if skillRes != nil {
		st.SkillCount = len(skillRes.Names)
		st.SkillNames = append([]string(nil), skillRes.Names...)
	}
	if len(mcpServerNames) > 0 {
		st.MCPServerCount = len(mcpServerNames)
		st.MCPServerNames = append([]string(nil), mcpServerNames...)
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
	return st, nil
}

// toolNamesOf returns the names of the given tools in registry order.
func toolNamesOf(tools []dive.Tool) []string {
	names := make([]string, 0, len(tools))
	for _, tl := range tools {
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
