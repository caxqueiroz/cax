# Plan 10: Themes + Visual Redesign + Markdown — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Ship a polished, themed `czcli` TUI with a refined chrome (thin separators, 2-space indent, no heavy background bands), 8 embedded YAML themes with live `/theme` switching, persisted active theme in `~/.czcli/state.json`, and markdown-rendered assistant replies via `glamour`.

**Architecture:** A new `internal/theme` package owns a `Theme` struct, an in-memory registry, embedded built-in YAMLs (`//go:embed builtins/*.yaml`), a user-dir loader, atomic state-file persistence, and a `Resolve` order (state.json → config.cli.theme → `default-dark` or `default-light` chosen by terminal background detection). `internal/channel/cli/view.go` is rewritten so every render reads `theme.Active()` through a `themedStyles()` helper instead of package-level lipgloss vars; assistant entries are rendered through a new `RenderMarkdown(input, width)` helper backed by `glamour.NewTermRenderer` keyed to `Theme.Markdown`. `cmd/czcli/main.go` loads themes on startup; a `/theme` slash command in `internal/channel/cli/commands.go` lists/switches themes live and writes state.json.

**Tech Stack:** Go 1.25, `github.com/charmbracelet/glamour` v1.0.0 (`WithStandardStyle`, `WithWordWrap`, `WithAutoStyle`), `github.com/charmbracelet/lipgloss` v1.1.0 (plain `lipgloss.Color` per theme — adaptive handling is done by picking `default-dark` vs `default-light` at startup via `github.com/muesli/termenv` v0.16.0 already in the dep graph), `gopkg.in/yaml.v3`, Go stdlib `embed`, `encoding/json`, `os`.

### Glamour + lipgloss findings (verified against installed module cache)

