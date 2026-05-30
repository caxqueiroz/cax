package theme

import (
	"testing"

	"gopkg.in/yaml.v3"
)

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
