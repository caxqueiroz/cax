package creator

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/caxqueiroz/czcli/internal/plugins"
)

func TestValidateName(t *testing.T) {
	cases := []struct {
		name    string
		in      string
		wantErr bool
	}{
		{"empty rejected", "", true},
		{"single char ok", "a", false},
		{"lowercase + digits ok", "explain-go-embedding-2", false},
		{"uppercase rejected", "Explain", true},
		{"leading hyphen rejected", "-explain", true},
		{"leading digit ok", "9explain", false},
		{"underscore rejected", "explain_go", true},
		{"slash rejected", "explain/go", true},
		{"backslash rejected", "explain\\go", true},
		{"dot rejected", "explain.go", true},
		{"double-dot rejected (regex catches)", "..", true},
		{"too long rejected", strings.Repeat("a", 65), true},
		{"max length ok", strings.Repeat("a", 64), false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := validateName(c.in)
			if (err != nil) != c.wantErr {
				t.Fatalf("validateName(%q) err=%v wantErr=%v", c.in, err, c.wantErr)
			}
		})
	}
}

func TestWriteSkill_HappyPath(t *testing.T) {
	home := t.TempDir()
	w := Writer{
		SkillsDir:   filepath.Join(home, "skills"),
		AgentsDir:   filepath.Join(home, "agents"),
		CommandsDir: filepath.Join(home, "commands"),
	}
	path, err := w.WriteSkill("explain-go-embedding",
		"Explain Go embedding succinctly.",
		"Use a small worked example.\n", false)
	if err != nil {
		t.Fatalf("WriteSkill: %v", err)
	}
	if !filepath.IsAbs(path) {
		t.Fatalf("WriteSkill returned non-abs path %q", path)
	}
	wantBase := filepath.Join(w.SkillsDir, "explain-go-embedding", "SKILL.md")
	wantAbs, _ := filepath.Abs(wantBase)
	if path != wantAbs {
		t.Fatalf("path = %q want %q", path, wantAbs)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read written file: %v", err)
	}
	if !strings.HasPrefix(string(data), "---\n") {
		t.Fatalf("missing frontmatter open; got: %q", string(data[:minInt(40, len(data))]))
	}
	if !strings.Contains(string(data), "name: explain-go-embedding\n") {
		t.Fatalf("missing name key; got: %s", string(data))
	}
	if !strings.Contains(string(data), "description: Explain Go embedding succinctly.\n") {
		t.Fatalf("missing description; got: %s", string(data))
	}
	if !strings.HasSuffix(string(data), "Use a small worked example.\n") {
		t.Fatalf("body not appended; got: %s", string(data))
	}
}

func TestWriteSkill_ConflictWithoutOverwrite(t *testing.T) {
	home := t.TempDir()
	w := Writer{SkillsDir: filepath.Join(home, "skills")}
	if _, err := w.WriteSkill("foo", "d", "b\n", false); err != nil {
		t.Fatalf("first write: %v", err)
	}
	_, err := w.WriteSkill("foo", "d2", "b2\n", false)
	if err == nil {
		t.Fatalf("expected conflict error on second write")
	}
	if !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("expected 'already exists' in error, got: %v", err)
	}
}

func TestWriteSkill_OverwriteAllowed(t *testing.T) {
	home := t.TempDir()
	w := Writer{SkillsDir: filepath.Join(home, "skills")}
	if _, err := w.WriteSkill("foo", "d", "first\n", false); err != nil {
		t.Fatalf("first write: %v", err)
	}
	path, err := w.WriteSkill("foo", "d", "second\n", true)
	if err != nil {
		t.Fatalf("overwrite: %v", err)
	}
	data, _ := os.ReadFile(path)
	if !strings.Contains(string(data), "second\n") {
		t.Fatalf("overwrite did not replace body; got: %s", string(data))
	}
	if strings.Contains(string(data), "first\n") {
		t.Fatalf("overwrite kept old body; got: %s", string(data))
	}
}