- `glamour@v1.0.0` ships these standard style names: `auto`, `dark`, `light`, `dracula`, `tokyo-night`, `notty`, `pink`, `ascii`. `WithStandardStyle(name)` returns an error for unknown names, so the markdown helper validates and falls back to `auto`.
- `WithAutoStyle()` is functionally equivalent to `WithStandardStyle("auto")`.
- `WithWordWrap(n)` is honored; we pass `viewport.Width` (clamped to ≥1) so glamour wraps assistant text — the existing `lipgloss.NewStyle().Width(w)` outer wrap is dropped for assistant entries to avoid double-wrapping.
- `lipgloss.AdaptiveColor{Light, Dark}` exists at v1.1.0, but encoding adaptive pairs cleanly in YAML doubles every color field. We sidestep that by shipping **two** default themes — `default-dark.yaml` and `default-light.yaml` — with explicit hex values. At startup `theme.Resolve` calls `termenv.HasDarkBackground()` (via the default renderer's output writer) to pick which one becomes the resolved default. User-set explicit themes (`dracula`, `nord`, …) override this.
- `//go:embed builtins/*.yaml` works for the layout used here (`internal/theme/builtins/*.yaml`) — verified by reading the embed docs and by the test in Task 4 that enumerates `fs.ReadDir`.

### Contract verification

`internal/theme` API in this plan matches `02-tui-redesign-contracts.md` verbatim:
`Theme` struct fields, `LoadBuiltins`, `LoadUserDir`, `Register`, `List`, `Get`, `Active`, `Set`, `Cycle`, `Resolve`. `RenderMarkdown(input string, width int) string` signature is preserved. No contract mismatches.

### Built-in YAMLs shipped

`internal/theme/builtins/default-dark.yaml`, `default-light.yaml`, `mono.yaml`, `dracula.yaml`, `nord.yaml`, `gruvbox-dark.yaml`, `solarized-dark.yaml`, `solarized-light.yaml`. All fields populated with explicit hex; the `markdown:` field is one of glamour's standard names mapped from the theme family.

---

## File Structure

```
internal/theme/                              NEW
  theme.go                                   Theme struct + YAML schema
  registry.go                                Register/List/Get/Active/Set/Cycle
  loader.go                                  LoadBuiltins/LoadUserDir/Resolve
  state.go                                   read/write ~/.czcli/state.json (atomic)
  builtins.go                                //go:embed builtins/*.yaml
  builtins/default-dark.yaml                 explicit hex; markdown: dark
  builtins/default-light.yaml                explicit hex; markdown: light
  builtins/mono.yaml                         explicit grayscale; markdown: notty
  builtins/dracula.yaml                      markdown: dracula
  builtins/nord.yaml                         markdown: dark
  builtins/gruvbox-dark.yaml                 markdown: dark
  builtins/solarized-dark.yaml               markdown: dark
  builtins/solarized-light.yaml              markdown: light
  theme_test.go                              loader + registry + resolve tests
  state_test.go                              atomic write + corrupt read tests

internal/channel/cli/markdown.go             NEW   glamour wrapper
internal/channel/cli/markdown_test.go        NEW

internal/channel/cli/view.go                 MODIFY rewrite to themed styles + thin separators + RenderMarkdown
internal/channel/cli/view_test.go            MODIFY assertions for new layout
internal/channel/cli/commands.go             MODIFY /theme list, /theme <name>
internal/channel/cli/commands_test.go        MODIFY /theme tests

internal/config/config.go                    MODIFY add CLIConfig { Theme string } and Config.CLI
internal/config/config_test.go               MODIFY CLI field parses

cmd/czcli/main.go                            MODIFY theme.LoadBuiltins + LoadUserDir + theme.Set(theme.Resolve(...))
```

---

### Task 1 — Theme struct + YAML schema

**Files:** `internal/theme/theme.go`, `internal/theme/theme_test.go`

- [ ] Add a failing test `TestThemeYAMLParses` that round-trips a minimal YAML doc:

  `internal/theme/theme_test.go` (COMPLETE):

  ```go
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
  ```

- [ ] Run `go test ./internal/theme/...` — expect FAIL: package doesn't exist.
- [ ] Create `internal/theme/theme.go` with the contract type (COMPLETE):

  ```go
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
  ```

- [ ] Run `go test ./internal/theme/...` — expect PASS.
- [ ] Commit:

  ```
  git add internal/theme/theme.go internal/theme/theme_test.go
  git commit -m "$(cat <<'EOF'
  feat(theme): add Theme struct + YAML schema

  Defines the small color bag plus glamour style name that the TUI
  reads on every render.

  Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
  EOF
  )"
  ```

---

### Task 2 — Registry (Register/List/Get/Active/Set/Cycle)

**Files:** `internal/theme/registry.go`, `internal/theme/theme_test.go`

- [ ] Append failing test `TestRegistry` (COMPLETE):

  ```go
  func TestRegistry(t *testing.T) {
  	reset() // package-private helper added in registry.go for tests
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
  ```

- [ ] Run `go test ./internal/theme/...` — expect FAIL.
- [ ] Create `internal/theme/registry.go` (COMPLETE):

  ```go
  package theme

  import (
  	"fmt"
  	"sort"
  	"sync"
  )

  var (
  	mu       sync.RWMutex
  	registry = map[string]*Theme{}
  	order    []string // insertion order, drives List() and Cycle()
  	active   *Theme
  )

  // Register adds or replaces a theme in the registry. Empty names are ignored.
  func Register(t *Theme) {
  	if t == nil || t.Name == "" {
  		return
  	}
  	mu.Lock()
  	defer mu.Unlock()
  	if _, exists := registry[t.Name]; !exists {
  		order = append(order, t.Name)
  	}
  	registry[t.Name] = t
  }

  // List returns theme names in registration order. Built-ins are loaded first
  // (alphabetised by LoadBuiltins for stability), user themes after.
  func List() []string {
  	mu.RLock()
  	defer mu.RUnlock()
  	out := make([]string, len(order))
  	copy(out, order)
  	return out
  }

  // Get returns a theme by name.
  func Get(name string) (*Theme, error) {
  	mu.RLock()
  	defer mu.RUnlock()
  	t, ok := registry[name]
  	if !ok {
  		return nil, fmt.Errorf("theme %q not found", name)
  	}
  	return t, nil
  }

  // Active returns the currently active theme, or nil if Set has never been
  // called. View code should fall back to a sentinel when nil.
  func Active() *Theme {
  	mu.RLock()
  	defer mu.RUnlock()
  	return active
  }

  // Set makes t the active theme. Subsequent Active() returns t. Safe for
  // concurrent reads.
  func Set(t *Theme) {
  	mu.Lock()
  	defer mu.Unlock()
  	active = t
  }

  // Cycle advances the active theme to the next name in registration order,
  // wrapping at the end. If nothing is active yet it picks the first. Returns
  // the new active theme, or nil if the registry is empty.
  func Cycle() *Theme {
  	mu.Lock()
  	defer mu.Unlock()
  	if len(order) == 0 {
  		return nil
  	}
  	if active == nil {
  		active = registry[order[0]]
  		return active
  	}
  	for i, n := range order {
  		if n == active.Name {
  			next := order[(i+1)%len(order)]
  			active = registry[next]
  			return active
  		}
  	}
  	active = registry[order[0]]
  	return active
  }

  // reset clears the registry. Test-only.
  func reset() {
  	mu.Lock()
  	defer mu.Unlock()
  	registry = map[string]*Theme{}
  	order = nil
  	active = nil
  }

  // sortNames returns names sorted alphabetically. Used by LoadBuiltins to
  // give a stable cross-platform ordering before user themes are appended.
  func sortNames(in []string) []string {
  	out := make([]string, len(in))
  	copy(out, in)
  	sort.Strings(out)
  	return out
  }
  ```

- [ ] Run `go test ./internal/theme/...` — expect PASS.
- [ ] Commit:

  ```
  git add internal/theme/registry.go internal/theme/theme_test.go
  git commit -m "$(cat <<'EOF'
  feat(theme): add in-memory registry with Set/Active/Cycle

  Mutex-guarded map + insertion-order slice so List() is stable and
  Cycle() wraps deterministically.

  Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
  EOF
  )"
  ```

---

### Task 3 — State file (atomic read/write of ~/.czcli/state.json)

**Files:** `internal/theme/state.go`, `internal/theme/state_test.go`

- [ ] Failing test `TestStateRoundTrip` (COMPLETE):

  ```go
  package theme

  import (
  	"os"
  	"path/filepath"
  	"testing"
  )

  func TestStateRoundTrip(t *testing.T) {
  	dir := t.TempDir()
  	p := filepath.Join(dir, "state.json")

  	if name := readStateTheme(p); name != "" {
  		t.Fatalf("missing file should return empty, got %q", name)
  	}

  	if err := writeStateTheme(p, "dracula"); err != nil {
  		t.Fatalf("write: %v", err)
  	}
  	if name := readStateTheme(p); name != "dracula" {
  		t.Fatalf("read = %q want dracula", name)
  	}

  	// Corrupt file -> empty + no error from reader (silent recovery).
  	if err := os.WriteFile(p, []byte("{not json"), 0o600); err != nil {
  		t.Fatal(err)
  	}
  	if name := readStateTheme(p); name != "" {
  		t.Fatalf("corrupt file should return empty, got %q", name)
  	}
  }

  func TestStateAtomic(t *testing.T) {
  	dir := t.TempDir()
  	p := filepath.Join(dir, "state.json")
  	if err := writeStateTheme(p, "nord"); err != nil {
  		t.Fatalf("write: %v", err)
  	}
  	// No leftover *.tmp from the rename.
  	entries, _ := os.ReadDir(dir)
  	for _, e := range entries {
  		if filepath.Ext(e.Name()) == ".tmp" {
  			t.Fatalf("leftover tmp file %s", e.Name())
  		}
  	}
  }
  ```

- [ ] Run — expect FAIL.
- [ ] Create `internal/theme/state.go` (COMPLETE):

  ```go
  package theme

  import (
  	"encoding/json"
  	"fmt"
  	"log/slog"
  	"os"
  	"path/filepath"
  )

  // stateFile is the on-disk shape of ~/.czcli/state.json. Only the theme name
  // is persisted today; future fields can be added without migrating users.
  type stateFile struct {
  	Theme string `json:"theme,omitempty"`
  }

  // readStateTheme returns the persisted theme name. Missing or corrupt files
  // return "" (silent recovery) so the resolver can fall through to defaults.
  func readStateTheme(path string) string {
  	data, err := os.ReadFile(path)
  	if err != nil {
  		if !os.IsNotExist(err) {
  			slog.Debug("theme: read state", "path", path, "err", err)
  		}
  		return ""
  	}
  	var s stateFile
  	if err := json.Unmarshal(data, &s); err != nil {
  		slog.Warn("theme: state.json corrupt, ignoring", "path", path, "err", err)
  		return ""
  	}
  	return s.Theme
  }

  // writeStateTheme persists the active theme name atomically (write tmp +
  // rename). The parent directory is created if missing.
  func writeStateTheme(path, name string) error {
  	if path == "" {
  		return fmt.Errorf("theme: empty state file path")
  	}
  	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
  		return fmt.Errorf("create state dir: %w", err)
  	}
  	data, err := json.Marshal(stateFile{Theme: name})
  	if err != nil {
  		return fmt.Errorf("marshal state: %w", err)
  	}
  	tmp, err := os.CreateTemp(filepath.Dir(path), "state-*.tmp")
  	if err != nil {
  		return fmt.Errorf("create tmp: %w", err)
  	}
  	tmpPath := tmp.Name()
  	if _, err := tmp.Write(data); err != nil {
  		_ = tmp.Close()
  		_ = os.Remove(tmpPath)
  		return fmt.Errorf("write tmp: %w", err)
  	}
  	if err := tmp.Close(); err != nil {
  		_ = os.Remove(tmpPath)
  		return fmt.Errorf("close tmp: %w", err)
  	}
  	if err := os.Rename(tmpPath, path); err != nil {
  		_ = os.Remove(tmpPath)
  		return fmt.Errorf("rename: %w", err)
  	}
  	return nil
  }

  // StateFile is the public helper view.go / commands.go use to compute the
  // path. It joins ~/.czcli/state.json or "" if the home dir can't be found.
  func StateFile() string {
  	home, err := os.UserHomeDir()
  	if err != nil {
  		return ""
  	}
  	return filepath.Join(home, ".czcli", "state.json")
  }

  // WriteActive persists the currently active theme to path. Convenience for
  // /theme handlers + Ctrl+T.
  func WriteActive(path string) error {
  	a := Active()
  	if a == nil {
  		return nil
  	}
  	return writeStateTheme(path, a.Name)
  }
  ```

- [ ] Run — expect PASS.
- [ ] Commit:

  ```
  git add internal/theme/state.go internal/theme/state_test.go
  git commit -m "$(cat <<'EOF'
  feat(theme): persist active theme to ~/.czcli/state.json atomically

  Temp file + rename so a half-written state.json never poisons future
  startups; corrupt files are logged + ignored.

  Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
  EOF
  )"
  ```

---

### Task 4 — Built-in YAML files + embedded loader

**Files:** `internal/theme/builtins.go`, `internal/theme/builtins/*.yaml`, `internal/theme/theme_test.go`

- [ ] Append failing test `TestLoadBuiltins` (COMPLETE):

  ```go
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
  ```

- [ ] Run — expect FAIL (no LoadBuiltins yet).
- [ ] Create the eight YAML files under `internal/theme/builtins/`. Sample below; all eight must populate every field. Hex values are chosen for readability on common terminals.

  `internal/theme/builtins/default-dark.yaml` (COMPLETE):

  ```yaml
  name: default-dark
  foreground: "#e6e6e6"
  dim: "#7a7a7a"
  separator: "#3a3a3a"
  accent: "#5fafff"
  ok: "#5fd787"
  amber: "#d7af00"
  red: "#ff5f5f"
  user_prefix: "#5fafff"
  assistant_text: "#e6e6e6"
  sys_text: "#9a9a9a"
  code_bg: "#262626"
  gauge_filled: "#5fd787"
  gauge_empty: "#3a3a3a"
  markdown: "dark"
  ```

  `internal/theme/builtins/default-light.yaml` (COMPLETE):

  ```yaml
  name: default-light
  foreground: "#1c1c1c"
  dim: "#6c6c6c"
  separator: "#bcbcbc"
  accent: "#005fd7"
  ok: "#008700"
  amber: "#af8700"
  red: "#d70000"
  user_prefix: "#005fd7"
  assistant_text: "#1c1c1c"
  sys_text: "#6c6c6c"
  code_bg: "#eeeeee"
  gauge_filled: "#008700"
  gauge_empty: "#bcbcbc"
  markdown: "light"
  ```

  `internal/theme/builtins/mono.yaml` (COMPLETE):

  ```yaml
  name: mono
  foreground: "#dddddd"
  dim: "#777777"
  separator: "#444444"
  accent: "#ffffff"
  ok: "#ffffff"
  amber: "#bbbbbb"
  red: "#ffffff"
  user_prefix: "#ffffff"
  assistant_text: "#dddddd"
  sys_text: "#999999"
  code_bg: "#1c1c1c"
  gauge_filled: "#dddddd"
  gauge_empty: "#444444"
  markdown: "notty"
  ```

  `internal/theme/builtins/dracula.yaml` (COMPLETE):

  ```yaml
  name: dracula
  foreground: "#f8f8f2"
  dim: "#6272a4"
  separator: "#44475a"
  accent: "#bd93f9"
  ok: "#50fa7b"
  amber: "#f1fa8c"
  red: "#ff5555"
  user_prefix: "#bd93f9"
  assistant_text: "#f8f8f2"
  sys_text: "#6272a4"
  code_bg: "#282a36"
  gauge_filled: "#50fa7b"
  gauge_empty: "#44475a"
  markdown: "dracula"
  ```

  `internal/theme/builtins/nord.yaml` (COMPLETE):

  ```yaml
  name: nord
  foreground: "#d8dee9"
  dim: "#4c566a"
  separator: "#3b4252"
  accent: "#88c0d0"
  ok: "#a3be8c"
  amber: "#ebcb8b"
  red: "#bf616a"
  user_prefix: "#88c0d0"
  assistant_text: "#d8dee9"
  sys_text: "#616e88"
  code_bg: "#2e3440"
  gauge_filled: "#a3be8c"
  gauge_empty: "#3b4252"
  markdown: "dark"
  ```

  `internal/theme/builtins/gruvbox-dark.yaml` (COMPLETE):

  ```yaml
  name: gruvbox-dark
  foreground: "#ebdbb2"
  dim: "#928374"
  separator: "#504945"
  accent: "#83a598"
  ok: "#b8bb26"
  amber: "#fabd2f"
  red: "#fb4934"
  user_prefix: "#83a598"
  assistant_text: "#ebdbb2"
  sys_text: "#928374"
  code_bg: "#282828"
  gauge_filled: "#b8bb26"
  gauge_empty: "#504945"
  markdown: "dark"
  ```

  `internal/theme/builtins/solarized-dark.yaml` (COMPLETE):

  ```yaml
  name: solarized-dark
  foreground: "#93a1a1"
  dim: "#586e75"
  separator: "#073642"
  accent: "#268bd2"
  ok: "#859900"
  amber: "#b58900"
  red: "#dc322f"
  user_prefix: "#268bd2"
  assistant_text: "#93a1a1"
  sys_text: "#586e75"
  code_bg: "#002b36"
  gauge_filled: "#859900"
  gauge_empty: "#073642"
  markdown: "dark"
  ```

  `internal/theme/builtins/solarized-light.yaml` (COMPLETE):

  ```yaml
  name: solarized-light
  foreground: "#657b83"
  dim: "#93a1a1"
  separator: "#eee8d5"
  accent: "#268bd2"
  ok: "#859900"
  amber: "#b58900"
  red: "#dc322f"
  user_prefix: "#268bd2"
  assistant_text: "#657b83"
  sys_text: "#93a1a1"
  code_bg: "#fdf6e3"
  gauge_filled: "#859900"
  gauge_empty: "#eee8d5"
  markdown: "light"
  ```

- [ ] Create `internal/theme/builtins.go` (COMPLETE):

  ```go
  package theme

  import (
  	"embed"
  	"fmt"
  	"io/fs"
  	"log/slog"
  	"strings"

  	"gopkg.in/yaml.v3"
  )

  //go:embed builtins/*.yaml
  var builtinFS embed.FS

  // LoadBuiltins parses every embedded YAML in builtins/ and registers it. The
  // returned slice is ordered alphabetically by file name so List()/Cycle()
  // produce a stable order across builds.
  func LoadBuiltins() []*Theme {
  	entries, err := fs.ReadDir(builtinFS, "builtins")
  	if err != nil {
  		slog.Error("theme: read embedded builtins", "err", err)
  		return nil
  	}
  	var fileNames []string
  	for _, e := range entries {
  		if e.IsDir() || !strings.HasSuffix(e.Name(), ".yaml") {
  			continue
  		}
  		fileNames = append(fileNames, e.Name())
  	}
  	fileNames = sortNames(fileNames)
  	out := make([]*Theme, 0, len(fileNames))
  	for _, fn := range fileNames {
  		data, err := fs.ReadFile(builtinFS, "builtins/"+fn)
  		if err != nil {
  			slog.Warn("theme: read builtin", "file", fn, "err", err)
  			continue
  		}
  		var t Theme
  		if err := yaml.Unmarshal(data, &t); err != nil {
  			slog.Warn("theme: parse builtin", "file", fn, "err", err)
  			continue
  		}
  		if err := validateLoaded(&t, fn); err != nil {
  			slog.Warn("theme: invalid builtin", "file", fn, "err", err)
  			continue
  		}
  		Register(&t)
  		out = append(out, &t)
  	}
  	return out
  }

  // validateLoaded enforces that every required field is set; ships a clear
  // error message that includes the file name so a broken built-in is easy
  // to find.
  func validateLoaded(t *Theme, source string) error {
  	missing := []string{}
  	check := func(name, v string) {
  		if v == "" {
  			missing = append(missing, name)
  		}
  	}
  	check("name", t.Name)
  	check("foreground", t.Foreground)
  	check("dim", t.Dim)
  	check("separator", t.Separator)
  	check("accent", t.Accent)
  	check("ok", t.OK)
  	check("amber", t.Amber)
  	check("red", t.Red)
  	check("user_prefix", t.UserPrefix)
  	check("assistant_text", t.AssistantText)
  	check("sys_text", t.SysText)
  	check("code_bg", t.CodeBG)
  	check("gauge_filled", t.GaugeFilled)
  	check("gauge_empty", t.GaugeEmpty)
  	check("markdown", t.Markdown)
  	if len(missing) > 0 {
  		return fmt.Errorf("%s: missing fields %v", source, missing)
  	}
  	return nil
  }
  ```

- [ ] Run `go test ./internal/theme/...` — expect PASS.
- [ ] Commit:

  ```
  git add internal/theme/builtins.go internal/theme/builtins internal/theme/theme_test.go
  git commit -m "$(cat <<'EOF'
  feat(theme): embed eight built-in themes via go:embed

  default-dark, default-light, mono, dracula, nord, gruvbox-dark,
  solarized-dark, solarized-light — each fully populated with explicit
  hex; built-in YAMLs alphabetised for stable List()/Cycle() order.

  Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
  EOF
  )"
  ```

---

### Task 5 — User-dir loader + Resolve

**Files:** `internal/theme/loader.go`, `internal/theme/theme_test.go`

- [ ] Append failing test `TestLoadUserDirAndResolve` (COMPLETE):

  ```go
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
  ```

- [ ] Add the missing imports (`os`, `path/filepath`) to `theme_test.go` if they aren't already pulled in by earlier tests.
- [ ] Run — expect FAIL.
- [ ] Create `internal/theme/loader.go` (COMPLETE):

  ```go
  package theme

  import (
  	"log/slog"
  	"os"
  	"path/filepath"
  	"strings"

  	"github.com/muesli/termenv"
  	"gopkg.in/yaml.v3"
  )

  // LoadUserDir scans a directory for *.yaml theme files and registers each
  // valid one. Per-file errors are logged + skipped; a broken theme never
  // blocks startup. A missing directory is silently ignored.
  func LoadUserDir(path string) []*Theme {
  	if path == "" {
  		return nil
  	}
  	entries, err := os.ReadDir(path)
  	if err != nil {
  		if !os.IsNotExist(err) {
  			slog.Warn("theme: read user dir", "path", path, "err", err)
  		}
  		return nil
  	}
  	out := make([]*Theme, 0, len(entries))
  	for _, e := range entries {
  		if e.IsDir() || !strings.HasSuffix(e.Name(), ".yaml") {
  			continue
  		}
  		full := filepath.Join(path, e.Name())
  		data, err := os.ReadFile(full)
  		if err != nil {
  			slog.Warn("theme: read user theme", "file", full, "err", err)
  			continue
  		}
  		var t Theme
  		if err := yaml.Unmarshal(data, &t); err != nil {
  			slog.Warn("theme: parse user theme", "file", full, "err", err)
  			continue
  		}
  		if err := validateLoaded(&t, full); err != nil {
  			slog.Warn("theme: invalid user theme", "file", full, "err", err)
  			continue
  		}
  		Register(&t)
  		out = append(out, &t)
  	}
  	return out
  }

  // Resolve picks the startup theme. Order:
  //   1. state.json "theme" field (if file exists, parses, names a registered theme)
  //   2. configThemeName (from config.cli.theme), if it names a registered theme
  //   3. terminal-background-adaptive default: default-light or default-dark
  // The returned theme is also Set() as active so callers don't need to.
  func Resolve(stateFile, configThemeName string) *Theme {
  	if name := readStateTheme(stateFile); name != "" {
  		if t, err := Get(name); err == nil {
  			Set(t)
  			return t
  		} else {
  			slog.Warn("theme: state.json names unknown theme", "name", name)
  		}
  	}
  	if configThemeName != "" {
  		if t, err := Get(configThemeName); err == nil {
  			Set(t)
  			return t
  		} else {
  			slog.Warn("theme: config.cli.theme names unknown theme", "name", configThemeName)
  		}
  	}
  	preferred := "default-dark"
  	if !termenv.DefaultOutput().HasDarkBackground() {
  		preferred = "default-light"
  	}
  	if t, err := Get(preferred); err == nil {
  		Set(t)
  		return t
  	}
  	// Final fallback: first registered theme, whatever it is.
  	for _, n := range List() {
  		if t, err := Get(n); err == nil {
  			Set(t)
  			return t
  		}
  	}
  	return nil
  }
  ```

- [ ] Run — expect PASS.
- [ ] Commit:

  ```
  git add internal/theme/loader.go internal/theme/theme_test.go
  git commit -m "$(cat <<'EOF'
  feat(theme): load user YAMLs + resolve startup theme

  LoadUserDir scans ~/.czcli/themes for *.yaml; Resolve picks
  state.json > config.cli.theme > default-{dark,light} chosen by
  terminal background.

  Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
  EOF
  )"
  ```

---

### Task 6 — Markdown renderer

**Files:** `internal/channel/cli/markdown.go`, `internal/channel/cli/markdown_test.go`

- [ ] Failing test (COMPLETE):

  ```go
  package cli

  import (
  	"strings"
  	"testing"

  	"github.com/caxqueiroz/czcli/internal/theme"
  )

  func TestRenderMarkdownNonEmpty(t *testing.T) {
  	theme.LoadBuiltins()
  	for _, name := range []string{"default-dark", "dracula", "nord", "mono"} {
  		th, err := theme.Get(name)
  		if err != nil {
  			t.Fatalf("Get(%s): %v", name, err)
  		}
  		theme.Set(th)
  		out := RenderMarkdown("# Hello\n\nworld with `code`.", 60)
  		if strings.TrimSpace(out) == "" {
  			t.Fatalf("%s: empty render", name)
  		}
  		if !strings.Contains(out, "Hello") || !strings.Contains(out, "world") {
  			t.Fatalf("%s: missing content, got %q", name, out)
  		}
  	}
  }

  func TestRenderMarkdownFallbackOnError(t *testing.T) {
  	theme.LoadBuiltins()
  	bad := &theme.Theme{Name: "bad", Foreground: "#fff", Dim: "#888", Separator: "#444",
  		Accent: "#fff", OK: "#fff", Amber: "#fff", Red: "#fff",
  		UserPrefix: "#fff", AssistantText: "#fff", SysText: "#888", CodeBG: "#222",
  		GaugeFilled: "#fff", GaugeEmpty: "#444", Markdown: "no-such-style"}
  	theme.Register(bad)
  	theme.Set(bad)
  	in := "raw input"
  	got := RenderMarkdown(in, 40)
  	if got == "" {
  		t.Fatalf("fallback should return non-empty")
  	}
  	if !strings.Contains(got, "raw input") {
  		t.Fatalf("fallback should preserve input, got %q", got)
  	}
  }
  ```

- [ ] Run — expect FAIL.
- [ ] Add the dep: `go get github.com/charmbracelet/glamour@v1.0.0` (run as part of impl), then create `internal/channel/cli/markdown.go` (COMPLETE):

  ```go
  package cli

  import (
  	"log/slog"

  	"github.com/charmbracelet/glamour"

  	"github.com/caxqueiroz/czcli/internal/theme"
  )

  // knownGlamourStyles enumerates the built-in glamour style names accepted
  // at v1.0.0. Anything outside this set falls back to "auto".
  var knownGlamourStyles = map[string]struct{}{
  	"auto": {}, "dark": {}, "light": {}, "dracula": {},
  	"tokyo-night": {}, "notty": {}, "pink": {}, "ascii": {},
  }

  // RenderMarkdown renders input as ANSI-styled markdown using glamour, keyed
  // to the active theme's Markdown field. Width is clamped to a sensible
  // minimum so glamour doesn't panic on very narrow viewports. On any error
  // (unknown style, glamour build/render failure) the original input is
  // returned verbatim so the TUI never breaks on weird markdown.
  func RenderMarkdown(input string, width int) string {
  	if input == "" {
  		return ""
  	}
  	if width < 20 {
  		width = 20
  	}
  	style := "auto"
  	if a := theme.Active(); a != nil && a.Markdown != "" {
  		if _, ok := knownGlamourStyles[a.Markdown]; ok {
  			style = a.Markdown
  		} else {
  			slog.Warn("markdown: unknown glamour style, falling back to auto",
  				"requested", a.Markdown)
  		}
  	}
  	r, err := glamour.NewTermRenderer(
  		glamour.WithStandardStyle(style),
  		glamour.WithWordWrap(width),
  	)
  	if err != nil {
  		slog.Warn("markdown: build renderer", "style", style, "err", err)
  		return input
  	}
  	out, err := r.Render(input)
  	if err != nil {
  		slog.Warn("markdown: render", "err", err)
  		return input
  	}
  	return out
  }
  ```

- [ ] Run — expect PASS.
- [ ] Commit:

  ```
  git add go.mod go.sum internal/channel/cli/markdown.go internal/channel/cli/markdown_test.go
  git commit -m "$(cat <<'EOF'
  feat(cli): render assistant markdown via glamour keyed to active theme

  Style name comes from theme.Active().Markdown with a known-styles
  allowlist; any failure (unknown style, render error) returns the raw
  input so the TUI never blanks an answer.

  Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
  EOF
  )"
  ```

---

### Task 7 — Config: CLI.Theme field

**Files:** `internal/config/config.go`, `internal/config/config_test.go`

- [ ] Failing test (append to existing config_test.go):

  ```go
  func TestConfigCLIThemeField(t *testing.T) {
  	dir := t.TempDir()
  	p := filepath.Join(dir, "config.yaml")
  	yaml := `persona: x
  providers:
    - name: openai
      model: gpt-4o-mini
      api_key_env: OPENAI_API_KEY
  embeddings:
    provider: openai
    model: text-embedding-3-small
    dim: 1536
    api_key_env: OPENAI_API_KEY
  memory:
    db_path: /tmp/czcli.db
  cli:
    theme: dracula
  `
  	if err := os.WriteFile(p, []byte(yaml), 0o600); err != nil {
  		t.Fatal(err)
  	}
  	cfg, err := Load(p)
  	if err != nil {
  		t.Fatalf("Load: %v", err)
  	}
  	if cfg.CLI.Theme != "dracula" {
  		t.Fatalf("CLI.Theme = %q", cfg.CLI.Theme)
  	}
  }

  func TestConfigCLIThemeDefaults(t *testing.T) {
  	dir := t.TempDir()
  	p := filepath.Join(dir, "config.yaml")
  	yaml := `persona: x
  providers:
    - name: openai
      model: gpt-4o-mini
      api_key_env: OPENAI_API_KEY
  embeddings:
    provider: openai
    model: text-embedding-3-small
    dim: 1536
    api_key_env: OPENAI_API_KEY
  memory:
    db_path: /tmp/czcli.db
  `
  	if err := os.WriteFile(p, []byte(yaml), 0o600); err != nil {
  		t.Fatal(err)
  	}
  	cfg, err := Load(p)
  	if err != nil {
  		t.Fatalf("Load: %v", err)
  	}
  	if cfg.CLI.Theme != "" {
  		t.Fatalf("CLI.Theme default = %q want empty", cfg.CLI.Theme)
  	}
  }
  ```

- [ ] Run — expect FAIL.
- [ ] Edit `internal/config/config.go`:
  - Add to `Config` struct: `CLI CLIConfig \`yaml:"cli"\``.
  - Add new type at the end of the file (after `LSPServerConfig`):

  ```go
  // CLIConfig configures the TUI channel. Theme is the initial theme name;
  // resolution falls back to ~/.czcli/state.json, then a terminal-adapted
  // default-{dark,light}.
  type CLIConfig struct {
  	Theme string `yaml:"theme"`
  }
  ```

- [ ] Run — expect PASS.
- [ ] Commit:

  ```
  git add internal/config/config.go internal/config/config_test.go
  git commit -m "$(cat <<'EOF'
  feat(config): add CLI.Theme initial-theme field

  Optional; empty means defer to state.json + terminal background.
  Path/dir renames remain on plan 11's roadmap.

  Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
  EOF
  )"
  ```

---

### Task 8 — View rewrite: themedStyles + refined layout + markdown rendering for assistant

**Files:** `internal/channel/cli/view.go`, `internal/channel/cli/view_test.go`

- [ ] Failing test (replace/extend existing view_test.go assertions). New test `TestRenderConversationRendersAssistantAsMarkdown` (COMPLETE):

  ```go
  package cli

  import (
  	"strings"
  	"testing"

  	"github.com/caxqueiroz/czcli/internal/theme"
  )

  func TestRenderConversationRendersAssistantAsMarkdown(t *testing.T) {
  	theme.LoadBuiltins()
  	th, _ := theme.Get("default-dark")
  	theme.Set(th)

  	m := newModel(80, 24)
  	m.viewport.Width = 60
  	m.history = []historyEntry{
  		{who: "you", text: "hi"},
  		{who: "bot", text: "# Header\n\nbody"},
  	}
  	out := m.renderConversation()
  	if !strings.Contains(out, "❯") {
  		t.Fatalf("user prefix missing in output: %q", out)
  	}
  	if !strings.Contains(out, "Header") {
  		t.Fatalf("markdown header not rendered: %q", out)
  	}
  }

  func TestViewLayoutThinSeparatorsAndIndent(t *testing.T) {
  	theme.LoadBuiltins()
  	th, _ := theme.Get("default-dark")
  	theme.Set(th)

  	m := newModel(60, 12)
  	m.hasStatus = true
  	out := m.View()
  	// Must contain at least three horizontal separators (top/middle/bottom)
  	if strings.Count(out, "─") < 3*10 { // generous lower bound across width
  		t.Fatalf("expected thin separator lines, got %q", out)
  	}
  	// Heavy bottom-bar background ANSI should be gone (no Background SGR 48;5;236).
  	if strings.Contains(out, "48;5;236") {
  		t.Fatalf("unexpected legacy bottom background fill")
  	}
  }
  ```

- [ ] Run — expect FAIL (because markdown is not yet wired into renderConversation, and the old view emits bg fills).
- [ ] Rewrite `internal/channel/cli/view.go` (COMPLETE replacement):

  ```go
  package cli

  import (
  	"fmt"
  	"strings"

  	"github.com/charmbracelet/lipgloss"

  	"github.com/caxqueiroz/czcli/internal/theme"
  )

  const (
  	gaugeAmberPct = 0.75
  	gaugeRedPct   = 0.90
  	gaugeCells    = 8
  	leftIndent    = "  " // global 2-space indent
  )

  // themedStyles is the per-render bag of lipgloss styles built from the active
  // theme. Recomputing per frame is cheap and lets /theme switch take effect
  // on the very next render without touching package vars.
  type themedStyles struct {
  	fg, dim, sep, accent             lipgloss.Style
  	ok, amber, red                   lipgloss.Style
  	user, sys                        lipgloss.Style
  	gaugeFilled, gaugeEmpty          lipgloss.Style
  	statusLabel, statusValue, marker lipgloss.Style
  }

  // styles returns the active theme's lipgloss bag. Falls back to a tiny
  // safe-defaults bag when no theme has been registered yet (early render
  // before LoadBuiltins, e.g. inside a test that forgot to set one).
  func styles() themedStyles {
  	t := theme.Active()
  	if t == nil {
  		t = &theme.Theme{
  			Foreground: "#e6e6e6", Dim: "#7a7a7a", Separator: "#3a3a3a",
  			Accent: "#5fafff", OK: "#5fd787", Amber: "#d7af00", Red: "#ff5f5f",
  			UserPrefix: "#5fafff", AssistantText: "#e6e6e6", SysText: "#9a9a9a",
  			CodeBG: "#262626", GaugeFilled: "#5fd787", GaugeEmpty: "#3a3a3a",
  			Markdown: "auto",
  		}
  	}
  	c := lipgloss.Color
  	return themedStyles{
  		fg:          lipgloss.NewStyle().Foreground(c(t.Foreground)),
  		dim:         lipgloss.NewStyle().Foreground(c(t.Dim)),
  		sep:         lipgloss.NewStyle().Foreground(c(t.Separator)),
  		accent:      lipgloss.NewStyle().Foreground(c(t.Accent)),
  		ok:          lipgloss.NewStyle().Foreground(c(t.OK)),
  		amber:       lipgloss.NewStyle().Foreground(c(t.Amber)),
  		red:         lipgloss.NewStyle().Foreground(c(t.Red)),
  		user:        lipgloss.NewStyle().Foreground(c(t.UserPrefix)).Bold(true),
  		sys:         lipgloss.NewStyle().Foreground(c(t.SysText)).Italic(true),
  		gaugeFilled: lipgloss.NewStyle().Foreground(c(t.GaugeFilled)),
  		gaugeEmpty:  lipgloss.NewStyle().Foreground(c(t.GaugeEmpty)),
  		statusLabel: lipgloss.NewStyle().Foreground(c(t.Dim)),
  		statusValue: lipgloss.NewStyle().Foreground(c(t.Foreground)).Bold(true),
  		marker:      lipgloss.NewStyle().Foreground(c(t.Accent)).Bold(true),
  	}
  }

  // renderTopBar: "  opus ✓                  hist 6.1k/8k ▓▓▓░ 76% ⚠"
  func (m model) renderTopBar() string {
  	s := styles()
  	if !m.hasStatus {
  		return leftIndent + s.dim.Render("connecting…")
  	}
  	st := m.status

  	var modelPart string
  	if st.OnFallback {
  		modelPart = s.amber.Render(fmt.Sprintf("%s ⚠ fallback #%d", st.Model, st.FallbackIndex))
  	} else {
  		modelPart = s.ok.Render(st.Model + " ✓")
  	}
  	gauge := m.renderGauge(s, st.ContextTokens, st.ContextBudget)
  	return leftIndent + modelPart + "   " + gauge
  }

  // renderGauge renders the in-memory history budget gauge (NOT the model's
  // context window). Style switches between OK/amber/red at thresholds.
  func (m model) renderGauge(s themedStyles, tokens, budget int) string {
  	pct := 0.0
  	if budget > 0 {
  		pct = float64(tokens) / float64(budget)
  	}
  	if pct > 1 {
  		pct = 1
  	}
  	filled := int(pct * gaugeCells)
  	if filled > gaugeCells {
  		filled = gaugeCells
  	}
  	bar := s.gaugeFilled.Render(strings.Repeat("▓", filled)) +
  		s.gaugeEmpty.Render(strings.Repeat("░", gaugeCells-filled))

  	chip := s.ok
  	warn := ""
  	switch {
  	case pct >= gaugeRedPct:
  		chip = s.red
  		warn = " ⚠"
  	case pct >= gaugeAmberPct:
  		chip = s.amber
  		warn = " ⚠"
  	}
  	return fmt.Sprintf("hist %s/%s %s %s%s",
  		humanizeTokensTenths(tokens),
  		humanizeTokensTenths(budget),
  		bar,
  		chip.Render(fmt.Sprintf("%d%%", int(pct*100))),
  		chip.Render(warn),
  	)
  }

  // renderBottomBar lays out the status row with accent window markers and
  // bold values. No background band — the row inherits the terminal bg so
  // both light and dark terminals look intentional.
  func (m model) renderBottomBar() string {
  	s := styles()
  	if !m.hasStatus {
  		return leftIndent + s.dim.Render("")
  	}
  	st := m.status
  	day := st.Usage.Day.InputTokens + st.Usage.Day.OutputTokens
  	week := st.Usage.Week.InputTokens + st.Usage.Week.OutputTokens
  	month := st.Usage.Month.InputTokens + st.Usage.Month.OutputTokens

  	div := s.dim.Render("  │  ")
  	kv := func(marker, val string) string {
  		return s.marker.Render(marker) + " " + s.statusValue.Render(val)
  	}
  	tagged := func(label, val string) string {
  		return s.statusLabel.Render(label) + " " + s.statusValue.Render(val)
  	}
  	emo := func(icon string, n int) string {
  		return s.statusLabel.Render(icon) + " " + s.statusValue.Render(fmt.Sprintf("%d", n))
  	}

  	tok := strings.Join([]string{
  		kv("1d", humanizeTokens(day)),
  		kv("1w", humanizeTokens(week)),
  		kv("1m", humanizeTokens(month)),
  	}, "  ")
  	mem := tagged("mem", humanizeBytes(st.MemSizeBytes))
  	tools := emo("🔧", len(st.ToolNames)) + "  " + emo("🤖", len(st.SubagentNames))

  	extras := ""
  	add := func(icon string, n int) {
  		if n > 0 {
  			extras += div + emo(icon, n)
  		}
  	}
  	add("📜", st.SkillCount)
  	add("🔌", st.MCPServerCount)
  	add("🧩", st.PluginCount)
  	add("🧠", st.LSPServerCount)
  	add("⚓", st.HookCount)

  	return leftIndent + tok + div + mem + div + tools + extras
  }

  // renderConversation builds the body for the viewport. Assistant entries
  // are rendered as markdown via glamour; user/sys entries stay raw with
  // theme-driven prefixes. Spinner/working… is unchanged.
  func (m model) renderConversation() string {
  	s := styles()
  	w := m.viewport.Width
  	if w <= 0 {
  		w = m.width
  	}
  	if w < 4 {
  		w = 4
  	}
  	innerWidth := w - len(leftIndent)
  	if innerWidth < 1 {
  		innerWidth = 1
  	}
  	wrap := lipgloss.NewStyle().Width(innerWidth)
  	wrapErr := s.red.Width(innerWidth)
  	indent := func(text string) string {
  		var b strings.Builder
  		for i, line := range strings.Split(text, "\n") {
  			if i > 0 {
  				b.WriteByte('\n')
  			}
  			b.WriteString(leftIndent)
  			b.WriteString(line)
  		}
  		return b.String()
  	}

  	var b strings.Builder
  	for _, h := range m.history {
  		switch h.who {
  		case "you":
  			b.WriteString(indent(wrap.Render(s.user.Render("❯ ") + h.text)))
  		case "bot":
  			b.WriteString(indent(strings.TrimRight(RenderMarkdown(h.text, innerWidth), "\n")))
  		default:
  			b.WriteString(indent(wrap.Render(s.sys.Render(h.text))))
  		}
  		b.WriteByte('\n')
  		if h.who == "you" || h.who == "bot" {
  			b.WriteByte('\n')
  		}
  	}
  	if m.streaming {
  		if m.stream == "" {
  			b.WriteString(leftIndent + s.dim.Render(m.spinner.View()+" working…"))
  		} else {
  			b.WriteString(indent(wrap.Render(m.stream)))
  		}
  		b.WriteByte('\n')
  	}
  	if m.lastErr != "" {
  		b.WriteString(indent(wrapErr.Render("✗ " + m.lastErr)))
  		b.WriteByte('\n')
  	}
  	return strings.TrimRight(b.String(), "\n")
  }

  // refreshViewport recomputes content and pins to the bottom.
  func (m *model) refreshViewport() {
  	m.viewport.SetContent(m.renderConversation())
  	m.viewport.GotoBottom()
  }

  // View composes top bar / sep / viewport / sep / bottom bar / sep / input.
  func (m model) View() string {
  	s := styles()
  	sep := s.sep.Render(strings.Repeat("─", m.width))
  	inputBlock := lipgloss.JoinVertical(
  		lipgloss.Left,
  		"",
  		leftIndent+m.input.View(),
  	)
  	return lipgloss.JoinVertical(
  		lipgloss.Left,
  		m.renderTopBar(),
  		sep,
  		m.viewport.View(),
  		sep,
  		m.renderBottomBar(),
  		sep,
  		inputBlock,
  	)
  }

  // humanizeTokensTenths renders e.g. 6100 → "6.1k" for the gauge numerator.
  // Sub-1000 values fall back to humanizeTokens.
  func humanizeTokensTenths(n int) string {
  	switch {
  	case n < 1000:
  		return fmt.Sprintf("%d", n)
  	case n < 1_000_000:
  		return strings.TrimSuffix(fmt.Sprintf("%.1f", float64(n)/1000), ".0") + "k"
  	default:
  		return strings.TrimSuffix(fmt.Sprintf("%.1f", float64(n)/1_000_000), ".0") + "M"
  	}
  }
  ```

- [ ] Update `internal/channel/cli/model.go`: the spinner style still references `dimStyle` (package var that no longer exists). Replace `sp.Style = dimStyle` with `sp.Style = lipgloss.NewStyle().Foreground(lipgloss.Color("244"))` (the spinner color is a static "always dim" — themed bag is per-render, the spinner Tick takes a Style snapshot). Import `lipgloss` if not already imported.
- [ ] Audit existing tests in `view_test.go`, `model_test.go`, and `commands_test.go` for substring assertions that depended on package-level styles. Update or relax to substring matches that survive the new layout (e.g. assert presence of `"❯"`, `"hist "`, `"1d "`, `"🔧"`, the model name, etc.).
- [ ] Run `go test ./internal/channel/cli/...` — expect PASS.
- [ ] Commit:

  ```
  git add internal/channel/cli/view.go internal/channel/cli/view_test.go internal/channel/cli/model.go
  git commit -m "$(cat <<'EOF'
  refactor(cli): theme-driven view + thin separators + markdown bodies

  Drops package-level lipgloss vars in favour of per-render styles()
  reading theme.Active(). Adds global 2-space indent, removes the
  heavy bottom-bar background, renders assistant entries through
  glamour via RenderMarkdown.

  Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
  EOF
  )"
  ```

---

### Task 9 — /theme list and /theme \<name\> slash commands

**Files:** `internal/channel/cli/commands.go`, `internal/channel/cli/commands_test.go`

- [ ] Failing test (append to commands_test.go) (COMPLETE):

  ```go
  func TestCmdThemeListAndSet(t *testing.T) {
  	theme.LoadBuiltins()
  	dark, _ := theme.Get("default-dark")
  	theme.Set(dark)

  	m := newModel(80, 24)
  	out, quit := m.handleCommand("/theme list")
  	if quit {
  		t.Fatalf("list should not quit")
  	}
  	if !strings.Contains(out, "default-dark") || !strings.Contains(out, "dracula") {
  		t.Fatalf("list missing themes: %q", out)
  	}
  	if !strings.Contains(out, "* default-dark") {
  		t.Fatalf("active theme marker missing: %q", out)
  	}

  	out, _ = m.handleCommand("/theme dracula")
  	if !strings.Contains(out, "dracula") {
  		t.Fatalf("set message missing: %q", out)
  	}
  	if theme.Active().Name != "dracula" {
  		t.Fatalf("active not switched, got %s", theme.Active().Name)
  	}

  	out, _ = m.handleCommand("/theme no-such-theme")
  	if !strings.Contains(out, "not found") {
  		t.Fatalf("expected not-found message, got %q", out)
  	}
  }
  ```

- [ ] Add the import `"github.com/caxqueiroz/czcli/internal/theme"` to commands_test.go if not present.
- [ ] Run — expect FAIL.
- [ ] Edit `internal/channel/cli/commands.go`:
  - Add the `theme` import: `"github.com/caxqueiroz/czcli/internal/theme"`.
  - Extend `handleCommand`'s switch with `case "theme": return m.cmdTheme(args), false`.
  - Update the unknown-command help string to include `/theme`.
  - Append the implementation at the end of the file:

  ```go
  // cmdTheme handles "/theme list" and "/theme <name>". Setting persists the
  // choice to ~/.czcli/state.json and the next refreshViewport call applies
  // the new look.
  func (m *model) cmdTheme(args string) string {
  	args = strings.TrimSpace(args)
  	if args == "" || args == "list" || args == "ls" {
  		names := theme.List()
  		if len(names) == 0 {
  			return "no themes registered"
  		}
  		active := ""
  		if a := theme.Active(); a != nil {
  			active = a.Name
  		}
  		var b strings.Builder
  		fmt.Fprintf(&b, "themes (%d):", len(names))
  		for _, n := range names {
  			marker := "  "
  			if n == active {
  				marker = "* "
  			}
  			fmt.Fprintf(&b, "\n  %s%s", marker, n)
  		}
  		return b.String()
  	}
  	t, err := theme.Get(args)
  	if err != nil {
  		return fmt.Sprintf("theme %q not found (try /theme list)", args)
  	}
  	theme.Set(t)
  	if path := theme.StateFile(); path != "" {
  		if err := theme.WriteActive(path); err != nil {
  			slog.Warn("theme: persist state", "err", err)
  		}
  	}
  	m.refreshViewport()
  	return fmt.Sprintf("theme set to %q", t.Name)
  }
  ```

- [ ] Change the receiver of `handleCommand` and `cmdTheme` to pointer (`*model`) — currently `handleCommand` is a value receiver. The minimum-impact path: keep `handleCommand` value-receiver, but in its `case "theme":` branch use `(&m).cmdTheme(args)` and after the call also call `m.refreshViewport()` via the pointer. (Refreshing on value-copy doesn't propagate; pointer receiver is required so the viewport content is recomputed against the new theme.)

  Simpler concrete plan: switch `handleCommand` to `func (m *model) handleCommand(...)`. Callers in `model.go` (`m.handleCommand(line)`) compile unchanged because `m` is the model value and Go auto-addresses. Verify there are no other value-receiver usages of `handleCommand` in tests; if so, update them to take `&m`.
- [ ] Add the `slog` import (`"log/slog"`) to commands.go.
- [ ] Run — expect PASS.
- [ ] Commit:

  ```
  git add internal/channel/cli/commands.go internal/channel/cli/commands_test.go
  git commit -m "$(cat <<'EOF'
  feat(cli): add /theme list and /theme <name> commands

  Lists registered themes with the active one marked; set persists to
  ~/.czcli/state.json and refreshes the viewport so the new look is
  visible on the next frame.

  Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
  EOF
  )"
  ```

---

### Task 10 — Startup wiring in cmd/czcli/main.go

**Files:** `cmd/czcli/main.go`

- [ ] No new unit test (main is exercised end-to-end manually). After config.Load succeeds and before `agent.BuildWithMCPInfos`, add:

  ```go
  // Themes: load embedded built-ins, then user themes from ~/.czcli/themes,
  // then resolve the active one. Order: state.json > config.cli.theme >
  // terminal-adapted default-{dark,light}.
  theme.LoadBuiltins()
  themesDir := userThemesDir()
  if themesDir != "" {
  	theme.LoadUserDir(themesDir)
  }
  if active := theme.Resolve(theme.StateFile(), cfg.CLI.Theme); active != nil {
  	slog.Info("theme: active", "name", active.Name)
  }
  ```

- [ ] Add the import `"github.com/caxqueiroz/czcli/internal/theme"`.
- [ ] Add helper at the bottom of `cmd/czcli/main.go`:

  ```go
  // userThemesDir returns ~/.czcli/themes or "" if home cannot be resolved.
  // The directory does not need to exist; LoadUserDir silently no-ops on
  // missing dirs.
  func userThemesDir() string {
  	home, err := os.UserHomeDir()
  	if err != nil {
  		return ""
  	}
  	return filepath.Join(home, ".czcli", "themes")
  }
  ```

- [ ] Run `go build ./...` and `go test ./...` — expect PASS.
- [ ] Manual smoke (not committed as a test):

  ```
  go run ./cmd/czcli   # no config -> writes default + exits gracefully
  CZCLI_CONFIG=./.czcli/config.yaml go run ./cmd/czcli
  # inside: /theme list, /theme dracula, send a markdown reply
  ```

- [ ] Commit:

  ```
  git add cmd/czcli/main.go
  git commit -m "$(cat <<'EOF'
  feat(main): load + resolve themes at startup

  LoadBuiltins -> LoadUserDir(~/.czcli/themes) -> Resolve(state.json,
  config.cli.theme). Theme module is global state so nothing new is
  threaded into agent.Build.

  Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
  EOF
  )"
  ```

---

### Task 11 — Lint + tidy sweep

**Files:** none (clean-up).

- [ ] Run `golangci-lint run ./...` — fix any new issues (likely candidates: unused imports left over from the view rewrite, the now-unused package vars `barStyle`/`bottomBarBg`/etc. — delete them entirely, not just comment them out).
- [ ] Run `go mod tidy` — confirm no diff outside of `glamour` being properly recorded.
- [ ] Run `go test -count=1 ./...` once more — expect green.
- [ ] If anything changed, commit:

  ```
  git add -A
  git commit -m "$(cat <<'EOF'
  chore: lint + tidy after theme/markdown rollout

  Drop dead package-level lipgloss vars superseded by themedStyles;
  go.mod/go.sum unchanged after tidy.

  Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
  EOF
  )"
  ```

---

## Definition of Done — final checks

- [ ] `go build ./...` clean.
- [ ] `go test -count=1 ./...` green.
- [ ] `golangci-lint run ./...` 0 issues.
- [ ] `go mod tidy` is a no-op.
- [ ] `go run ./cmd/czcli` with no config writes the default + exits 0.
- [ ] With a valid config and a valid `OPENAI_API_KEY`, the TUI launches, the bottom row shows accent window markers, separators are thin, assistant replies render as markdown, `/theme list` lists eight built-ins, `/theme dracula` switches live, and the chosen theme survives a restart.
- [ ] All commits sit on `feat/tui-redesign`.
