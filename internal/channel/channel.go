// Package channel defines the contract types that connect inbound/outbound
// message channels (CLI/TUI, Telegram, ...) to the agent core. It contains
// only type definitions so the agent (Plan 3) and channel implementations
// (Plan 4) can depend on it without import cycles.
package channel

import "context"

// Message is an inbound message from a channel to the agent.
type Message struct {
	SessionID string
	Text      string
}

// Reply is the final response returned from a turn.
type Reply struct {
	Text string
}

// StreamEvent is emitted during a turn so channels can render progress live.
type StreamEvent struct {
	// Type is one of: "text" | "tool_start" | "tool_end" |
	// "subagent_start" | "subagent_end" | "error".
	Type string
	// Text is a text delta, a tool/agent name, or an error message.
	Text string
}

// EventSink receives StreamEvents during a turn.
type EventSink func(ev StreamEvent)

// Handler processes one inbound message, streaming progress via emit and
// returning the final reply.
type Handler func(ctx context.Context, msg Message, emit EventSink) (Reply, error)

// Status is the always-on dashboard snapshot the TUI renders.
type Status struct {
	Provider         string
	Model            string
	OnFallback       bool
	FallbackIndex    int
	ContextTokens    int
	ContextBudget    int
	Usage            UsageRollup
	MemSizeBytes     int64
	MessageCount     int
	MemoryCount      int
	ToolNames        []string
	SubagentNames    []string
	RunningSubagents []string

	// Extensibility (Plans 6–9). Empty/zero until the relevant plan populates
	// them; the channel package never needs to change again after this.
	SkillCount     int
	SkillNames     []string
	MCPServerCount int
	MCPServerNames []string
	LSPServerCount int
	LSPLanguages   []string
	LSPServers     []LSPServerSummary
	PluginCount    int
	PluginNames    []string
	HookCount      int
}

// LSPServerSummary is the per-server LSP detail surfaced via /lsp; it mirrors
// internal/lsp.ServerInfo here to avoid an import cycle. Populated by Plan 8.
type LSPServerSummary struct {
	Name      string
	Languages []string
	Running   bool
	LastError string
}

// UsageTotals is duplicated here as plain data to avoid a channel→memory import
// cycle; the agent maps memory.UsageRollup → channel.UsageRollup.
type UsageTotals struct {
	InputTokens  int
	OutputTokens int
}

// UsageRollup holds token totals over 1d/1w/1m windows.
type UsageRollup struct {
	Day   UsageTotals
	Week  UsageTotals
	Month UsageTotals
}

// StatusFunc returns the current dashboard snapshot.
type StatusFunc func(ctx context.Context) (Status, error)

// Channel runs the channel loop, dispatching inbound messages to handle and
// reading the dashboard via status. Start blocks until ctx is cancelled.
type Channel interface {
	Start(ctx context.Context, handle Handler, status StatusFunc) error
}
