package hooks

import "strings"

// matches reports whether an Entry fires for the given event + tool/command
// pair. An empty Matcher (zero value) matches all events of its configured
// type. Tool is exact-match, case-insensitive. Command is substring (Bash).
// When both Matcher.Tool and Matcher.Command are set, both must hold.
func matches(e Entry, ev Event, toolName, command string) bool {
	if e.Event != ev {
		return false
	}
	if e.Matcher.Tool != "" {
		if !strings.EqualFold(e.Matcher.Tool, toolName) {
			return false
		}
	}
	if e.Matcher.Command != "" {
		if !strings.Contains(command, e.Matcher.Command) {
			return false
		}
	}
	return true
}
