// Package agent assembles the dive.Agent that powers cax: a multi-provider
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

	"github.com/caxqueiroz/cax/internal/bgproc"
	"github.com/caxqueiroz/cax/internal/channel"
	"github.com/caxqueiroz/cax/internal/config"
	"github.com/caxqueiroz/cax/internal/hooks"
	"github.com/caxqueiroz/cax/internal/lsp"
	"github.com/caxqueiroz/cax/internal/mcp"
	"github.com/caxqueiroz/cax/internal/memory"
	"github.com/caxqueiroz/cax/internal/projectroot"
	"github.com/caxqueiroz/cax/internal/skills"
	"github.com/caxqueiroz/cax/internal/tasks"
	"github.com/caxqueiroz/cax/internal/tools"
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
	store  *memory.Store
	model  llm.StreamingLLM
	router *Router
	cfg    *config.Config

	buildMu        sync.RWMutex
	agent          *dive.Agent
	tools          []dive.Tool
	skillRes       *skills.LoadResult
	subagentNames  []string
	mcpServerNames []string
	lspInfos       []lsp.ServerInfo
	hooksDisp      *hooks.Dispatcher // nil-safe; populated by Build/Rebuild
	taskBoard      *tasks.Board      // nil-safe; shared with the CLI for the live task panel
	bgReg          *bgproc.Registry  // nil-safe; powers bash_bg/bash_status + the completion-notice injection

	dialogMu sync.RWMutex
	dialog   dive.Dialog // optional override; SetDialog wires the TUI's modal

	mu      sync.Mutex
	running map[string]int // running sub-agent task descriptions -> ref count

	sessionMu     sync.RWMutex
	lastSessionID string // most recent session id seen by Handle; powers the buffer gauge
}

// TaskBoard returns the shared *tasks.Board so callers (the CLI) can
// subscribe to updates from the tasks_set tool. Nil-safe.
func (a *Assistant) TaskBoard() *tasks.Board { return a.taskBoard }

// SetDialog installs a custom dive.Dialog used by the permission gate. The
// next Build/Rebuild picks it up; pass nil to revert to the legacy stdin
// prompt. Calling this before the first Build is fine — Build reads the
// field at construction time.
func (a *Assistant) SetDialog(d dive.Dialog) {
	a.dialogMu.Lock()
	a.dialog = d
	a.dialogMu.Unlock()
}

// BuildOptions bundles every optional dependency the assistant accepts.
// Required values (cfg, store, model) stay positional on BuildAgent; the
// rest live here so future additions don't churn the signature.
//
// Model is required when Router is nil. Router takes precedence when both
// are set: the bare Model is still kept for status display (ActiveReporter
// surfaces the fallback chain), but role lookups go through Router.
type BuildOptions struct {
	Model        llm.StreamingLLM
	Router       *Router
	SkillRes     *skills.LoadResult
	MCPTools     []dive.Tool
	MCPInfos     []mcp.ServerInfo
	LSPTools     []dive.Tool
	LSPInfos     []lsp.ServerInfo
	HooksDisp    *hooks.Dispatcher
	CreatorTools []dive.Tool
	TaskBoard    *tasks.Board
	BgReg        *bgproc.Registry
	ProjectRoot  *projectroot.Resolver
}

// Build is the legacy thin entrypoint kept for back-compat. New callers
// should use BuildAgent with a BuildOptions struct.
func Build(
	ctx context.Context,
	cfg *config.Config,
	store *memory.Store,
	model llm.StreamingLLM,
	skillRes *skills.LoadResult,
	mcpTools []dive.Tool,
	creatorTools []dive.Tool,
) (*Assistant, error) {
	return BuildAgent(ctx, cfg, store, BuildOptions{
		Model:        model,
		SkillRes:     skillRes,
		MCPTools:     mcpTools,
		CreatorTools: creatorTools,
	})
}

