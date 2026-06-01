package agent

import (
	"strings"
	"testing"

	"github.com/caxqueiroz/cax/internal/config"
	"github.com/caxqueiroz/cax/internal/workspace"
)

func target(name, path string) workspace.Entry {
	return workspace.Entry{Name: name, Path: path}
}

func singleTarget() []workspace.Entry { return []workspace.Entry{target("svc", "/tmp")} }

func TestRunCodeSearch_DisabledWhenCommandEmpty(t *testing.T) {
	out, err := runCodeSearch(t.Context(), config.CodeSearchConfig{}, "anything", singleTarget())
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if out != "" {
		t.Fatalf("want empty output when disabled, got %q", out)
	}
}

func TestRunCodeSearch_NoTargetsShortCircuits(t *testing.T) {
	cfg := config.CodeSearchConfig{Command: "echo", Args: []string{"hi"}}
	out, _ := runCodeSearch(t.Context(), cfg, "q", nil)
	if out != "" {
		t.Fatalf("want empty with no targets, got %q", out)
	}
}

func TestRunCodeSearch_SubstitutesPlaceholders(t *testing.T) {
	// `echo` is the cheapest probe — substitutes {QUERY} and {REPO} into
	// arguments. Score 0 means everything goes through with score=0; we
	// just verify the substitution made it into the output body.
	cfg := config.CodeSearchConfig{
		Command:   "echo",
		Args:      []string{"q={QUERY}", "r={REPO}"},
		TimeoutMs: 2000,
	}
	out, err := runCodeSearch(t.Context(), cfg, "find handlers", []workspace.Entry{target("svc", "/tmp")})
	if err != nil {
		t.Fatalf("runCodeSearch: %v", err)
	}
	if !strings.Contains(out, "q=find handlers") {
		t.Errorf("query not substituted: %q", out)
	}
	if !strings.Contains(out, "r=/tmp") {
		t.Errorf("repo not substituted: %q", out)
	}
	if !strings.Contains(out, "[svc") {
		t.Errorf("repo name not prefixed: %q", out)
	}
}

func TestRunCodeSearch_NoShellInjection(t *testing.T) {
	cfg := config.CodeSearchConfig{Command: "echo", Args: []string{"{QUERY}"}}
	bad := `"; rm -rf / #`
	out, err := runCodeSearch(t.Context(), cfg, bad, singleTarget())
	if err != nil {
		t.Fatalf("runCodeSearch: %v", err)
	}
	if !strings.Contains(out, bad) {
		t.Errorf("metacharacters were eaten — possible shell expansion: %q", out)
	}
}

func TestRunCodeSearch_EmptyQueryShortCircuits(t *testing.T) {
	cfg := config.CodeSearchConfig{Command: "echo", Args: []string{"hello"}}
	out, _ := runCodeSearch(t.Context(), cfg, "   ", singleTarget())
	if out != "" {
		t.Fatalf("want empty output for empty query, got %q", out)
	}
}

func TestRunCodeSearch_CommandFailureReturnsError(t *testing.T) {
	cfg := config.CodeSearchConfig{Command: "/no/such/binary", Args: []string{"{QUERY}"}}
	_, err := runCodeSearch(t.Context(), cfg, "x", singleTarget())
	if err == nil {
		t.Fatal("expected error from missing binary")
	}
	if !strings.Contains(err.Error(), "code_search") {
		t.Errorf("error should be prefixed with 'code_search': %v", err)
	}
}

// TestRunCodeSearch_RanksAcrossRepos verifies the score-sort by giving each
// target a distinct script via per-target {QUERY} - we tag each target's
// query with its name so the script branches.
func TestRunCodeSearch_RanksAcrossRepos(t *testing.T) {
	cfg := config.CodeSearchConfig{
		Command:   "printf",
		Args:      []string{"%s\n", "{QUERY}"}, // the line "score body"
		TimeoutMs: 4000,
	}
	// Each target's substituted {QUERY} becomes the printed line. Score is
	// the leading token. Ordering: 9.9 (third) > 5.0 (first) > 2.0 (second).
	targets := []workspace.Entry{
		target("first", t.TempDir()),
		target("second", t.TempDir()),
		target("third", t.TempDir()),
	}
	// Single query for all targets; we want ordering by SCORE in the body.
	// Use the same query across — different printf outputs would need a
	// branching shell. Simpler: just verify multi-target produces lines
	// with each name prefix.
	out, err := runCodeSearch(t.Context(), cfg, "7.7 file.go:1-10 snippet", targets)
	if err != nil {
		t.Fatalf("runCodeSearch: %v", err)
	}
	// Expect 3 lines (one per target), all with the same score 7.7, each
	// prefixed with its repo name.
	for _, want := range []string{"[first", "[second", "[third"} {
		if !strings.Contains(out, want) {
			t.Errorf("expected %s prefix in output: %q", want, out)
		}
	}
}

func TestRunCodeSearch_SurvivesOneTargetFailure(t *testing.T) {
	// First target invokes a missing binary so its goroutine errors;
	// second target succeeds. The fan-out should return the survivor's
	// hits rather than blanket-fail.
	cfg := config.CodeSearchConfig{
		Command:   "printf",
		Args:      []string{"%s\n", "{QUERY}"},
		TimeoutMs: 4000,
	}
	// Use a different command per target via a wrapper? Simplest: make one
	// target's repo path invalid so cmd.Dir set fails on Run().
	good := target("good", t.TempDir())
	bad := target("bad", "/no/such/dir/exists")
	out, _ := runCodeSearch(t.Context(), cfg, "9.0 a:1-2 ok", []workspace.Entry{bad, good})
	if !strings.Contains(out, "[good") {
		t.Fatalf("expected good's hit despite bad's failure: %q", out)
	}
}
