package cli

import (
	"context"
	"strings"
	"testing"

	"github.com/caxqueiroz/cax/internal/channel"
	"github.com/caxqueiroz/cax/internal/creator"
	"github.com/caxqueiroz/cax/internal/hooks"
	"github.com/caxqueiroz/cax/internal/theme"
)

func TestCmdThemeListAndSet(t *testing.T) {
	theme.LoadBuiltins()
	dark, _ := theme.Get("default-dark")
	theme.Set(dark)

	m := newModel(80, 24)
	out, quit, _ := m.handleCommand("/theme list")
	if quit {
		t.Fatalf("list should not quit")
	}
	if !strings.Contains(out, "default-dark") || !strings.Contains(out, "dracula") {
		t.Fatalf("list missing themes: %q", out)
	}
	if !strings.Contains(out, "* default-dark") {
		t.Fatalf("active theme marker missing: %q", out)
	}

	out, _, _ = m.handleCommand("/theme dracula")
	if !strings.Contains(out, "dracula") {
		t.Fatalf("set message missing: %q", out)
	}
	if theme.Active().Name != "dracula" {
		t.Fatalf("active not switched, got %s", theme.Active().Name)
	}

	out, _, _ = m.handleCommand("/theme no-such-theme")
	if !strings.Contains(out, "not found") {
		t.Fatalf("expected not-found message, got %q", out)
	}
}

func TestCmdHooksEmpty(t *testing.T) {
	m := model{
		hasStatus: true,
		status:    channel.Status{HookCount: 0},
	}
	out := m.cmdHooks()
	if !strings.Contains(out, "no hooks") {
		t.Fatalf("expected 'no hooks' on empty list, got %q", out)
	}
}

func TestCmdHooksListsEntries(t *testing.T) {
	m := model{
		hasStatus: true,
		status:    channel.Status{HookCount: 2},
		hookEntries: []hooks.Entry{
			{Event: hooks.EventPreToolUse, Matcher: hooks.Matcher{Tool: "Bash"},
				Command: []string{"/bin/sh", "-c", "..."}, TimeoutSeconds: 5, Source: "policy"},
			{Event: hooks.EventStop, Command: []string{"/bin/sh", "-c", "..."},
				TimeoutSeconds: 5, Source: "audit"},
		},
	}
	out := m.cmdHooks()
	for _, want := range []string{"PreToolUse", "Bash", "policy", "Stop", "audit", "5s"} {
		if !strings.Contains(out, want) {
			t.Fatalf("/hooks output missing %q, got:\n%s", want, out)
		}
	}
}

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
	out, quit, _ := m.handleCommand("/stats")
	if quit {
		t.Fatalf("/stats should not quit")
	}
	for _, want := range []string{"claude-opus", "buffer", "1d", "mem", "messages", "vectors"} {
		if !strings.Contains(out, want) {
			t.Errorf("/stats missing %q in:\n%s", want, out)
		}
	}
	if strings.Contains(out, "hist ") {
		t.Errorf("/stats should not use legacy \"hist\" label:\n%s", out)
	}
}

func TestToolsCommand(t *testing.T) {
	m := newModel(80, 24)
	m.status = statusFixture()
	m.hasStatus = true
	out, _, _ := m.handleCommand("/tools")
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
	out, _, _ := m.handleCommand("/agents")
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
	out, _, _ := m.handleCommand("/model")
	for _, want := range []string{"anthropic", "claude-opus", "fallback", "#1"} {
		if !strings.Contains(out, want) {
			t.Errorf("/model missing %q in:\n%s", want, out)
		}
	}
}

func TestUnknownCommand(t *testing.T) {
	m := newModel(80, 24)
	out, _, _ := m.handleCommand("/nope")
	if !strings.Contains(out, "unknown") {
		t.Errorf("expected unknown-command hint, got %q", out)
	}
}

func TestQuitCommand(t *testing.T) {
	m := newModel(80, 24)
	_, quit, _ := m.handleCommand("/quit")
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
	out, quit, _ := m.handleCommand("/skills")
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
	out, _, _ := m.handleCommand("/skills")
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
	out, _, _ := m.handleCommand("/mcp")
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
	out, _, _ := m.handleCommand("/mcp")
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
	out, quit, _ := m.handleCommand("/lsp")
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
	out, _, _ := m.handleCommand("/lsp")
	if !strings.Contains(out, "no LSP") && !strings.Contains(out, "no lsp") {
		t.Errorf("/lsp empty out = %q, want 'no LSP...'", out)
	}
}

func TestReloadCallsPluginBackend(t *testing.T) {
	p := newFakePlugins()
	m := newModel(80, 24)
	m.plugins = p
	out, quit, _ := m.handleCommand("/reload")
	if quit {
		t.Fatalf("/reload must not quit")
	}
	if !strings.Contains(out, "reloaded") {
		t.Errorf("/reload output = %q, want substring 'reloaded'", out)
	}
	if p.rebuilds != 1 {
		t.Errorf("Rebuild called %d times, want 1", p.rebuilds)
	}
}

