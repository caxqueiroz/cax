package cli

import (
	"strings"
	"testing"

	"github.com/caxqueiroz/czcli/internal/channel"
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

func TestCmdSkillsLists(t *testing.T) {
	m := newModel(80, 24)
	m.hasStatus = true
	m.status = statusFixture()
	m.status.SkillCount = 2
	m.status.SkillNames = []string{"alpha", "beta"}
	out, quit := m.handleCommand("/skills")
	if quit {
		t.Fatalf("/skills should not quit")
	}
	for _, want := range []string{"alpha", "beta", "2"} {
		if !strings.Contains(out, want) {
			t.Errorf("/skills out = %q, want substring %q", out, want)
		}
	}
}

func TestCmdSkillsEmpty(t *testing.T) {
	m := newModel(80, 24)
	m.hasStatus = true
	m.status = statusFixture()
	out, _ := m.handleCommand("/skills")
	if !strings.Contains(out, "no skills") {
		t.Errorf("/skills empty out = %q, want 'no skills...'", out)
	}
}

func TestCmdMCPLists(t *testing.T) {
	m := newModel(80, 24)
	m.hasStatus = true
	m.status = statusFixture()
	m.status.MCPServerCount = 2
	m.status.MCPServerNames = []string{"git", "github"}
	out, _ := m.handleCommand("/mcp")
	for _, want := range []string{"git", "github"} {
		if !strings.Contains(out, want) {
			t.Errorf("/mcp out = %q, want substring %q", out, want)
		}
	}
}

func TestCmdMCPEmpty(t *testing.T) {
	m := newModel(80, 24)
	m.hasStatus = true
	m.status = statusFixture()
	out, _ := m.handleCommand("/mcp")
	if !strings.Contains(out, "no mcp") {
		t.Errorf("/mcp empty out = %q, want 'no mcp...'", out)
	}
}

func TestCmdLSPLists(t *testing.T) {
	m := newModel(80, 24)
	m.hasStatus = true
	m.status = statusFixture()
	m.status.LSPServerCount = 2
	m.status.LSPLanguages = []string{"go", "python"}
	m.status.LSPServers = []channel.LSPServerSummary{
		{Name: "gopls", Languages: []string{"go"}, Running: true},
		{Name: "pyright", Languages: []string{"python"}, Running: false, LastError: "pyright not found"},
	}
	out, quit := m.handleCommand("/lsp")
	if quit {
		t.Fatal("quit unexpected")
	}
	for _, want := range []string{"gopls", "go", "pyright", "pyright not found"} {
		if !strings.Contains(out, want) {
			t.Errorf("/lsp out = %q, want substring %q", out, want)
		}
	}
}

func TestCmdLSPEmpty(t *testing.T) {
	m := newModel(80, 24)
	m.hasStatus = true
	m.status = statusFixture()
	out, _ := m.handleCommand("/lsp")
	if !strings.Contains(out, "no LSP") && !strings.Contains(out, "no lsp") {
		t.Errorf("/lsp empty out = %q, want 'no LSP...'", out)
	}
}