// TestWriteSkill_RoundTrip asserts the on-disk frontmatter parses cleanly
// through internal/plugins.splitFrontmatter shape (description-only is fine —
// SKILL.md's name key is dive-skill-specific, not the cmdFrontmatter shape).
func TestWriteSkill_RoundTrip(t *testing.T) {
	home := t.TempDir()
	w := Writer{SkillsDir: filepath.Join(home, "skills")}
	path, err := w.WriteSkill("rt", "round-trip", "body line\n", false)
	if err != nil {
		t.Fatalf("WriteSkill: %v", err)
	}
	data, _ := os.ReadFile(path)
	fmBytes, body := plugins.SplitFrontmatterForTest(data)
	if len(fmBytes) == 0 {
		t.Fatalf("frontmatter empty; output not parseable: %s", string(data))
	}
	if !strings.HasPrefix(string(body), "body line") {
		t.Fatalf("body mis-split: %q", string(body))
	}
}

func TestWriteAgent_FullFrontmatter(t *testing.T) {
	home := t.TempDir()
	w := Writer{AgentsDir: filepath.Join(home, "agents")}
	path, err := w.WriteAgent("reviewer",
		"Reviews Go diffs.",
		[]string{"Read", "Glob"},
		[]string{"Bash"},
		"Be terse.\n", false)
	if err != nil {
		t.Fatalf("WriteAgent: %v", err)
	}
	data, _ := os.ReadFile(path)
	wantSegments := []string{
		"---\n",
		"description: Reviews Go diffs.\n",
		"tools:\n",
		"  - Read\n",
		"  - Glob\n",
		"disallowed-tools:\n",
		"  - Bash\n",
		"---\n\n",
		"Be terse.\n",
	}
	got := string(data)
	for _, want := range wantSegments {
		if !strings.Contains(got, want) {
			t.Fatalf("agent file missing segment %q in:\n%s", want, got)
		}
	}
}

func TestWriteAgent_OmitsEmptyToolsAndDisallowed(t *testing.T) {
	home := t.TempDir()
	w := Writer{AgentsDir: filepath.Join(home, "agents")}
	path, err := w.WriteAgent("plain", "Plain agent.", nil, nil, "body\n", false)
	if err != nil {
		t.Fatalf("WriteAgent: %v", err)
	}
	data, _ := os.ReadFile(path)
	if strings.Contains(string(data), "tools:") || strings.Contains(string(data), "disallowed-tools:") {
		t.Fatalf("expected no tools/disallowed-tools keys for empty slices; got:\n%s", string(data))
	}
}

func TestWriteCommand_RoundTripWithPlugins(t *testing.T) {
	home := t.TempDir()
	w := Writer{CommandsDir: filepath.Join(home, "commands")}
	path, err := w.WriteCommand("greet",
		"Greet the user.",
		"<name>",
		"Hello $ARGUMENTS!\n", false)
	if err != nil {
		t.Fatalf("WriteCommand: %v", err)
	}
	data, _ := os.ReadFile(path)
	fm, body := plugins.SplitFrontmatterForTest(data)
	if len(fm) == 0 {
		t.Fatalf("frontmatter empty; output not parseable: %s", string(data))
	}
	var meta struct {
		Description  string `yaml:"description"`
		ArgumentHint string `yaml:"argument-hint"`
	}
	if err := yaml.Unmarshal(fm, &meta); err != nil {
		t.Fatalf("yaml.Unmarshal frontmatter: %v\nfrontmatter:\n%s", err, string(fm))
	}
	if meta.Description != "Greet the user." {
		t.Fatalf("description = %q want %q", meta.Description, "Greet the user.")
	}
	if meta.ArgumentHint != "<name>" {
		t.Fatalf("argument-hint = %q want %q", meta.ArgumentHint, "<name>")
	}
	if !strings.HasPrefix(string(body), "Hello $ARGUMENTS!") {
		t.Fatalf("body mis-split; got: %q", string(body))
	}
}

func TestWriteCommand_OmitsEmptyArgumentHint(t *testing.T) {
	home := t.TempDir()
	w := Writer{CommandsDir: filepath.Join(home, "commands")}
	path, err := w.WriteCommand("nohint", "No hint here.", "  ", "body\n", false)
	if err != nil {
		t.Fatalf("WriteCommand: %v", err)
	}
	data, _ := os.ReadFile(path)
	if strings.Contains(string(data), "argument-hint:") {
		t.Fatalf("expected no argument-hint key for blank hint; got:\n%s", string(data))
	}
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