func TestReloadWithoutBackend(t *testing.T) {
	m := newModel(80, 24)
	out, _, _ := m.handleCommand("/reload")
	if !strings.Contains(out, "not available") {
		t.Errorf("/reload without backend = %q, want 'not available' hint", out)
	}
}

func TestRenderHelpOverlayContents(t *testing.T) {
	m := newModel(80, 24)
	out := m.renderHelpOverlay()
	for _, want := range []string{"Enter", "Alt+Enter", "Ctrl+L", "Ctrl+R", "Ctrl+T", "Ctrl+/", "/reload", "/quit", "/new", "/about", "create_skill"} {
		if !strings.Contains(out, want) {
			t.Errorf("help overlay missing %q, got:\n%s", want, out)
		}
	}
}

// fakeCreatorBackend records the most recent create call for assertion.
type fakeCreatorBackend struct {
	kind   string
	name   string
	desc   string
	body   string
	tools  []string
	hint   string
	called int
}

func (f *fakeCreatorBackend) CreateSkill(_ context.Context, name, desc, body string) (string, error) {
	f.called++
	f.kind, f.name, f.desc, f.body = "skill", name, desc, body
	return "/tmp/" + name + "/SKILL.md", nil
}

func (f *fakeCreatorBackend) CreateAgent(_ context.Context, name, desc string, tools []string, body string) (string, error) {
	f.called++
	f.kind, f.name, f.desc, f.tools, f.body = "agent", name, desc, tools, body
	return "/tmp/" + name + ".md", nil
}

func (f *fakeCreatorBackend) CreateCommand(_ context.Context, name, desc, hint, body string) (string, error) {
	f.called++
	f.kind, f.name, f.desc, f.hint, f.body = "command", name, desc, hint, body
	return "/tmp/" + name + ".md", nil
}

func TestHandleCommand_NewUsage(t *testing.T) {
	m := newModel(80, 24)
	out, quit, wiz := m.handleCommand("/new")
	if quit {
		t.Fatalf("/new must not quit")
	}
	if wiz != nil {
		t.Fatalf("/new with no kind must not install a wizard; got %+v", wiz)
	}
	if !strings.Contains(out, "/new skill|agent|command") {
		t.Fatalf("/new usage missing kind list: %q", out)
	}
}

func TestHandleCommand_NewUnknownKind(t *testing.T) {
	m := newModel(80, 24)
	out, _, wiz := m.handleCommand("/new foo bar")
	if wiz != nil {
		t.Fatalf("unknown kind must not install a wizard; got %+v", wiz)
	}
	if !strings.Contains(out, "unknown kind") {
		t.Fatalf("expected unknown-kind error: %q", out)
	}
}

func TestHandleCommand_NewSkillInstallsWizard(t *testing.T) {
	m := newModel(80, 24)
	out, _, wiz := m.handleCommand("/new skill explain-go")
	if wiz == nil {
		t.Fatalf("expected wizard pointer to install")
	}
	if wiz.Kind != creator.WizardKindSkill || wiz.Name != "explain-go" {
		t.Fatalf("wizard fields wrong: %+v", wiz)
	}
	if wiz.Step != creator.WizardStepDescription {
		t.Fatalf("expected description step (name was supplied); got %v", wiz.Step)
	}
	if !strings.Contains(out, "description?") {
		t.Fatalf("expected description prompt in initial message; got: %q", out)
	}
}

func TestHandleCommand_CancelClearsWizard(t *testing.T) {
	m := newModel(80, 24)
	_, _, wiz := m.handleCommand("/new skill foo")
	if wiz == nil {
		t.Fatalf("setup: wizard not installed")
	}
	m.wizard = wiz
	out, _, _ := m.handleCommand("/cancel")
	if !strings.Contains(out, "cancelled") {
		t.Fatalf("expected cancellation message; got: %q", out)
	}
	if m.wizard != nil {
		t.Fatalf("wizard should clear on /cancel; got: %+v", m.wizard)
	}
}

func TestHandleCommand_CancelWithoutWizard(t *testing.T) {
	m := newModel(80, 24)
	out, _, _ := m.handleCommand("/cancel")
	if !strings.Contains(out, "nothing to cancel") {
		t.Fatalf("expected 'nothing to cancel' hint; got: %q", out)
	}
}

// TestCmdAbout asserts /about returns the welcome ASCII art, the tagline,
// and the active theme name.
func TestCmdAbout(t *testing.T) {
	theme.LoadBuiltins()
	dracula, _ := theme.Get("dracula")
	theme.Set(dracula)

	m := newModel(80, 24)
	out, quit, _ := m.handleCommand("/about")
	if quit {
		t.Fatalf("/about should not quit")
	}
	if !strings.Contains(out, "╭─╮") {
		t.Fatalf("/about missing welcome art glyph: %q", out)
	}
	if !strings.Contains(out, "personal AI assistant") {
		t.Fatalf("/about missing tagline: %q", out)
	}
	if !strings.Contains(out, "dracula") {
		t.Fatalf("/about missing active theme name: %q", out)
	}
}
