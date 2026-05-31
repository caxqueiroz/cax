package creator

import (
	"context"
	"fmt"

	"github.com/deepnoodle-ai/dive"
)

// createSkillInput is the JSON contract for the create_skill tool. The model
// passes a kebab-case name, a one-line description, and the markdown body.
type createSkillInput struct {
	Name        string `json:"name" description:"Kebab-case skill name (^[a-z0-9][a-z0-9-]{0,63}$)."`
	Description string `json:"description" description:"One-line description for the skill frontmatter."`
	Body        string `json:"body" description:"Markdown body. Will be written verbatim under the frontmatter."`
	Overwrite   bool   `json:"overwrite,omitempty" description:"Replace an existing file with the same name."`
}

// createAgentInput is the JSON contract for the create_agent tool.
type createAgentInput struct {
	Name            string   `json:"name" description:"Kebab-case agent name."`
	Description     string   `json:"description" description:"One-line description for the agent frontmatter."`
	Tools           []string `json:"tools,omitzero" description:"Optional allow-list of tool names available to the agent."`
	DisallowedTools []string `json:"disallowed_tools,omitzero" description:"Optional deny-list of tool names blocked for the agent."`
	Body            string   `json:"body" description:"Markdown system-prompt body."`
	Overwrite       bool     `json:"overwrite,omitempty" description:"Replace an existing file with the same name."`
}

// createCommandInput is the JSON contract for the create_command tool. The
// body may contain $ARGUMENTS which the CLI dispatcher expands at call time.
type createCommandInput struct {
	Name         string `json:"name" description:"Kebab-case command name (used as /<name>)."`
	Description  string `json:"description" description:"One-line description for the command frontmatter."`
	ArgumentHint string `json:"argument_hint,omitempty" description:"Optional argument hint shown in /help."`
	Body         string `json:"body" description:"Markdown prompt body. May include the literal $ARGUMENTS token."`
	Overwrite    bool   `json:"overwrite,omitempty" description:"Replace an existing file with the same name."`
}

// Tools returns the three FuncTools that materialize a skill / agent / command
// file and trigger a hot-reload. They share the same shape: validate +
// delegate to Writer + on success call Reloader.Rebuild + return a tool
// result naming the absolute path. Writer-side failures (name validation,
// already-exists conflict) are surfaced via NewToolResultError so the model
// can retry; Reloader failures are surfaced the same way but the file is
// already on disk and a subsequent /reload (or a successful next mutation)
// will pick it up.
func Tools(w Writer, r Reloader) []dive.Tool {
	skillTool := dive.FuncTool(
		"create_skill",
		"Create a new skill: writes a SKILL.md under cax's skills directory and reloads the agent so the new skill is immediately available.",
		func(ctx context.Context, in *createSkillInput) (*dive.ToolResult, error) {
			path, err := w.WriteSkill(in.Name, in.Description, in.Body, in.Overwrite)
			if err != nil {
				return dive.NewToolResultError(fmt.Sprintf("create_skill failed: %s", err.Error())), nil
			}
			if r != nil {
				if err := r.Rebuild(ctx); err != nil {
					return dive.NewToolResultError(
						fmt.Sprintf("create_skill: wrote %s but reload failed: %s", path, err.Error()),
					), nil
				}
			}
			return dive.NewToolResultText(fmt.Sprintf("wrote %s; agent reloaded", path)), nil
		},
	)

	agentTool := dive.FuncTool(
		"create_agent",
		"Create a new sub-agent persona: writes <name>.md under cax's agents directory and reloads the agent so the new persona is immediately available.",
		func(ctx context.Context, in *createAgentInput) (*dive.ToolResult, error) {
			path, err := w.WriteAgent(in.Name, in.Description, in.Tools, in.DisallowedTools, in.Body, in.Overwrite)
			if err != nil {
				return dive.NewToolResultError(fmt.Sprintf("create_agent failed: %s", err.Error())), nil
			}
			if r != nil {
				if err := r.Rebuild(ctx); err != nil {
					return dive.NewToolResultError(
						fmt.Sprintf("create_agent: wrote %s but reload failed: %s", path, err.Error()),
					), nil
				}
			}
			return dive.NewToolResultText(fmt.Sprintf("wrote %s; agent reloaded", path)), nil
		},
	)

	commandTool := dive.FuncTool(
		"create_command",
		"Create a new slash command: writes <name>.md under cax's commands directory and reloads the agent so /<name> is immediately available.",
		func(ctx context.Context, in *createCommandInput) (*dive.ToolResult, error) {
			path, err := w.WriteCommand(in.Name, in.Description, in.ArgumentHint, in.Body, in.Overwrite)
			if err != nil {
				return dive.NewToolResultError(fmt.Sprintf("create_command failed: %s", err.Error())), nil
			}
			if r != nil {
				if err := r.Rebuild(ctx); err != nil {
					return dive.NewToolResultError(
						fmt.Sprintf("create_command: wrote %s but reload failed: %s", path, err.Error()),
					), nil
				}
			}
			return dive.NewToolResultText(fmt.Sprintf("wrote %s; agent reloaded", path)), nil
		},
	)

	return []dive.Tool{skillTool, agentTool, commandTool}
}
