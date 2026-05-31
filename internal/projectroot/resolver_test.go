package projectroot

import (
	"os"
	"path/filepath"
	"testing"
)

// makeTree builds: <root>/srv/a/cmd/main.go where <root>/.git exists and
// <root>/srv/a/go.mod exists. Returns the absolute root path.
func makeTree(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	mkfile := func(rel, body string) {
		full := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	mkfile(".git/HEAD", "ref: refs/heads/main\n")
	mkfile("srv/a/go.mod", "module a\n")
	mkfile("srv/a/cmd/main.go", "package main\n")
	mkfile("srv/b/package.json", "{}\n")
	mkfile("srv/b/index.js", "")
	return root
}

func TestWalkUp_FindsInnermostMarker(t *testing.T) {
	root := makeTree(t)
	// From inside srv/a/cmd: should find go.mod at srv/a (NOT the .git at root).
	got := WalkUp(filepath.Join(root, "srv/a/cmd"))
	want := filepath.Join(root, "srv/a")
	if got != want {
		t.Fatalf("walk-up: got %q, want %q", got, want)
	}
}

func TestWalkUp_FallsThroughToGitWhenNoNearerMarker(t *testing.T) {
	root := makeTree(t)
	// Empty intermediate dir: no go.mod here; walk-up climbs to root for .git.
	emptyDir := filepath.Join(root, "srv/empty")
	if err := os.MkdirAll(emptyDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if got := WalkUp(emptyDir); got != root {
		t.Fatalf("expected to fall through to %q, got %q", root, got)
	}
}

func TestWalkUp_AcceptsFilePath(t *testing.T) {
	root := makeTree(t)
	// Pass a file path, not a directory — walk-up should start from its parent.
	got := WalkUp(filepath.Join(root, "srv/a/cmd/main.go"))
	want := filepath.Join(root, "srv/a")
	if got != want {
		t.Fatalf("file path walk-up: got %q, want %q", got, want)
	}
}

func TestResolver_OverrideWinsOverEverything(t *testing.T) {
	root := makeTree(t)
	r := NewWithLaunchCWD(filepath.Join(root, "srv/a/cmd"))
	override := filepath.Join(root, "srv/b")
	if _, err := r.SetOverride(override); err != nil {
		t.Fatalf("SetOverride: %v", err)
	}
	// Query mentions a different valid path — override still wins.
	got := r.For("look at " + filepath.Join(root, "srv/a") + " please")
	if got != override {
		t.Fatalf("override should win: got %q, want %q", got, override)
	}
}

func TestResolver_DetectsPathInQuery(t *testing.T) {
	root := makeTree(t)
	r := NewWithLaunchCWD(filepath.Join(root, "srv/a"))
	q := "what does " + filepath.Join(root, "srv/b") + " export?"
	got := r.For(q)
	want := filepath.Join(root, "srv/b")
	if got != want {
		t.Fatalf("query-path detection: got %q, want %q", got, want)
	}
}

func TestResolver_PrefersLongestPathInQuery(t *testing.T) {
	root := makeTree(t)
	r := NewWithLaunchCWD(filepath.Join(root, "srv/a"))
	// Both paths exist; longer one (srv/a/cmd) is more specific.
	q := "compare " + root + " with " + filepath.Join(root, "srv/a/cmd")
	got := r.For(q)
	want := filepath.Join(root, "srv/a")
	// The detected path was srv/a/cmd → walks up to srv/a (go.mod marker).
	if got != want {
		t.Fatalf("longest-path: got %q, want %q", got, want)
	}
}

func TestResolver_FallsBackToWalkUpFromLaunchCWD(t *testing.T) {
	root := makeTree(t)
	r := NewWithLaunchCWD(filepath.Join(root, "srv/a/cmd"))
	got := r.For("any prompt without paths")
	want := filepath.Join(root, "srv/a")
	if got != want {
		t.Fatalf("launch walk-up: got %q, want %q", got, want)
	}
}

func TestResolver_NoSignalsReturnsEmpty(t *testing.T) {
	dir := t.TempDir() // no markers anywhere up the tree
	r := NewWithLaunchCWD(dir)
	// With no launch cwd and no markers, we get the dir itself as last resort.
	if got := r.For("hi"); got != dir {
		t.Fatalf("with launchCWD set, expect dir fallback %q, got %q", dir, got)
	}
}

func TestResolver_ClearOverrideRestoresAutoDetection(t *testing.T) {
	root := makeTree(t)
	r := NewWithLaunchCWD(filepath.Join(root, "srv/a"))
	_, _ = r.SetOverride(filepath.Join(root, "srv/b"))
	r.ClearOverride()
	got := r.For("anything")
	want := filepath.Join(root, "srv/a")
	if got != want {
		t.Fatalf("after clear: got %q, want %q", got, want)
	}
}

func TestSetOverride_RejectsBadPath(t *testing.T) {
	r := New()
	if _, err := r.SetOverride("/no/such/path/should/exist/xyz"); err == nil {
		t.Fatal("expected error for nonexistent path")
	}
	if r.Override() != "" {
		t.Fatal("override should remain unset after failure")
	}
}

func TestSetOverride_RejectsRegularFile(t *testing.T) {
	r := New()
	f := filepath.Join(t.TempDir(), "f.txt")
	if err := os.WriteFile(f, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := r.SetOverride(f); err == nil {
		t.Fatal("expected error for file path (not a directory)")
	}
}
