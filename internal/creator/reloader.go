package creator

import "context"

// Reloader is the single-method contract the create tools call after a
// successful write. The concrete implementation lives in cmd/cax/main.go
// (assistantReloader) and captures every dependency *agent.Assistant.Rebuild
// needs, so this package doesn't import internal/agent (cycle-safe).
//
// Implementations MUST be safe for concurrent calls — the dive agent runs
// tools in goroutines; Rebuild's atomic-swap mutex in internal/agent handles
// the actual swap. Errors are returned to the tool caller which surfaces
// them as a ToolResult error so the model can adapt.
type Reloader interface {
	Rebuild(ctx context.Context) error
}
