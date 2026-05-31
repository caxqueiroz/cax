package agent

import (
	"context"
	"fmt"
	"log/slog"
	"maps"
	"sort"

	"github.com/deepnoodle-ai/dive"
	"github.com/deepnoodle-ai/dive/llm"
	"github.com/deepnoodle-ai/dive/subagent"
	"github.com/deepnoodle-ai/dive/toolkit/orchestration"

	"github.com/caxqueiroz/cax/internal/config"
)

// augmentTools layers sub-agent tools onto the assistant's tool set. MCP
// tools are now appended in Build before augmentTools runs (Plan 6 routed
// mcp.Connect through cmd/cax/main.go), and the catalog merges the
// built-in personas (GeneralPurpose, Explore, Plan) with any markdown
// definitions found via FileLoader over cfg.Subagents.Dirs.
//
// dive v1.7.0 exposes sub-agents through the top-level subagent package
// (a plain map[string]*Definition) plus toolkit/orchestration's Agent /
// TaskStop tools (replacing the v1.5 experimental Task/TaskStop pair). The
// Agent tool takes an AgentFactory that constructs a sub-agent on demand,
// filtering the parent's tools per definition (subagent.FilterTools strips
// the Agent tool so sub-agents cannot spawn). A shared *Runs tracker links
// background spawns to TaskStop so the model can cancel them by task_id.
func (a *Assistant) augmentTools(ctx context.Context, model llm.StreamingLLM) error {
	if !a.cfg.Subagents.Enabled {
		return nil
	}

	// Start with the built-in personas (GeneralPurpose, Explore, Plan).
	defs := map[string]*subagent.Definition{
		"GeneralPurpose": subagent.GeneralPurpose,
		"Explore":        subagent.Explore,
		"Plan":           subagent.Plan,
	}

	if dirs := a.cfg.Subagents.Dirs; len(dirs) > 0 {
		loader := &subagent.FileLoader{Directories: append([]string(nil), dirs...)}
		loaded, lerr := loader.Load(ctx)
		if lerr != nil {
			slog.Warn("subagent file loader failed", "dirs", dirs, "err", lerr)
		} else {
			maps.Copy(defs, loaded)
		}
	}

	// parentTools is the tool set sub-agents may inherit from (built-ins + recall
	// + MCP). FilterTools removes the Agent tool, so capture it before appending
	// Agent / TaskStop below.
	parentTools := append([]dive.Tool(nil), a.tools...)

	// Sub-agents default to the SUBAGENT_DEFAULT role (typically the cheap
	// model). The dive.Agent type accepts llm.LLM; our router returns
	// llm.StreamingLLM which embeds llm.LLM, so the assignment is implicit.
	subagentModel := llm.LLM(model)
	if a.router != nil {
		subagentModel = a.router.For(config.ModelRoleSubagentDefault)
	}
	factory := func(_ context.Context, name string, def *subagent.Definition, pt []dive.Tool) (*dive.Agent, error) {
		filtered := subagent.FilterTools(def, pt)
		sa, ferr := dive.NewAgent(dive.AgentOptions{
			Name:         name,
			SystemPrompt: def.Prompt,
			Model:        subagentModel,
			Tools:        filtered,
		})
		if ferr != nil {
			return nil, fmt.Errorf("agent: build subagent %q: %w", name, ferr)
		}
		return sa, nil
	}

	runs := orchestration.NewRuns()
	agentTool := orchestration.NewAgentTool(orchestration.AgentToolOptions{
		Subagents:    defs,
		AgentFactory: factory,
		ParentTools:  parentTools,
		Runs:         runs,
	})
	taskStop := orchestration.NewTaskStopTool(orchestration.TaskStopToolOptions{Runs: runs})

	a.tools = append(a.tools, agentTool, taskStop)
	a.subagentNames = sortedNames(defs)
	return nil
}

// sortedNames returns the keys of defs in sorted order so the /agents view is
// deterministic.
func sortedNames(defs map[string]*subagent.Definition) []string {
	names := make([]string, 0, len(defs))
	for n := range defs {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}