// BuildWithMCPInfos is a deprecated forwarder. Prefer BuildAgent with
// BuildOptions. Kept so existing callers (older tests, embedded users) keep
// compiling unchanged.
func BuildWithMCPInfos(
	ctx context.Context,
	cfg *config.Config,
	store *memory.Store,
	model llm.StreamingLLM,
	skillRes *skills.LoadResult,
	mcpTools []dive.Tool,
	mcpInfos []mcp.ServerInfo,
	lspTools []dive.Tool,
	lspInfos []lsp.ServerInfo,
	hooksDisp *hooks.Dispatcher,
	creatorTools []dive.Tool,
	taskBoard *tasks.Board,
	bgReg *bgproc.Registry,
	router *Router,
) (*Assistant, error) {
	return BuildAgent(ctx, cfg, store, BuildOptions{
		Model:        model,
		Router:       router,
		SkillRes:     skillRes,
		MCPTools:     mcpTools,
		MCPInfos:     mcpInfos,
		LSPTools:     lspTools,
		LSPInfos:     lspInfos,
		HooksDisp:    hooksDisp,
		CreatorTools: creatorTools,
		TaskBoard:    taskBoard,
		BgReg:        bgReg,
	})
}

// BuildAgent is the canonical assistant constructor. Required: cfg, store,
// and either opts.Model or opts.Router. Everything else is optional and
// nil-safe.
func BuildAgent(
	ctx context.Context,
	cfg *config.Config,
	store *memory.Store,
	opts BuildOptions,
) (*Assistant, error) {
	if cfg == nil {
		return nil, fmt.Errorf("agent: nil config")
	}
	model := opts.Model
	router := opts.Router
	if router != nil && model == nil {
		// Router supplies the agent chain; reuse it as the model surface.
		model = router.For(config.ModelRoleAgent)
	}
	if model == nil {
		return nil, fmt.Errorf("agent: BuildOptions.Model or BuildOptions.Router is required")
	}
	if router == nil {
		// Tests + simple callers: wrap the bare model in a stub router so every
		// role resolves to the same llm.
		router = NewRouterFromLLM(model)
	}
	skillRes := opts.SkillRes
	mcpTools := opts.MCPTools
	mcpInfos := opts.MCPInfos
	lspTools := opts.LSPTools
	lspInfos := opts.LSPInfos
	hooksDisp := opts.HooksDisp
	creatorTools := opts.CreatorTools
	taskBoard := opts.TaskBoard
	bgReg := opts.BgReg
	projectRoot := opts.ProjectRoot
	if projectRoot == nil {
		projectRoot = projectroot.New()
	}

	builtins, err := tools.Registry(cfg.Tools, store, taskBoard, bgReg)
	if err != nil {
		return nil, fmt.Errorf("agent: build tool registry: %w", err)
	}

	a := &Assistant{
		store:     store,
		model:     model,
		router:    router,
		cfg:       cfg,
		tools:     builtins,
		skillRes:  skillRes,
		running:   make(map[string]int),
		hooksDisp: hooksDisp,
		taskBoard: taskBoard,
		bgReg:     bgReg,
	}

	// MCP tools come from cmd/cax/main.go via mcp.Connect; mcp.Connect is
	// no longer called inside augmentTools.
	a.tools = append(a.tools, mcpTools...)
	// LSP tools come from cmd/cax/main.go via lsp.New + Manager.Tools().
	a.tools = append(a.tools, lspTools...)
	// Creator tools (create_skill/create_agent/create_command) come from
	// cmd/cax/main.go via creator.Tools; they wrap the Writer + Reloader
	// pair and trigger Rebuild on every successful write.
	a.tools = append(a.tools, creatorTools...)

	// Sub-agents stay inside augmentTools but no longer reach into mcp.
	if err := a.augmentTools(ctx, model); err != nil {
		return nil, fmt.Errorf("agent: augment tools: %w", err)
	}

	// Permission dialog is read at hook-fire time so SetDialog after Build
	// takes effect immediately. Legacy stdin/stdout is the default until the
	// channel sets its own (TUI modal).
	legacyDialog := tools.ConfirmDialog(cfg.Tools.RequireConfirm, os.Stdin, os.Stdout)
	deps := &hookDeps{
		store: store,
		cfg:   cfg,
		dialogFn: func() dive.Dialog {
			a.dialogMu.RLock()
			defer a.dialogMu.RUnlock()
			if a.dialog != nil {
				return a.dialog
			}
			return legacyDialog
		},
		summarizerFn: func() memory.Summarizer { return a.Summarizer() },
		factExtractorFn: func() memory.FactExtractor {
			if a.router == nil {
				return nil
			}
			role := config.ModelRoleFactExtractor
			if a.cfg != nil {
				role = a.cfg.Memory.EffectiveFactExtractorRole()
			}
			return NewFactExtractor(a.router.For(role))
		},
		hooksDisp:   hooksDisp,
		bgReg:       bgReg,
		projectRoot: projectRoot,
	}

	diveOpts := dive.AgentOptions{
		Name:         "cax",
		SystemPrompt: composeSystemPrompt(cfg.Persona),
		Model:        model,
		Tools:        a.tools,
		Hooks: dive.Hooks{
			PreGeneration:  []dive.PreGenerationHook{deps.preGeneration},
			PreToolUse:     []dive.PreToolUseHook{deps.preToolUse},
			PostToolUse:    []dive.PostToolUseHook{deps.postToolUse},
			PostGeneration: []dive.PostGenerationHook{deps.postGeneration},
		},
	}
	if skillRes != nil && skillRes.Loader != nil {
		diveOpts.Extensions = append(diveOpts.Extensions, skillRes.Loader)
	}

	diveAgent, err := dive.NewAgent(diveOpts)
	if err != nil {
		return nil, fmt.Errorf("agent: new dive agent: %w", err)
	}
	a.agent = diveAgent

	names := make([]string, 0, len(mcpInfos))
	for _, info := range mcpInfos {
		names = append(names, info.Name)
	}
	a.mcpServerNames = names
	a.lspInfos = append([]lsp.ServerInfo(nil), lspInfos...)
	return a, nil
}

