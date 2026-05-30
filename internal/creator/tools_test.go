package creator

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/deepnoodle-ai/dive"
)

// fakeReloader records each Rebuild call so tests can assert the
// reload-after-write contract without spinning up a real *dive.Agent.
type fakeReloader struct {
	calls atomic.Int32
	err   error
}

func (f *fakeReloader) Rebuild(_ context.Context) error {
	f.calls.Add(1)
	return f.err
}

func newWriter(t *testing.T) (Writer, string) {
	t.Helper()
	home := t.TempDir()
	return Writer{
		SkillsDir:   filepath.Join(home, "skills"),
		AgentsDir:   filepath.Join(home, "agents"),
		CommandsDir: filepath.Join(home, "commands"),
	}, home
}

// callTool finds a tool by name and invokes it via its dive.Tool surface.
// dive's Tool.Call accepts `any`; we pass a JSON []byte that the FuncTool
// adapter unmarshals into the typed input struct.
func callTool(t *testing.T, tools []dive.Tool, name string, inputJSON string) *dive.ToolResult {
	t.Helper()
	for _, tl := range tools {
		if tl.Name() == name {
			res, err := tl.Call(context.Background(), []byte(inputJSON))
			if err != nil {
				t.Fatalf("tool %s call: %v", name, err)
			}
			return res
		}
	}
	t.Fatalf("tool %s not registered", name)
	return nil
}

// toolResultText returns the concatenated text content of a dive.ToolResult.
// dive's ToolResult content blocks each carry a Text field; for create tools
// the result is always a single text block produced by NewToolResultText.
func toolResultText(r *dive.ToolResult) string {
	var sb strings.Builder
	for _, c := range r.Content {
		sb.WriteString(c.Text)
	}
	return sb.String()
}

func TestTools_RegistersThreeNames(t *testing.T) {
	w, _ := newWriter(t)
	got := Tools(w, &fakeReloader{})
	names := map[string]bool{}
	for _, tl := range got {
		names[tl.Name()] = true
	}
	for _, want := range []string{"create_skill", "create_agent", "create_command"} {
		if !names[want] {
			t.Fatalf("missing tool %q; got %v", want, names)
		}
	}
}

func TestCreateSkill_WritesFileAndRebuilds(t *testing.T) {
	w, _ := newWriter(t)
	fr := &fakeReloader{}
	tools := Tools(w, fr)
	res := callTool(t, tools, "create_skill", `{
        "name":"explain-go-embedding",
        "description":"Explain Go embedding succinctly.",
        "body":"Use a worked example.\n"
    }`)
	if res == nil || res.IsError {
		t.Fatalf("create_skill tool returned error: %+v (text=%q)", res, toolResultText(res))
	}
	if fr.calls.Load() != 1 {
		t.Fatalf("Reloader.Rebuild calls = %d, want 1", fr.calls.Load())
	}
	if _, err := os.Stat(filepath.Join(w.SkillsDir, "explain-go-embedding", "SKILL.md")); err != nil {
		t.Fatalf("expected SKILL.md to exist: %v", err)
	}
	out := toolResultText(res)
	if !strings.Contains(out, "SKILL.md") {
		t.Fatalf("expected tool result to mention SKILL.md; got: %q", out)
	}
}

func TestCreateAgent_WritesFileAndRebuilds(t *testing.T) {
	w, _ := newWriter(t)
	fr := &fakeReloader{}
	tools := Tools(w, fr)
	res := callTool(t, tools, "create_agent", `{
        "name":"reviewer",
        "description":"Reviews Go diffs.",
        "tools":["Read","Glob"],
        "body":"Be terse.\n"
    }`)
	if res == nil || res.IsError {
		t.Fatalf("create_agent error: %+v (text=%q)", res, toolResultText(res))
	}
	if fr.calls.Load() != 1 {
		t.Fatalf("Reloader.Rebuild calls = %d, want 1", fr.calls.Load())
	}
	if _, err := os.Stat(filepath.Join(w.AgentsDir, "reviewer.md")); err != nil {
		t.Fatalf("expected reviewer.md: %v", err)
	}
}

func TestCreateCommand_WritesFileAndRebuilds(t *testing.T) {
	w, _ := newWriter(t)
	fr := &fakeReloader{}
	tools := Tools(w, fr)
	res := callTool(t, tools, "create_command", `{
        "name":"greet",
        "description":"Greet the user.",
        "argument_hint":"<name>",
        "body":"Hello $ARGUMENTS!\n"
    }`)
	if res == nil || res.IsError {
		t.Fatalf("create_command error: %+v (text=%q)", res, toolResultText(res))
	}
	if fr.calls.Load() != 1 {
		t.Fatalf("Reloader.Rebuild calls = %d, want 1", fr.calls.Load())
	}
	if _, err := os.Stat(filepath.Join(w.CommandsDir, "greet.md")); err != nil {
		t.Fatalf("expected greet.md: %v", err)
	}
}

func TestCreateSkill_ConflictReturnsToolError(t *testing.T) {
	w, _ := newWriter(t)
	fr := &fakeReloader{}
	tools := Tools(w, fr)
	_ = callTool(t, tools, "create_skill", `{"name":"dup","description":"d","body":"b\n"}`)
	res := callTool(t, tools, "create_skill", `{"name":"dup","description":"d2","body":"b2\n"}`)
	if res == nil || !res.IsError {
		t.Fatalf("expected IsError tool result on conflict; got: %+v", res)
	}
	if fr.calls.Load() != 1 {
		t.Fatalf("Reloader.Rebuild calls = %d, want 1 (no reload on failure)", fr.calls.Load())
	}
}

func TestCreateSkill_InvalidNameReturnsToolError(t *testing.T) {
	w, _ := newWriter(t)
	fr := &fakeReloader{}
	tools := Tools(w, fr)
	res := callTool(t, tools, "create_skill", `{"name":"BAD/NAME","description":"d","body":"b\n"}`)
	if res == nil || !res.IsError {
		t.Fatalf("expected IsError on invalid name; got: %+v", res)
	}
	if fr.calls.Load() != 0 {
		t.Fatalf("Reloader.Rebuild calls = %d, want 0 (no reload on validation failure)", fr.calls.Load())
	}
}
