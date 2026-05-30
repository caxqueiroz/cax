package hooks

import "testing"

func TestMatches(t *testing.T) {
	cases := []struct {
		name      string
		entry     Entry
		ev        Event
		toolName  string
		command   string
		wantMatch bool
	}{
		{
			name:      "wrong event never matches",
			entry:     Entry{Event: EventPreToolUse, Matcher: Matcher{Tool: "Bash"}},
			ev:        EventPostToolUse,
			toolName:  "Bash",
			wantMatch: false,
		},
		{
			name:      "empty matcher matches any tool on the right event",
			entry:     Entry{Event: EventPreToolUse},
			ev:        EventPreToolUse,
			toolName:  "Edit",
			wantMatch: true,
		},
		{
			name:      "tool exact match is case-insensitive",
			entry:     Entry{Event: EventPreToolUse, Matcher: Matcher{Tool: "BASH"}},
			ev:        EventPreToolUse,
			toolName:  "bash",
			wantMatch: true,
		},
		{
			name:      "tool mismatch fails even when command would substring-match",
			entry:     Entry{Event: EventPreToolUse, Matcher: Matcher{Tool: "Write", Command: "rm"}},
			ev:        EventPreToolUse,
			toolName:  "Bash",
			command:   "rm -rf /",
			wantMatch: false,
		},
		{
			name:      "command substring matches anywhere in the command string",
			entry:     Entry{Event: EventPreToolUse, Matcher: Matcher{Command: "rm -rf"}},
			ev:        EventPreToolUse,
			toolName:  "Bash",
			command:   "sh -c 'rm -rf /tmp/build'",
			wantMatch: true,
		},
		{
			name:      "command substring requires occurrence",
			entry:     Entry{Event: EventPreToolUse, Matcher: Matcher{Command: "curl"}},
			ev:        EventPreToolUse,
			toolName:  "Bash",
			command:   "echo hello",
			wantMatch: false,
		},
		{
			name:      "tool and command both required when both set",
			entry:     Entry{Event: EventPreToolUse, Matcher: Matcher{Tool: "Bash", Command: "rm"}},
			ev:        EventPreToolUse,
			toolName:  "Bash",
			command:   "rm -rf /",
			wantMatch: true,
		},
		{
			name:      "userPromptSubmit ignores tool/command and matches event-only",
			entry:     Entry{Event: EventUserPromptSubmit},
			ev:        EventUserPromptSubmit,
			wantMatch: true,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := matches(c.entry, c.ev, c.toolName, c.command)
			if got != c.wantMatch {
				t.Fatalf("matches(%+v, %q, %q, %q) = %v, want %v",
					c.entry, c.ev, c.toolName, c.command, got, c.wantMatch)
			}
		})
	}
}
