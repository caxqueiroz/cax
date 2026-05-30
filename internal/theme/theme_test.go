package theme

import (
	"os"
	"path/filepath"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestLoadUserDirAndResolve(t *testing.T) {
	reset()
	LoadBuiltins()

	dir := t.TempDir()
	custom := `name: cust
foreground: "#ffffff"
dim: "#777777"
separator: "#333333"
accent: "#ffcc00"
ok: "#00ff00"
amber: "#ffaa00"
red: "#ff0000"
user_prefix: "#ffcc00"
assistant_text: "#ffffff"
sys_text: "#888888"
code_bg: "#111111"
gauge_filled: "#00ff00"
gauge_empty: "#333333"
markdown: "dark"
`
	if err := os.WriteFile(filepath.Join(dir, "cust.yaml"), []byte(custom), 0o600); err != nil {
		t.Fatal(err)
	}
	// A broken file is logged + skipped, not fatal.
	if err := os.WriteFile(filepath.Join(dir, "broken.yaml"), []byte("not: [valid"), 0o600); err != nil {
		t.Fatal(err)
	}
	LoadUserDir(dir)

	if _, err := Get("cust"); err != nil {
		t.Fatalf("user theme not registered: %v", err)
	}

	// state.json takes precedence
	state := filepath.Join(t.TempDir(), "state.json")
	if err := writeStateTheme(state, "dracula"); err != nil {
		t.Fatal(err)
	}
	got := Resolve(state, "nord")
	if got.Name != "dracula" {
		t.Fatalf("state wins: got %q", got.Name)
	}

	// config name when state missing
	got = Resolve(filepath.Join(t.TempDir(), "nope.json"), "nord")
	if got.Name != "nord" {
		t.Fatalf("config wins: got %q", got.Name)
	}

	// fallback when both absent: returns either default-dark or default-light
	got = Resolve(filepath.Join(t.TempDir(), "nope.json"), "")
	if got.Name != "default-dark" && got.Name != "default-light" {
		t.Fatalf("fallback should be a default-* theme, got %q", got.Name)
	}
}

func TestLoadBuiltins(t *testing.T) {
	reset()
	ts := LoadBuiltins()
	want := []string{
		"default-dark", "default-light", "dracula",
		"gruvbox-dark", "mono", "nord",
		"solarized-dark", "solarized-light",
	}
	if len(ts) != len(want) {
		t.Fatalf("LoadBuiltins returned %d themes, want %d (%v)", len(ts), len(want), names(ts))
	}
	seen := map[string]bool{}
	for _, th := range ts {
		if th.Name == "" {
			t.Fatalf("empty name in %+v", th)
		}
		if th.Foreground == "" || th.Accent == "" || th.Markdown == "" {
			t.Fatalf("%s missing required field: %+v", th.Name, th)
		}
		seen[th.Name] = true
	}
	for _, n := range want {
		if !seen[n] {
			t.Fatalf("missing built-in %q", n)
		}
	}
}

func names(ts []*Theme) []string {
	out := make([]string, len(ts))
	for i, t := range ts {
		out[i] = t.Name
	}
	return out
}

func TestRegistry(t *testing.T) {
	reset()
	a := &Theme{Name: "a"}
	b := &Theme{Name: "b"}
	Register(a)
	Register(b)
	got, err := Get("a")
	if err != nil || got != a {
		t.Fatalf("Get(a) = %v, %v", got, err)
	}
	names := List()
	if len(names) != 2 || names[0] != "a" || names[1] != "b" {
		t.Fatalf("List() = %v", names)
	}
	Set(a)
	if Active() != a {
		t.Fatalf("Active() = %v want a", Active())
	}
	next := Cycle()
	if next != b || Active() != b {
		t.Fatalf("Cycle() = %v active=%v", next, Active())
	}
	if _, err := Get("missing"); err == nil {
		t.Fatalf("Get(missing) should error")
	}
}

func TestThemeYAMLParses(t *testing.T) {
	src := []byte(`name: t1
foreground: "#ffffff"
dim: "#888888"
separator: "#444444"
accent: "#5fafff"
ok: "#42d77d"
amber: "#d7af00"
red: "#ff5f5f"
user_prefix: "#5fafff"
assistant_text: "#ffffff"
sys_text: "#888888"
code_bg: "#262626"
gauge_filled: "#42d77d"
gauge_empty: "#444444"
markdown: "dark"
`)
	var th Theme
	if err := yaml.Unmarshal(src, &th); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if th.Name != "t1" {
		t.Fatalf("Name = %q want t1", th.Name)
	}
	if th.Foreground != "#ffffff" {
		t.Fatalf("Foreground = %q", th.Foreground)
	}
	if th.Markdown != "dark" {
		t.Fatalf("Markdown = %q", th.Markdown)
	}
	if th.UserPrefix != "#5fafff" {
		t.Fatalf("UserPrefix = %q", th.UserPrefix)
	}
	if th.CodeBG != "#262626" {
		t.Fatalf("CodeBG = %q", th.CodeBG)
	}
}