// Rebuild swaps the inner *dive.Agent atomically. In-flight turns finish
// under the existing agent; the next turn picks up the new one. Plans 7-9
// call this after /plugin install|enable|disable mutations. Plan 8 extended
// the signature with lspTools+lspInfos so plugin-contributed LSP servers are
// picked up on hot-reload; Plan 9 appends hooksDisp so the new generation of
// plugin-declared hooks replaces the old set atomically; Plan 12 appends
// creatorTools so a creator-triggered Rebuild re-installs the three
// create_* FuncTools alongside the rest.
func (a *Assistant) Rebuild(
	ctx context.Context,
	cfg *config.Config,
	skillRes *skills.LoadResult,
	mcpTools []dive.Tool,
	mcpInfos []mcp.ServerInfo,
	lspTools []dive.Tool,
	lspInfos []lsp.ServerInfo,
	hooksDisp *hooks.Dispatcher,
	creatorTools []dive.Tool,
) error {
	next, err := BuildWithMCPInfos(ctx, cfg, a.store, a.model, skillRes, mcpTools, mcpInfos, lspTools, lspInfos, hooksDisp, creatorTools, a.taskBoard, a.bgReg, a.router)
	if err != nil {
		return fmt.Errorf("agent: rebuild: %w", err)
	}
	a.buildMu.Lock()
	a.agent = next.agent
	a.tools = next.tools
	a.skillRes = next.skillRes
	a.subagentNames = next.subagentNames
	a.mcpServerNames = next.mcpServerNames
	a.lspInfos = next.lspInfos
	a.hooksDisp = next.hooksDisp
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
	// Record the current session so Status() can query its working-window
	// token count to populate the buffer gauge.
	if msg.SessionID != "" {
		a.sessionMu.Lock()
		a.lastSessionID = msg.SessionID
		a.sessionMu.Unlock()
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
		// Per-turn closure the PostGeneration hook calls after summarisation.
		// The hook hands us (messages_summarised, chunk_tokens); we surface
		// a sys notice through the channel so the TUI can render it after
		// the bot reply.
		dive.WithValue("emit_summarized", func(msgs, tokens int) {
			emit(channel.StreamEvent{
				Type: "summarized",
				Text: fmt.Sprintf("compacted %d messages (~%d tokens) into the summary", msgs, tokens),
			})
		}),
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

// runningSubagents returns one entry per ACTIVE sub-agent invocation (not per
// distinct name). Three parallel Agent calls return three entries so the
// CLI's badge can show the real fan-out count.
func (a *Assistant) runningSubagents() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	var names []string
	for n, count := range a.running {
		for range count {
			names = append(names, n)
		}
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
	lspInfos := a.lspInfos
	hooksDisp := a.hooksDisp
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
	// Provider is the dive provider name (openai/bedrock/...); Model is the
	// configured model ID (e.g. gpt-5.5) so the dashboard shows what the
	// user actually picked rather than the provider tag.
	if reporter, ok := a.model.(ActiveReporter); ok {
		idx, name := reporter.Active()
		st.Provider = name
		st.Model = name
		st.FallbackIndex = idx
		st.OnFallback = idx > 0
		if mr, ok := a.model.(interface{ ActiveModel() string }); ok {
			if modelID := mr.ActiveModel(); modelID != "" {
				st.Model = modelID
			}
		}
	}

	if skillRes != nil {
		st.SkillCount = len(skillRes.Names)
		st.SkillNames = append([]string(nil), skillRes.Names...)
	}
	if len(mcpServerNames) > 0 {
		st.MCPServerCount = len(mcpServerNames)
		st.MCPServerNames = append([]string(nil), mcpServerNames...)
	}
	if len(lspInfos) > 0 {
		st.LSPServerCount = len(lspInfos)
		st.LSPLanguages = lspLanguageSet(lspInfos)
		st.LSPServers = lspServerSummaries(lspInfos)
	}
	// Entries() is nil-safe so this renders 0 when no plugin contributes hooks.
	st.HookCount = len(hooksDisp.Entries())

	if stats, err := a.store.Stats(ctx); err == nil {
		st.MemSizeBytes = stats.DBSizeBytes
		st.MessageCount = stats.MessageCount
		st.MemoryCount = stats.MemoryCount
	}
	if cwd, err := os.Getwd(); err == nil {
		st.CWD = cwd
	}
	// Populate the working-window token count for the buffer gauge by
	// loading the recent message window of the most-recent session and
	// summing its per-message token counts. Best-effort: empty session id
	// or store errors silently keep ContextTokens at 0.
	a.sessionMu.RLock()
	sid := a.lastSessionID
	a.sessionMu.RUnlock()
	if sid != "" {
		if _, msgs, err := a.store.LoadWindow(ctx, sid, st.ContextBudget); err == nil {
			total := 0
			for _, m := range msgs {
				total += m.Tokens
			}
			st.ContextTokens = total
		}
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

// lspLanguageSet returns the de-duplicated, sorted union of language IDs
// across all RUNNING LSP servers. Stopped servers don't claim a language for
// dashboard purposes.
func lspLanguageSet(infos []lsp.ServerInfo) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, i := range infos {
		if !i.Running {
			continue
		}
		for _, lang := range i.Languages {
			if !seen[lang] {
				seen[lang] = true
				out = append(out, lang)
			}
		}
	}
	sort.Strings(out)
	return out
}

// lspServerSummaries projects lsp.ServerInfo into channel.LSPServerSummary so
// the channel package never needs to import internal/lsp.
func lspServerSummaries(infos []lsp.ServerInfo) []channel.LSPServerSummary {
	out := make([]channel.LSPServerSummary, 0, len(infos))
	for _, i := range infos {
		out = append(out, channel.LSPServerSummary{
			Name:      i.Name,
			Languages: append([]string(nil), i.Languages...),
			Running:   i.Running,
			LastError: i.LastError,
		})
	}
	return out
}

// toolNamesOf returns the names of the given tools in registry order.
func toolNamesOf(tools []dive.Tool) []string {
	names := make([]string, 0, len(tools))
	for _, tl := range tools {
		names = append(names, tl.Name())
	}
	return names
}

// Summarizer returns a memory.Summarizer backed by the SUMMARIZER role
// (config.ModelRoleSummarizer). With ModelRoles unset, this falls through to
// the agent chain — same as before. With a cheap model wired to the
// summarizer role, summarization runs on the cheaper provider so the main
// turn-loop tokens aren't burned on memory plumbing.
func (a *Assistant) Summarizer() memory.Summarizer {
	m := a.model
	if a.router != nil {
		m = a.router.For(config.ModelRoleSummarizer)
	}
	return modelSummarizer{model: m}
}

// modelSummarizer condenses messages with a single non-streaming model call.
type modelSummarizer struct {
	model llm.StreamingLLM
}

const summarizePrompt = "You are folding new conversation into an existing summary. Output ONLY the new, single summary text — concise, preserving facts, decisions, names, and any details worth remembering from both the prior summary (if any) and the new messages."

// defaultInstructions is the short, action-first block appended to every
// system prompt. Kept terse on purpose — long examples make the model
// echo the same style back.
const defaultInstructions = `You are running as a local CLI process on the user's machine. The tools you have (Bash, Write, Edit, Glob, Grep, WebFetch, search_memory, Agent) execute on THIS machine — Bash runs in the user's shell with the user's permissions, Write/Edit modify real files on the user's filesystem. You are NOT a sandboxed chat assistant. Never say "I can't run commands on your machine", "I don't have remote access", or "I can only give you commands to run" — those statements are false. If something needs running, call Bash. If a file needs creating, call Write.

Rules:
1. When asked to do something, use the tools (Write, Edit, Bash, Glob, Grep, WebFetch, search_memory, Agent for sub-agents). Do not just print the code you would write. Do not offer to "provide commands you can run" — run them yourself with Bash.
2. Be brief. No preamble like "I'll do X then Y". Just do it and report what you did in one or two sentences.
3. Do not re-print tool output the user already saw via the tool. Reference paths instead.
4. Wrap pre-formatted text in fenced code blocks only when you actually need to show it.
5. If a tool fails or output shows an error (compile error, test failure, lint warning, non-zero exit code, missing file), READ the error, fix the cause yourself with Edit/Write, and retry. Do not ask the user to fix code you wrote. Only escalate when you've genuinely run out of moves: missing credentials, missing tools, ambiguous design choices, or the same failure after 3+ self-fix attempts.
6. Before reporting a task "done", verify it works. Created Go code? Run "go build ./..." (or "task build"). Created Python? Run it or its tests. Created a script? Execute it. "Done" means the artifact behaves as asked.
7. For multi-step work, publish your plan via tasks_set BEFORE starting and update it as you go. Rules for the list:
   - Use AS MANY OR AS FEW tasks as the work ACTUALLY HAS. Two real steps → 2 tasks. Eight real steps → 8 tasks. Do NOT default to round numbers (3, 5, 10) — that's a bias from training data, not a description of the work.
   - If you discover sub-steps mid-work (a build fails and now needs a fix, an investigation reveals 4 unrelated files), CALL tasks_set AGAIN with the expanded list. The panel is dynamic; growing it mid-turn is expected.
   - Exactly one task in_progress at a time.
   - BEFORE replying, mark every finished task completed. Leaving in_progress strands the panel.
   - Skip tasks_set entirely for trivial work (single tool call, one quick read). Three round trips to publish a 1-line plan is noise.
8. For long-running commands (full builds, test suites, watchers, anything > ~10s), use bash_bg instead of Bash. It returns a task_id immediately so you can continue working; poll with bash_status(task_id), or wait for the auto-injected completion notice on the next turn. Use plain Bash for short commands (< ~10s).
9. When work has INDEPENDENT parts (search 3 different subdirs, investigate 4 unrelated bugs, review N files), FAN OUT: emit multiple Agent tool calls IN THE SAME TURN with run_in_background:true. Pick the right subagent_type per task (Explore for read-only search, GeneralPurpose for action, Plan for design). Each agent runs concurrently with its own context window, results stream back automatically. Sequential single-agent calls for independent work waste wall-clock and your context budget. Examples that should fan out: "find all callers of X and Y and Z" (3 Explore agents), "review these 5 files" (5 GeneralPurpose agents), "what does package A do and how does package B differ" (2 Explore agents).`

// composeSystemPrompt prepends the user's persona to the built-in default
// instructions. If the persona is empty, falls back to a neutral default.
func composeSystemPrompt(persona string) string {
	persona = strings.TrimSpace(persona)
	if persona == "" {
		persona = "A concise, helpful personal assistant."
	}
	return persona + "\n\n" + defaultInstructions
}

func (s modelSummarizer) Summarize(ctx context.Context, priorSummary string, msgs []memory.Message) (string, error) {
	if len(msgs) == 0 {
		return priorSummary, nil
	}
	var sb strings.Builder
	if strings.TrimSpace(priorSummary) != "" {
		sb.WriteString("PRIOR SUMMARY (compresses earlier turns; preserve its key facts in the new summary):\n")
		sb.WriteString(priorSummary)
		sb.WriteString("\n\n")
	}
	sb.WriteString("NEW MESSAGES TO FOLD IN:\n")
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
