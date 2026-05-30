package usercmds

import (
	"os"
	"path/filepath"
	"testing"
)

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
}

func TestLoadParsesFrontmatter(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "hello.md"), `---
description: greet
argument-hint: <name>
---

hello $ARGUMENTS
`)
	writeFile(t, filepath.Join(dir, "no-fm.md"), "plain body, no frontmatter\n")
	writeFile(t, filepath.Join(dir, "ignored.txt"), "not markdown")
	cmds := Load([]string{dir})
	if len(cmds) != 2 {
		t.Fatalf("len = %d, want 2; got %+v", len(cmds), cmds)
	}
	byName := map[string]bool{cmds[0].Name: true, cmds[1].Name: true}
	if !byName["hello"] || !byName["no-fm"] {
		t.Fatalf("names = %+v", byName)
	}
	var hello = cmds[0]
	if cmds[1].Name == "hello" {
		hello = cmds[1]
	}
	if hello.Description != "greet" {
		t.Errorf("Description = %q, want greet", hello.Description)
	}
	if hello.ArgumentHint != "<name>" {
		t.Errorf("ArgumentHint = %q, want <name>", hello.ArgumentHint)
	}
	if hello.Prompt != "hello $ARGUMENTS\n" {
		t.Errorf("Prompt = %q, want %q", hello.Prompt, "hello $ARGUMENTS\n")
	}
	if want := "user:" + filepath.Base(dir); hello.Source != want {
		t.Errorf("Source = %q, want %q", hello.Source, want)
	}
}

func TestLoadSkipsMissingAndMalformed(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "good.md"), `---
description: ok
---
body
`)
	writeFile(t, filepath.Join(dir, "bad.md"), `---
description: [not, valid, yaml: scalar
---
body
`)
	// Pass two dirs: one missing, one real. Missing must be silently skipped.
	cmds := Load([]string{filepath.Join(dir, "does-not-exist"), dir})
	if len(cmds) != 2 {
		t.Fatalf("len = %d, want 2 (good + bad with empty desc), got %+v", len(cmds), cmds)
	}
	// Both files should be returned even when frontmatter fails to parse; the
	// parser logs and continues with empty meta (mirrors plugins.ReadCommands).
}

func TestLoadDeterministicOrder(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "c.md"), "body\n")
	writeFile(t, filepath.Join(dir, "a.md"), "body\n")
	writeFile(t, filepath.Join(dir, "b.md"), "body\n")
	cmds := Load([]string{dir})
	if len(cmds) != 3 || cmds[0].Name != "a" || cmds[1].Name != "b" || cmds[2].Name != "c" {
		t.Fatalf("order = %+v, want [a b c]", cmds)
	}
}
