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
	"github.com/caxqueiroz/czcli/internal/memory"
	"github.com/caxqueiroz/czcli/internal/tools"
	"github.com/deepnoodle-ai/dive"
	"github.com/deepnoodle-ai/dive/llm"
)

var (
	_ channel.Handler    = (*Assistant)(nil).Handle
	_ channel.StatusFunc = (*Assistant)(nil).Status
)

// Assistant wraps a configured dive.Agent plus the dependencies needed to run
// turns, report status, and summarize memory.
type Assistant struct {
	agent *dive.Agent
	store *memory.Store
	model llm.StreamingLLM
	cfg   *config.Config
	tools []dive.Tool

	subagentNames []string

	mu      sync.Mutex
	running map[string]int // running sub-agent task descriptions -> ref count
}

// Build assembles the dive.Agent: model (from BuildModel), tools (tools.Registry
// plus optional sub-agent tools and MCP tools), and the memory hooks
// (PreGeneration / PreToolUse / PostGeneration). We do NOT use dive.Session; the
// agent is stateless per call and czcli owns history in memory.Store, carrying
// session_id / user_input through HookContext.Values.
func Build(ctx context.Context, cfg *config.Config, store *memory.Store, model llm.StreamingLLM) (*Assistant, error) {
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
		store:   store,
		model:   model,
		cfg:     cfg,
		tools:   builtins,
		running: make(map[string]int),
	}

	// Sub-agents + MCP are layered in by augmentTools (subagents.go). It is a
	// no-op when both are disabled.
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
// memory stats and usage and the set of available tools and sub-agents.
func (a *Assistant) Status(ctx context.Context) (channel.Status, error) {
	st := channel.Status{
		Provider:         a.model.Name(),
		Model:            a.model.Name(),
		ContextBudget:    a.cfg.Memory.TokenBudget,
		ToolNames:        a.toolNames(),
		SubagentNames:    a.subagentNames,
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
