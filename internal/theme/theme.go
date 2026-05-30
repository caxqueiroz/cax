// Package theme defines czcli's TUI color theme — a small struct of hex
// color strings plus a glamour style name for markdown rendering — and a
// process-wide registry of named themes loaded from embedded YAML + user
// YAML files. Themes are swapped live via Set; view.go reads Active() on
// every render.
package theme

// Theme is a named bag of colors and a glamour markdown style name.
// Color fields are hex strings ("#rrggbb") consumed via lipgloss.Color.
type Theme struct {
	Name string `yaml:"name"`

	// Base text and structural colors.
	Foreground string `yaml:"foreground"`
	Dim        string `yaml:"dim"`
	Separator  string `yaml:"separator"`

	// Semantic colors.
	Accent string `yaml:"accent"`
	OK     string `yaml:"ok"`
	Amber  string `yaml:"amber"`
	Red    string `yaml:"red"`

	// Conversation-specific.
	UserPrefix    string `yaml:"user_prefix"`
	AssistantText string `yaml:"assistant_text"`
	SysText       string `yaml:"sys_text"`
	CodeBG        string `yaml:"code_bg"`

	// Gauge.
	GaugeFilled string `yaml:"gauge_filled"`
	GaugeEmpty  string `yaml:"gauge_empty"`

	// Markdown rendering: glamour style name. One of glamour's built-ins
	// ("dark", "light", "dracula", "tokyo-night", "notty", "pink", "ascii")
	// or "auto" to derive from terminal background.
	Markdown string `yaml:"markdown"`
}
