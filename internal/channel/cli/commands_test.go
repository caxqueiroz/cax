package cli

import (
	"strings"
	"testing"
)

func TestParseCommand(t *testing.T) {
	cases := []struct {
		in   string
		name string
		args string
	}{
		{"/stats", "stats", ""},
		{"/model", "model", ""},
		{"/schedule list", "schedule", "list"},
		{"/tools   verbose", "tools", "verbose"},
	}
	for _, c := range cases {
		name, args := parseCommand(c.in)
		if name != c.name || args != c.args {
			t.Errorf("parseCommand(%q) = (%q,%q), want (%q,%q)", c.in, name, args, c.name, c.args)
		}
	}
}

func TestStatsCommand(t *testing.T) {
	m := newModel(80, 24)
	m.status = statusFixture()
	m.hasStatus = true
	out, quit := m.handleCommand("/stats")
	if quit {
		t.Fatalf("/stats should not quit")
	}
	for _, want := range []string{"claude-opus", "ctx", "1d", "mem", "messages", "vectors"} {
		if !strings.Contains(out, want) {
			t.Errorf("/stats missing %q in:\n%s", want, out)
		}
	}
}

func TestToolsCommand(t *testing.T) {
	m := newModel(80, 24)
	m.status = statusFixture()
	m.hasStatus = true
	out, _ := m.handleCommand("/tools")
	for _, want := range []string{"a", "h", "8"} {
		if !strings.Contains(out, want) {
			t.Errorf("/tools missing %q in:\n%s", want, out)
		}
	}
}

func TestAgentsCommand(t *testing.T) {
	m := newModel(80, 24)
	s := statusFixture()
	s.RunningSubagents = []string{"explore"}
	m.status = s
	m.hasStatus = true
	out, _ := m.handleCommand("/agents")
	for _, want := range []string{"explore", "plan", "general", "running"} {
		if !strings.Contains(out, want) {
			t.Errorf("/agents missing %q in:\n%s", want, out)
		}
	}
}

func TestModelCommand(t *testing.T) {
	m := newModel(80, 24)
	s := statusFixture()
	s.OnFallback = true
	s.FallbackIndex = 1
	m.status = s
	m.hasStatus = true
	out, _ := m.handleCommand("/model")
	for _, want := range []string{"anthropic", "claude-opus", "fallback", "#1"} {
		if !strings.Contains(out, want) {
			t.Errorf("/model missing %q in:\n%s", want, out)
		}
	}
}

func TestUnknownCommand(t *testing.T) {
	m := newModel(80, 24)
	out, _ := m.handleCommand("/nope")
	if !strings.Contains(out, "unknown") {
		t.Errorf("expected unknown-command hint, got %q", out)
	}
}

func TestQuitCommand(t *testing.T) {
	m := newModel(80, 24)
	_, quit := m.handleCommand("/quit")
	if !quit {
		t.Errorf("/quit should return quit=true")
	}
}
