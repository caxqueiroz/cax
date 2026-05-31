package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"time"

	"github.com/caxqueiroz/cax/internal/bgproc"
	"github.com/caxqueiroz/cax/internal/config"
	"github.com/caxqueiroz/cax/internal/hooks"
	"github.com/caxqueiroz/cax/internal/memory"
	"github.com/caxqueiroz/cax/internal/projectroot"
	"github.com/deepnoodle-ai/dive"
	"github.com/deepnoodle-ai/dive/llm"
)

// gatedTools are the tools that require user confirmation before running.
var gatedTools = map[string]bool{"Bash": true, "Write": true, "Edit": true}

// hookDeps bundles everything the memory hooks need. Built once in Build and
// shared by all hook closures. hooksDisp is nil-safe: when no plugin contributes
// hooks the dispatcher itself is nil and all Dispatch calls are no-ops.
type hookDeps struct {
	store           *memory.Store
	cfg             *config.Config
	dialogFn        func() dive.Dialog // late-bound; reads the current dialog at fire time
	summarizerFn    func() memory.Summarizer
	factExtractorFn func() memory.FactExtractor // nil when memory.mode == snippets
	hooksDisp       *hooks.Dispatcher
	bgReg           *bgproc.Registry      // nil-safe; powers completion-notice injection
	projectRoot     *projectroot.Resolver // nil-safe; resolved per-turn for code_search
}

// factsRecallToFacts converts the lightweight RecallFact hits returned by
// RecallFacts into the fuller memory.Fact shape the extractor wants. Only
// ID + Text are populated — that's all the extractor reads.
func factsRecallToFacts(in []memory.RecalledFact) []memory.Fact {
	out := make([]memory.Fact, len(in))
	for i, r := range in {
		out[i] = memory.Fact{ID: r.ID, Text: r.Text}
	}
	return out
}

// factsEnabled reports whether the agent should run the mem0-style extractor
// + inject facts. memory.mode = snippets keeps the legacy raw-recall path.
func (d *hookDeps) factsEnabled() bool {
	if d.cfg == nil {
		return false
	}
	mode := d.cfg.Memory.EffectiveMode()
	return mode == config.MemoryModeFacts || mode == config.MemoryModeBoth
}

