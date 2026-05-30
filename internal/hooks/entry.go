// Package hooks runs plugin-declared shell-command hooks on agent lifecycle
// events: UserPromptSubmit, PreToolUse, PostToolUse, Stop. Hooks receive a JSON
// envelope on stdin; their stdout is fed back to the model and their exit code
// is the gate (0 = no block; non-zero = block with stdout as feedback). All
// failures (spawn errors, timeouts, panics) are logged and treated as no-ops so
// a broken hook never blocks the agent — best-effort throughout.
package hooks

// Event identifies an agent lifecycle event a hook can subscribe to. The string
// values match Claude Code's wire format (hook_event_name) so plugin manifests
// authored for Claude Code run unchanged in czcli.
type Event string

const (
	EventUserPromptSubmit Event = "UserPromptSubmit"
	EventPreToolUse       Event = "PreToolUse"
	EventPostToolUse      Event = "PostToolUse"
	EventStop             Event = "Stop"
)

// Matcher narrows when a hook fires. An empty Matcher (zero value) matches all
// events of its configured type. Tool is exact-match (case-insensitive) on the
// tool name; Command is substring on the Bash command string.
type Matcher struct {
	Tool    string
	Command string
}

// Entry is one declared hook: which event, optional matcher, the argv to exec,
// a per-entry timeout (seconds; 0 means use the dispatcher default), and the
// plugin name that contributed it (for the /hooks listing).
type Entry struct {
	Event          Event
	Matcher        Matcher
	Command        []string
	TimeoutSeconds int
	Source         string
}

// Result is the outcome of one Dispatch call. Block is true when at least one
// matching hook exited non-zero; Feedback is the joined stdout of all blocking
// hooks (or empty if none blocked).
type Result struct {
	Block    bool
	Feedback string
}
