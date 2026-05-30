package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	"github.com/caxqueiroz/cax/internal/config"
	"github.com/caxqueiroz/cax/internal/hooks"
	"github.com/caxqueiroz/cax/internal/memory"
	"github.com/deepnoodle-ai/dive"
	"github.com/deepnoodle-ai/dive/llm"
)

// gatedTools are the tools that require user confirmation before running.
var gatedTools = map[string]bool{"Bash": true, "Write": true, "Edit": true}

// hookDeps bundles everything the memory hooks need. Built once in Build and
// shared by all hook closures. hooksDisp is nil-safe: when no plugin contributes
// hooks the dispatcher itself is nil and all Dispatch calls are no-ops.
type hookDeps struct {
	store        *memory.Store
	cfg          *config.Config
	dialog       dive.Dialog
	summarizerFn func() memory.Summarizer
	hooksDisp    *hooks.Dispatcher
}

// sessionID reads the session id placed in HookContext.Values by Handle.
func sessionID(hctx *dive.HookContext) string {
	if v, ok := hctx.Values["session_id"].(string); ok && v != "" {
		return v
	}
	return "default"
}

// budgetOf returns the configured token budget, defaulting to 8000.
func (d *hookDeps) budgetOf() int {
	if d.cfg != nil && d.cfg.Memory.TokenBudget > 0 {
		return d.cfg.Memory.TokenBudget
	}
	return 8000
}

// preGeneration loads the rolling summary + recent window and the top-k
// semantic recall for the user's query, and injects them ahead of the input.
// Memory is best-effort: any failure logs and degrades without aborting.
//
// Plugin hooks for EventUserPromptSubmit fire FIRST: a block aborts the turn
// (returns a *dive.HookAbortError) before memory injection runs.
func (d *hookDeps) preGeneration(ctx context.Context, hctx *dive.HookContext) error {
	if d.hooksDisp != nil {
		payload := map[string]any{
			"session_id": sessionID(hctx),
			"prompt":     lastUserText(hctx.Messages),
		}
		if r := d.hooksDisp.Dispatch(ctx, hooks.EventUserPromptSubmit, payload); r.Block {
			return dive.AbortGeneration(r.Feedback)
		}
	}

	sid := sessionID(hctx)
	budget := d.budgetOf()

	var blocks []llm.Content

	summary, _, err := d.store.LoadWindow(ctx, sid, budget)
	if err != nil {
		slog.Warn("loadWindow failed", "err", err, "session_id", sid)
	} else if strings.TrimSpace(summary) != "" {
		blocks = append(blocks, llm.NewTextContent("Conversation summary so far:\n"+summary))
	}

	query := lastUserText(hctx.Messages)
	if query != "" {
		k := 5
		if d.cfg != nil && d.cfg.Memory.RecallK > 0 {
			k = d.cfg.Memory.RecallK
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

// preToolUse runs plugin EventPreToolUse hooks first (block returns a
// *dive.UserFeedbackError so dive surfaces a Deny tool result to the model),
// then gates Bash/Write/Edit through the permission dialog. A user denial
// also returns a UserFeedbackError.
func (d *hookDeps) preToolUse(ctx context.Context, hctx *dive.HookContext) error {
	if hctx.Tool == nil {
		return nil
	}
	name := hctx.Tool.Name()

	if d.hooksDisp != nil {
		payload := map[string]any{
			"tool_name":  name,
			"tool_input": rawJSONToMap(hctx.Call),
		}
		if r := d.hooksDisp.Dispatch(ctx, hooks.EventPreToolUse, payload); r.Block {
			return dive.NewUserFeedback(r.Feedback)
		}
	}

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

// postToolUse fires after a successful tool call. Plugin hooks at this point
// cannot block (the tool has already run) but their stdout is appended as
// AdditionalContext so the model sees it alongside the tool's own output.
// Matches Claude Code's PostToolUse semantics.
func (d *hookDeps) postToolUse(ctx context.Context, hctx *dive.HookContext) error {
	if d.hooksDisp == nil || hctx.Tool == nil {
		return nil
	}
	payload := map[string]any{
		"tool_name":  hctx.Tool.Name(),
		"tool_input": rawJSONToMap(hctx.Call),
	}
	r := d.hooksDisp.Dispatch(ctx, hooks.EventPostToolUse, payload)
	if r.Feedback != "" {
		if hctx.AdditionalContext == "" {
			hctx.AdditionalContext = r.Feedback
		} else {
			hctx.AdditionalContext = hctx.AdditionalContext + "\n\n" + r.Feedback
		}
	}
	return nil
}

// postGeneration persists the user+assistant turn, embeds the assistant turn
// into memory, records token usage, and triggers rolling summarization when the
// window exceeds the budget. All steps are best-effort.
//
// Stop hooks fire last; feedback is informational only (Claude Code parity).
func (d *hookDeps) postGeneration(ctx context.Context, hctx *dive.HookContext) error {
	sid := sessionID(hctx)

	if userInput, _ := hctx.Values["user_input"].(string); userInput != "" {
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
	if reply != "" {
		id, err := d.store.AppendMessage(ctx, memory.Message{
			SessionID: sid,
			Role:      memory.RoleAssistant,
			Content:   reply,
			Tokens:    memory.EstimateTokens(reply),
		})
		if err != nil {
			slog.Warn("append assistant message failed", "err", err, "session_id", sid)
		}
		if err := d.store.AddMemory(ctx, sid, reply, id); err != nil {
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

	if err := d.store.MaybeSummarize(ctx, sid, d.summarizerFn(), d.budgetOf()); err != nil {
		slog.Warn("maybe summarize failed", "err", err, "session_id", sid)
	}

	if d.hooksDisp != nil {
		payload := map[string]any{"session_id": sid}
		if r := d.hooksDisp.Dispatch(ctx, hooks.EventStop, payload); r.Feedback != "" {
			slog.Info("hook: stop feedback", "feedback", r.Feedback)
		}
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

// rawJSONToMap parses a tool call's raw JSON input into a map for matcher +
// envelope use. Returns nil on any parse error (matcher then sees an empty
// command, which is the safe default).
func rawJSONToMap(c *llm.ToolUseContent) map[string]any {
	if c == nil || len(c.Input) == 0 {
		return nil
	}
	var m map[string]any
	if err := json.Unmarshal(c.Input, &m); err != nil {
		return nil
	}
	return m
}
