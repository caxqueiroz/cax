package agent

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/caxqueiroz/czcli/internal/mcp"
	"github.com/deepnoodle-ai/dive"
	"github.com/deepnoodle-ai/dive/experimental/subagent"
	"github.com/deepnoodle-ai/dive/experimental/toolkit/extended"
	"github.com/deepnoodle-ai/dive/llm"
)

// augmentTools layers MCP tools and (when enabled) sub-agent tools onto the
// assistant's tool set. The catalog merges the built-in general-purpose persona
// with any markdown definitions found via FileLoader over cfg.Subagents.Dir.
//
// dive v1.5.0 exposes sub-agents through experimental/subagent (a Registry of
// Definitions) plus experimental/toolkit/extended's Task / TaskStop tools — not
// the orchestration.NewAgentTool API the plan referenced. The Task tool takes an
// AgentFactory that constructs a sub-agent on demand, filtering the parent's
// tools per definition (FilterTools strips the Task tool so sub-agents cannot
// spawn). Only the top-level turn is persisted by the PostGeneration hook, so
// inner sub-agent calls are not separately recorded.
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

	registry := subagent.NewRegistry(true) // includes "general-purpose"

	if dir := a.cfg.Subagents.Dir; dir != "" {
		loader := &subagent.FileLoader{Directories: []string{dir}}
		loaded, lerr := loader.Load(ctx)
		if lerr != nil {
			slog.Warn("subagent file loader failed", "dir", dir, "err", lerr)
		} else {
			registry.RegisterAll(loaded)
		}
	}

	// parentTools is the tool set sub-agents may inherit from (built-ins + recall
	// + MCP). FilterTools removes the Task tool, so capture it before appending
	// Task / TaskStop below.
	parentTools := append([]dive.Tool(nil), a.tools...)

	factory := func(fctx context.Context, name string, def *subagent.Definition, pt []dive.Tool) (*dive.Agent, error) {
		filtered := subagent.FilterTools(def, pt)
		sa, ferr := dive.NewAgent(dive.AgentOptions{
			Name:         name,
			SystemPrompt: def.Prompt,
			Model:        model,
			Tools:        filtered,
		})
		if ferr != nil {
			return nil, fmt.Errorf("agent: build subagent %q: %w", name, ferr)
		}
		return sa, nil
	}

	taskRegistry := extended.NewTaskRegistry()
	taskTool := extended.NewTaskTool(extended.TaskToolOptions{
		Registry:         taskRegistry,
		AgentFactory:     factory,
		SubagentRegistry: registry,
		ParentTools:      parentTools,
	})
	taskStop := extended.NewTaskStopTool(extended.TaskStopToolOptions{Registry: taskRegistry})

	a.tools = append(a.tools, dive.ToolAdapter(taskTool), dive.ToolAdapter(taskStop))
	a.subagentNames = registry.List()
	return nil
}