// snippetsEnabled reports whether the legacy snippet recall path should run.
// modes: snippets, both → yes. facts → no.
func (d *hookDeps) snippetsEnabled() bool {
	if d.cfg == nil {
		return true
	}
	mode := d.cfg.Memory.EffectiveMode()
	return mode == config.MemoryModeSnippets || mode == config.MemoryModeBoth
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

	summary, recent, err := d.store.LoadWindow(ctx, sid, budget)
	if err != nil {
		slog.Warn("loadWindow failed", "err", err, "session_id", sid)
	}
	if strings.TrimSpace(summary) != "" {
		blocks = append(blocks, llm.NewTextContent("Conversation summary so far:\n"+summary))
	}
	// Inject the recent uncovered turns so the agent sees the conversation
	// it just left off on after a restart — previously these were discarded
	// and the LLM had no idea where the user "left off" unless the question
	// happened to semantically match a memory vector. The current turn's
	// user message has not been persisted yet (PostGeneration writes it),
	// so this only contains prior turns.
	if len(recent) > 0 {
		var sb strings.Builder
		sb.WriteString("Recent conversation (most recent last):\n")
		for _, m := range recent {
			sb.WriteString(string(m.Role))
			sb.WriteString(": ")
			sb.WriteString(strings.TrimSpace(m.Content))
			sb.WriteByte('\n')
		}
		blocks = append(blocks, llm.NewTextContent(sb.String()))
	}

	query := lastUserText(hctx.Messages)
	if query != "" {
		k := 5
		if d.cfg != nil && d.cfg.Memory.RecallK > 0 {
			k = d.cfg.Memory.RecallK
		}
		if d.snippetsEnabled() {
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
		if d.factsEnabled() {
			facts, ferr := d.store.RecallFacts(ctx, sid, query, k)
			if ferr != nil {
				slog.Warn("fact recall failed", "err", ferr, "session_id", sid)
			} else if len(facts) > 0 {
				var sb strings.Builder
				sb.WriteString("Known facts about the user:\n")
				for _, f := range facts {
					sb.WriteString("- " + strings.TrimSpace(f.Text) + "\n")
				}
				blocks = append(blocks, llm.NewTextContent(sb.String()))
			}
		}
		// External code-search ranker (e.g. ken-mcp's `ken search`). When
		// configured, the user's query is fed to the command and ranked file
		// chunks are injected as context — the agent doesn't have to grep
		// around to find what the user is asking about. Best-effort.
		if d.cfg != nil && d.cfg.Memory.CodeSearch.Enabled() {
			var repo string
			if d.projectRoot != nil {
				repo = d.projectRoot.For(query)
			}
			ranked, rerr := runCodeSearch(ctx, d.cfg.Memory.CodeSearch, query, repo)
			if rerr != nil {
				slog.Warn("code_search failed", "err", rerr, "session_id", sid, "repo", repo)
			} else if ranked != "" {
				slog.Info("code_search hit", "session_id", sid, "repo", repo, "bytes", len(ranked))
				blocks = append(blocks, llm.NewTextContent("Relevant code from "+repo+" (ranked):\n"+ranked))
			}
		}
	}

	if d.bgReg != nil {
		notices := d.bgReg.DrainCompletionNotices()
		if len(notices) > 0 {
			var sb strings.Builder
			sb.WriteString("Background tasks finished since last turn:\n")
			for _, n := range notices {
				sb.WriteString("- " + n + "\n")
			}
			sb.WriteString("Use bash_status(task_id) to see their output.")
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

	var input string
	if hctx.Call != nil {
		input = string(hctx.Call.Input)
	}
	slog.Info("tool: call",
		"session_id", sessionID(hctx),
		"tool", name,
		"input", input,
	)
	hctx.Values["tool_call_start"] = time.Now()

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
	dialog := d.dialogFn()
	out, err := dialog.Show(ctx, &dive.DialogInput{
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
	if hctx.Tool != nil {
		var elapsedMs int64
		if start, ok := hctx.Values["tool_call_start"].(time.Time); ok {
			elapsedMs = time.Since(start).Milliseconds()
		}
		var resultPreview string
		var resultBytes int
		var isErr bool
		var goErr string
		if hctx.Result != nil {
			if hctx.Result.Error != nil {
				goErr = hctx.Result.Error.Error()
			}
			if r := hctx.Result.Result; r != nil {
				isErr = r.IsError
				for _, c := range r.Content {
					if c != nil {
						resultPreview += c.Text
					}
				}
				resultBytes = len(resultPreview)
				if len(resultPreview) > 240 {
					resultPreview = resultPreview[:240] + "…"
				}
			}
		}
		slog.Info("tool: result",
			"session_id", sessionID(hctx),
			"tool", hctx.Tool.Name(),
			"is_error", isErr,
			"go_err", goErr,
			"bytes", resultBytes,
			"elapsed_ms", elapsedMs,
			"preview", resultPreview,
		)
	}

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

	if rep, err := d.store.MaybeSummarize(ctx, sid, d.summarizerFn(), d.budgetOf()); err != nil {
		slog.Warn("maybe summarize failed", "err", err, "session_id", sid)
	} else if rep.ChunkMessages > 0 {
		// A summary was just written. Surface a sys notice via the per-turn
		// emit callback the assistant stashed in HookContext.Values so the
		// UI can render "✂ summarised N messages" after the bot reply.
		if emit, ok := hctx.Values["emit_summarized"].(func(int, int)); ok {
			emit(rep.ChunkMessages, rep.ChunkTokens)
		}
	}

	// mem0-style fact extraction. Runs only when memory.mode != snippets.
	// Best-effort: extractor failures degrade silently (memory is never
	// allowed to block a turn). Uses the cheap model role wired by the agent.
	if d.factsEnabled() && d.factExtractorFn != nil {
		userInput, _ := hctx.Values["user_input"].(string)
		if userInput != "" && reply != "" {
			if ex := d.factExtractorFn(); ex != nil {
				// Pull a few existing similar facts so the extractor can choose
				// update/delete vs add instead of blindly duplicating.
				existing, _ := d.store.RecallFacts(ctx, sid, userInput, 5)
				ops, eerr := ex.Extract(ctx, userInput, reply, factsRecallToFacts(existing))
				if eerr != nil {
					slog.Warn("fact extract failed", "err", eerr, "session_id", sid)
				} else if len(ops) > 0 {
					a, u, del, fail := memory.ApplyFactOps(ctx, d.store, ops, memory.Fact{SessionID: sid})
					slog.Info("facts: applied", "session_id", sid, "add", a, "update", u, "delete", del, "fail", fail)
				}
			}
		}
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
	for _, msg := range slices.Backward(msgs) {
		if msg.Role == llm.User {
			if t := strings.TrimSpace(msg.Text()); t != "" {
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
