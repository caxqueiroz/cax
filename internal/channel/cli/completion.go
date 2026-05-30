package cli

import (
	"sort"
	"strings"
)

// builtinCommands is the canonical list of cax's own slash commands shown by
// the autocomplete dropdown. Plugin- and user-contributed commands are
// merged in at render time via model.userCommands.
var builtinCommands = []commandEntry{
	{name: "stats", desc: "model + history + memory snapshot"},
	{name: "tools", desc: "list registered tools"},
	{name: "agents", desc: "list sub-agent personas"},
	{name: "schedule", desc: "manage scheduled prompts"},
	{name: "model", desc: "show active provider/model"},
	{name: "skills", desc: "list discovered skills"},
	{name: "mcp", desc: "list connected MCP servers"},
	{name: "lsp", desc: "list LSP servers"},
	{name: "plugin", desc: "list/install/enable/disable plugins"},
	{name: "hooks", desc: "list registered hooks"},
	{name: "theme", desc: "switch theme (list|<name>)"},
	{name: "reload", desc: "reload plugins/skills/themes/MCP/LSP"},
	{name: "new", desc: "start the creator wizard (skill|agent|command)"},
	{name: "about", desc: "show the brand mark + active theme"},
	{name: "permissions", desc: "on|off|status — toggle tool confirmations"},
	{name: "quit", desc: "exit cax"},
}

// commandEntry is one row in the autocomplete dropdown.
type commandEntry struct {
	name string // without leading slash
	desc string // one-line description
}

// completionState is the model-side data for the slash-command dropdown.
// idx is the highlighted entry (0..len-1); 0 when matches changes.
type completionState struct {
	matches []commandEntry
	idx     int
}

// completionFor returns the autocomplete matches for the current input.
// Returns nil when the input doesn't start with "/" (autocomplete off) or
// when the user has already moved past the command token (space typed —
// the user is now typing arguments, not a command name).
func (m model) completionFor(input string) []commandEntry {
	if !strings.HasPrefix(input, "/") {
		return nil
	}
	rest := input[1:]
	if strings.ContainsAny(rest, " \t") {
		return nil
	}
	prefix := strings.ToLower(rest)

	// Built-ins + user/plugin commands, merged, deduped by name.
	pool := make([]commandEntry, 0, len(builtinCommands)+len(m.userCommands))
	seen := make(map[string]bool, len(pool))
	for _, c := range builtinCommands {
		pool = append(pool, c)
		seen[c.name] = true
	}
	for _, c := range m.userCommands {
		if seen[c.Name] {
			continue
		}
		pool = append(pool, commandEntry{name: c.Name, desc: c.Description})
		seen[c.Name] = true
	}

	matches := make([]commandEntry, 0, len(pool))
	for _, c := range pool {
		if strings.HasPrefix(strings.ToLower(c.name), prefix) {
			matches = append(matches, c)
		}
	}
	sort.Slice(matches, func(i, j int) bool { return matches[i].name < matches[j].name })
	return matches
}

// applyCompletion replaces the input with "/<name> " (trailing space so the
// user can immediately type args) and clears the completion state.
func (m *model) applyCompletion(name string) {
	m.input.SetValue("/" + name + " ")
	m.completion = completionState{}
	m.resizeInput()
}
