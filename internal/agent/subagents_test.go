package agent

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/caxqueiroz/czcli/internal/config"
)

func buildWithSubagents(t *testing.T, dir string) *Assistant {
	t.Helper()
	store := newTestStore(t)
	cfg := &config.Config{
		Persona:   "czcli",
		Memory:    config.MemoryConfig{TokenBudget: 8000, RecallK: 5},
		Tools:     config.ToolsConfig{FilesEnabled: true},
		Subagents: config.SubagentsConfig{Enabled: true, Dir: dir},
	}
	a, err := Build(context.Background(), cfg, store, newScriptLLM("ok"))
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	return a
}

func TestAugmentTools_AddsTaskAndTaskStop(t *testing.T) {
	a := buildWithSubagents(t, t.TempDir()) // empty dir -> built-ins + general-purpose
	names := map[string]bool{}
	for _, tl := range a.tools {
		names[tl.Name()] = true
	}
	if !names["Task"] {
		t.Fatal("Task tool missing when subagents enabled")
	}
	if !names["TaskStop"] {
		t.Fatal("TaskStop tool missing when subagents enabled")
	}
	// The built-in general-purpose persona must be present.
	got := map[string]bool{}
	for _, n := range a.subagentNames {
		got[n] = true
	}
	if !got["general-purpose"] {
		t.Fatalf("general-purpose missing from catalog: %v", a.subagentNames)
	}
}

func TestAugmentTools_LoadsFileLoaderDefinitions(t *testing.T) {
	dir := t.TempDir()
	md := "---\ndescription: A custom reviewer agent.\n---\nYou are a code reviewer.\n"
	if err := os.WriteFile(filepath.Join(dir, "Reviewer.md"), []byte(md), 0o644); err != nil {
		t.Fatalf("write agent file: %v", err)
	}
	a := buildWithSubagents(t, dir)
	found := false
	for _, n := range a.subagentNames {
		if n == "Reviewer" {
			found = true
		}
	}
	if !found {
		t.Fatalf("file-loaded subagent missing: %v", a.subagentNames)
	}
}

func TestAugmentTools_DisabledByDefault(t *testing.T) {
	a, _ := buildTestAssistant(t, "ok") // Subagents.Enabled = false
	for _, tl := range a.tools {
		if tl.Name() == "Task" {
			t.Fatal("Task tool present though subagents disabled")
		}
	}
	if len(a.subagentNames) != 0 {
		t.Fatalf("expected no subagents, got %v", a.subagentNames)
	}
}
