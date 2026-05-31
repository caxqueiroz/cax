package agent

import (
	"strings"
	"testing"

	"github.com/caxqueiroz/cax/internal/config"
)

func TestRunCodeSearch_DisabledWhenCommandEmpty(t *testing.T) {
	out, err := runCodeSearch(t.Context(), config.CodeSearchConfig{}, "anything", "")
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if out != "" {
		t.Fatalf("want empty output when disabled, got %q", out)
	}
}

func TestRunCodeSearch_SubstitutesPlaceholders(t *testing.T) {
	// `echo` is the cheapest probe — substitutes {QUERY} and {REPO} into
	// arguments and we assert the captured stdout has them inlined.
	cfg := config.CodeSearchConfig{
		Command:   "echo",
		Args:      []string{"q={QUERY}", "r={REPO}"},
		RepoRoot:  "/tmp",
		TimeoutMs: 2000,
	}
	out, err := runCodeSearch(t.Context(), cfg, "find handlers", "")
	if err != nil {
		t.Fatalf("runCodeSearch: %v", err)
	}
	if !strings.Contains(out, "q=find handlers") {
		t.Errorf("query not substituted: %q", out)
	}
	if !strings.Contains(out, "r=/tmp") {
		t.Errorf("repo not substituted: %q", out)
	}
}

func TestRunCodeSearch_NoShellInjection(t *testing.T) {
	// A query containing shell metacharacters must NOT be expanded by a
	// shell. The whole string lands in one argv slot intact.
	cfg := config.CodeSearchConfig{
		Command: "echo",
		Args:    []string{"{QUERY}"},
	}
	bad := `"; rm -rf / #`
	out, err := runCodeSearch(t.Context(), cfg, bad, "")
	if err != nil {
		t.Fatalf("runCodeSearch: %v", err)
	}
	if !strings.Contains(out, bad) {
		t.Errorf("metacharacters were eaten — possible shell expansion: %q", out)
	}
}

func TestRunCodeSearch_EmptyQueryShortCircuits(t *testing.T) {
	cfg := config.CodeSearchConfig{Command: "echo", Args: []string{"hello"}}
	out, _ := runCodeSearch(t.Context(), cfg, "   ", "")
	if out != "" {
		t.Fatalf("want empty output for empty query, got %q", out)
	}
}

func TestRunCodeSearch_CommandFailureReturnsError(t *testing.T) {
	cfg := config.CodeSearchConfig{
		Command: "/no/such/binary",
		Args:    []string{"{QUERY}"},
	}
	_, err := runCodeSearch(t.Context(), cfg, "x", "")
	if err == nil {
		t.Fatal("expected error from missing binary")
	}
	if !strings.Contains(err.Error(), "code_search") {
		t.Errorf("error should be prefixed with 'code_search': %v", err)
	}
}

func TestRunCodeSearch_TruncatesOversizedOutput(t *testing.T) {
	// `yes` floods stdout; we cap at codeSearchMaxBytes so the prompt isn't
	// blown out by a runaway tool.
	cfg := config.CodeSearchConfig{
		Command:   "sh",
		Args:      []string{"-c", "yes lineabcdefghij | head -5000"},
		TimeoutMs: 5000,
	}
	out, err := runCodeSearch(t.Context(), cfg, "x", "")
	if err != nil {
		t.Fatalf("runCodeSearch: %v", err)
	}
	if len(out) > codeSearchMaxBytes+32 {
		t.Errorf("output not truncated: len=%d", len(out))
	}
	if !strings.Contains(out, "[truncated]") {
		t.Errorf("expected truncation marker, got tail: %q", out[max(0, len(out)-100):])
	}
}
