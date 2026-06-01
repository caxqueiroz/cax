package workspace

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

// pythiaLike builds:
//
//	<root>/.git/HEAD
//	<root>/clob-engine/go.mod
//	<root>/liquidity-provider/go.mod
//	<root>/payment-gateway/package.json
//	<root>/docs/                 (no marker — should be skipped)
//	<root>/.hidden/go.mod        (hidden — should be skipped)
//
// Returns the absolute root.
func pythiaLike(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	mk := func(rel, body string) {
		full := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	mk(".git/HEAD", "ref: refs/heads/main\n")
	mk("clob-engine/go.mod", "module clob\n")
	mk("liquidity-provider/go.mod", "module lp\n")
	mk("payment-gateway/package.json", "{}\n")
	mk("docs/index.md", "")
	mk(".hidden/go.mod", "module hidden\n")
	return root
}

func TestDiscover_FindsSiblingsUnderGitRoot(t *testing.T) {
	root := pythiaLike(t)
	t.Setenv("HOME", filepath.Dir(root)) // bound the climb above /tmp pollution
	// Start one level deep — Discover should walk up to root.
	gotRoot, children, err := Discover(filepath.Join(root, "clob-engine"))
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if gotRoot != root {
		t.Fatalf("root: got %q, want %q", gotRoot, root)
	}
	gotNames := make([]string, len(children))
	for i, c := range children {
		gotNames[i] = c.Name
	}
	slices.Sort(gotNames)
	wantNames := []string{"clob-engine", "liquidity-provider", "payment-gateway"}
	if !slices.Equal(gotNames, wantNames) {
		t.Fatalf("children: got %v, want %v", gotNames, wantNames)
	}
}

// TestDiscover_MultiRepoParentWithoutOwnGit verifies the "Pythia layout":
// parent dir has NO .git of its own, but each child is its own repo. The
// old discover required a .git ancestor and failed; the new one trips on
// 2+ child projects regardless of parent's git status.
func TestDiscover_MultiRepoParentWithoutOwnGit(t *testing.T) {
	parent := t.TempDir()
	t.Setenv("HOME", filepath.Dir(parent))
	for _, svc := range []string{"clob-engine", "liquidity-provider", "risk-engine"} {
		gitDir := filepath.Join(parent, svc, ".git")
		if err := os.MkdirAll(gitDir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(gitDir, "HEAD"), []byte("ref\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// NOTE: parent itself has NO .git.
	gotRoot, children, err := Discover(filepath.Join(parent, "clob-engine"))
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if gotRoot != parent {
		t.Fatalf("root: got %q, want %q", gotRoot, parent)
	}
	if len(children) != 3 {
		t.Fatalf("want 3 children, got %d", len(children))
	}
}

// TestDiscover_SingleProjectReturnsEmpty: a plain solo repo has no
// siblings, so Discover should NOT promote it to a workspace — return
// empty so the user adds it explicitly with /workspace add if they want.
//
// HOME is pinned to the test's own temp boundary so the walk-up doesn't
// climb into /tmp and pick up unrelated marker dirs.
func TestDiscover_SingleProjectReturnsEmpty(t *testing.T) {
	parent := t.TempDir()
	t.Setenv("HOME", parent)
	repo := filepath.Join(parent, "solo")
	if err := os.MkdirAll(filepath.Join(repo, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "go.mod"), []byte("module x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	root, children, err := Discover(repo)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	// Walk-up hits HOME (=parent) which has only one child project — below
	// the threshold — so Discover returns empty.
	if root != "" || len(children) != 0 {
		t.Fatalf("expected empty for solo repo, got root=%q children=%d", root, len(children))
	}
}

func TestDiscover_NoProjectsAnywhereReturnsEmpty(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	root, children, err := Discover(dir)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if root != "" || children != nil {
		t.Fatalf("expected empty result, got root=%q children=%v", root, children)
	}
}

func TestAddRejectsDuplicatesAndPersists(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "workspace.json")
	w, err := New(statePath)
	if err != nil {
		t.Fatal(err)
	}
	p := t.TempDir()
	e, err := w.Add("", p)
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	if e.Name != filepath.Base(p) {
		t.Errorf("auto-name: got %q, want %q", e.Name, filepath.Base(p))
	}
	if _, err := w.Add("dup", p); err == nil {
		t.Fatal("expected duplicate-path error")
	}

	// New workspace from the same statePath reads back the persisted entry.
	w2, err := New(statePath)
	if err != nil {
		t.Fatal(err)
	}
	if got := w2.Entries(); len(got) != 1 || got[0].Path != p {
		t.Fatalf("persistence: got %+v", got)
	}
}

func TestRemove(t *testing.T) {
	w, _ := New("")
	a, _ := w.Add("alpha", t.TempDir())
	_, _ = w.Add("beta", t.TempDir())
	if _, err := w.Remove("alpha"); err != nil {
		t.Fatal(err)
	}
	if got := w.Entries(); len(got) != 1 || got[0].Name != "beta" {
		t.Fatalf("after remove alpha: %+v", got)
	}
	if _, err := w.Remove(a.Path); err == nil {
		t.Fatal("expected error removing already-removed entry")
	}
}

func TestMatchingNames_NarrowsByService(t *testing.T) {
	w, _ := New("")
	_, _ = w.Add("clob-engine", t.TempDir())
	_, _ = w.Add("liquidity-provider", t.TempDir())
	_, _ = w.Add("risk-engine", t.TempDir())

	got := w.MatchingNames("how does clob-engine talk to risk-engine?")
	if len(got) != 2 {
		t.Fatalf("want 2 matches, got %d (%+v)", len(got), got)
	}
	names := []string{got[0].Name, got[1].Name}
	slices.Sort(names)
	if !slices.Equal(names, []string{"clob-engine", "risk-engine"}) {
		t.Fatalf("names: %v", names)
	}
}

func TestMatchingNames_NoFalseSubstrings(t *testing.T) {
	w, _ := New("")
	_, _ = w.Add("engine", t.TempDir())
	// "clobengine" should NOT match "engine" because "engine" needs to be
	// a word boundary.
	got := w.MatchingNames("about clobengine internals")
	if len(got) != 0 {
		t.Fatalf("substring should not match, got: %+v", got)
	}
}

func TestMatchingNames_HyphenIsAWordBoundary(t *testing.T) {
	w, _ := New("")
	_, _ = w.Add("clob-engine", t.TempDir())
	got := w.MatchingNames("how does clob-engine route orders?")
	if len(got) != 1 || got[0].Name != "clob-engine" {
		t.Fatalf("hyphen boundary: %+v", got)
	}
}

func TestFind_ByNameOrPath(t *testing.T) {
	w, _ := New("")
	p := t.TempDir()
	_, _ = w.Add("foo", p)
	if e := w.Find("foo"); e == nil || e.Path != p {
		t.Fatalf("Find by name: %+v", e)
	}
	if e := w.Find(p); e == nil || e.Name != "foo" {
		t.Fatalf("Find by path: %+v", e)
	}
	if e := w.Find("missing"); e != nil {
		t.Fatalf("Find missing should be nil, got %+v", e)
	}
}
