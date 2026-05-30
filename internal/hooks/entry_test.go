package hooks

import "testing"

func TestEventConstantsMatchClaudeCodeWireFormat(t *testing.T) {
	cases := []struct {
		got, want Event
	}{
		{EventUserPromptSubmit, "UserPromptSubmit"},
		{EventPreToolUse, "PreToolUse"},
		{EventPostToolUse, "PostToolUse"},
		{EventStop, "Stop"},
	}
	for _, c := range cases {
		if string(c.got) != string(c.want) {
			t.Fatalf("event constant mismatch: got %q want %q", c.got, c.want)
		}
	}
}

func TestZeroValues(t *testing.T) {
	var m Matcher
	if m.Tool != "" || m.Command != "" {
		t.Fatalf("zero Matcher should be empty, got %+v", m)
	}
	var e Entry
	if e.Event != "" || e.TimeoutSeconds != 0 || e.Source != "" || len(e.Command) != 0 {
		t.Fatalf("zero Entry should be empty, got %+v", e)
	}
	var r Result
	if r.Block || r.Feedback != "" {
		t.Fatalf("zero Result should be non-blocking and empty, got %+v", r)
	}
}
