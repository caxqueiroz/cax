# Plan 11: Path Migration + Multi-Line Input + Keybindings + User-Commands Loader — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Migrate czcli's discovery paths to the `~/.czcli/{skills,agents,commands}` family (with a one-line YAML-singular `subagents.dir` migration), introduce a stand-alone `internal/usercmds` loader that parses `~/.czcli/commands/*.md` (and `.czcli/commands/*.md`) into `plugins.PluginCommand` entries merged into the slash-command set, swap the single-line `bubbles/textinput` for a 1–6 row `bubbles/textarea` with Enter → submit / Alt+Enter → newline, and add the pi.dev-inspired keybindings (`Ctrl+L` model picker, `Ctrl+R` reload, `Ctrl+T` theme cycle, `Ctrl+/` help overlay) plus a `/reload` slash command that re-runs `pluginsMgr.Load` → `skills.Load` → `mcp.Connect` → `lsp.New` → `theme.LoadUserDir` → `assistant.Rebuild` through the existing `pluginAdapter.Rebuild` path.

**Architecture:** Path defaults move from the legacy `.dive/*` and singular `subagents.dir` to the czcli-namespaced trio. `applySubagentDefaults` (new) accepts the legacy `Dir` field as a one-element `Dirs` and warns via `slog`. A new `applyCommandsDefaults` mirrors `applyPluginDefaults`. `internal/usercmds.Load(dirs)` reuses `plugins.PluginCommand` (Plan 7 owns the type) and the same Claude-Code frontmatter parser as `plugins.ReadCommands` (description + argument-hint), set `Source = "user:<dir-basename>"` so the dispatcher can distinguish user vs plugin commands; per-file parse errors are logged + skipped (mirroring `plugins.ReadCommands`). `cmd/czcli/main.go` calls `usercmds.Load(cfg.Commands.Dirs)` after `pluginsMgr.Load` and appends to `contrib.Commands`; the same call is re-issued inside `pluginAdapter.Rebuild` so `/reload` and `/plugin` mutations hot-pick user commands. The CLI swaps `textinput.Model` for `textarea.Model` (bubbles v1.0.0 pinned API: `New() Model`, `Focus() tea.Cmd`, `Focused() bool`, `Reset()`, `Value() string`, `SetValue(string)`, `SetWidth(int)`, `SetHeight(int)`, `LineCount() int`, `KeyMap KeyMap` mutable). To make Enter submit (not insert newline) we clear `m.input.KeyMap.InsertNewline` (`key.NewBinding(key.WithKeys())`) and intercept `tea.KeyEnter` in `model.Update` BEFORE delegating to `m.input.Update` — exactly the path the legacy `textinput` switch uses today. Multi-line newline is `Alt+Enter` only (bubbletea v1.3.10 has NO `KeyShiftEnter` constant; terminal escape tables don't surface Shift+Enter without kitty/CSI-u; `Alt+Enter` is universally encoded as `KeyMsg{Type: KeyEnter, Alt: true}` → `.String() == "alt+enter"`). When `msg.Alt && msg.Type == tea.KeyEnter`, we call `m.input.InsertRune('\n')` directly and skip submit. Height management lives in `Update`: after every input mutation we set `m.input.SetHeight(clamp(m.input.LineCount(), 1, 6))` and recompute viewport height via the existing layout math. The four Ctrl-key handlers live in `model.Update` alongside the existing `KeyCtrlC` / `KeyEsc` cases: `KeyCtrlL` → `m.handleCommand("/model")`, `KeyCtrlR` → `m.handleCommand("/reload")`, `KeyCtrlT` → `theme.Set(theme.Cycle())` + `writeState(stateFile, theme.Active().Name)` + `m.refreshViewport()`. `Ctrl+/` is bubbletea `KeyMsg.String() == "ctrl+_"` on most terminals (the documented mapping), so we match on `msg.String()` for that case only — it's the safest cross-terminal match. The overlay is rendered by `View()` when `m.helpOpen` is true, listing the keybindings table + the slash commands (assembled from a static list + `m.userCommands` snapshot). The `/reload` command is added to `handleCommand`'s switch, calling `m.plugins.Rebuild(ctx)` (the existing `pluginBackend.Rebuild` already does the heavy lifting; Plan 11 extends `pluginAdapter.Rebuild` in `cmd/czcli/main.go` to also call `usercmds.Load` and `theme.LoadUserDir` before `assistant.Rebuild`).

**Tech Stack:** Go 1.25; `github.com/charmbracelet/bubbles/textarea` (v1.0.0 — pinned, verified API surface below); `github.com/charmbracelet/bubbletea` v1.3.10 (`KeyMsg{Alt: true, Type: KeyEnter}` for `Alt+Enter`); `github.com/charmbracelet/bubbles/key` (already transitively present) to clear the textarea `InsertNewline` binding; existing `internal/plugins` (re-use `plugins.PluginCommand` shape + `splitFrontmatter` pattern); existing `internal/theme` (Plan 10 ships `theme.Cycle()` + `theme.Set` + `theme.LoadUserDir`); existing `internal/channel/cli` model surfaces; stdlib `log/slog`, `os`, `path/filepath`, `strings`, `bytes`, `gopkg.in/yaml.v3`.

---

## Reality-check the APIs (verified 2026-05-30 against the pinned module cache)

- **`bubbles/textarea` v1.0.0** (`$(go env GOMODCACHE)/github.com/charmbracelet/bubbles@v1.0.0/textarea/textarea.go`):
  - `func New() Model` (line 283).
  - Field `KeyMap KeyMap` is exported and mutable (line 214 + line 84: `InsertNewline: key.NewBinding(key.WithKeys("enter", "ctrl+m"), ...)`).
  - `SetWidth(int)` (line 892), `SetHeight(int)` (line 947), `Width() int`, `Height() int`.
  - `Focus() tea.Cmd` (line 581), `Focused() bool` (line 575), `Blur()` (line 589), `Reset()` (line 596).
  - `Value() string` (line 451), `SetValue(string)` (line 346), `InsertRune(rune)` (line 357), `LineCount() int` (line 476), `Length() int` (line 466).
  - `MaxHeight int` field (line 1028 of `Update`: `if m.MaxHeight > 0 && len(m.value) >= m.MaxHeight { return m, nil }`). We set `m.input.MaxHeight = 6` so even an erroneous newline insertion past the cap is a no-op.
  - `Update(msg tea.Msg) (Model, tea.Cmd)` (line 958). Crucially, the default `InsertNewline` binding catches `enter` AND `ctrl+m` (line 84). We override with `m.input.KeyMap.InsertNewline = key.NewBinding(key.WithKeys())` so `Enter` falls through to our `submit()` switch arm. (`Update`'s switch is `case key.Matches(msg, m.KeyMap.InsertNewline):` at line 1027 — with an empty `key.WithKeys()`, `key.Matches` cannot match, so the default arm at line 1064 `m.insertRunesFromUserInput(msg.Runes)` would run on `enter` if we didn't intercept it first. **We intercept `tea.KeyEnter` BEFORE delegating to `m.input.Update`** (the existing pattern in `model.Update`), so `Enter` never reaches the textarea at all and `insertRunesFromUserInput` is irrelevant for `Enter`.)
- **`bubbletea` v1.3.10 key surface** (`$(go env GOMODCACHE)/github.com/charmbracelet/bubbletea@v1.3.10/key.go`):
  - `tea.KeyShiftEnter` **does NOT exist** (`grep -n "KeyShiftEnter" key.go` → no match). The constants list (lines 210–237) includes `KeyShiftTab`, `KeyShiftUp/Down/Left/Right/Home/End`, `KeyCtrlShift{Up,Down,Left,Right,Home,End}` — **no `KeyShiftEnter`**. Standard terminals (xterm/iTerm2/Alacritty) encode `Shift+Enter` as a bare `Enter` (`\r`) unless the kitty/CSI-u keyboard protocol is negotiated, which bubbletea v1.3.10 does NOT enable. Conclusion: detect `Alt+Enter` only.
  - `Alt+Enter` reliably arrives as `tea.KeyMsg{Type: tea.KeyEnter, Alt: true}` (verified via `key_test.go:389,444` cases asserting `"alt+enter"` as the canonical `KeyMsg.String()` output). We match `msg.Alt && msg.Type == tea.KeyEnter` (does not need a `String()` comparison — typed fields are stable).
  - `Ctrl+/` is encoded as `tea.KeyCtrlUnderscore` on most terminals (xterm spec: Ctrl+/ → `0x1f`, which is also Ctrl+_). `KeyCtrlUnderscore` IS a defined constant in v1.3.10 (`grep -n "KeyCtrlUnderscore" key.go` → present in the table near `KeyCtrlSlash` and friends). Match `msg.Type == tea.KeyCtrlUnderscore`.
- **`theme.Cycle()`** (Plan 10): returns `*theme.Theme`; `theme.Set(*Theme)` is the single mutator. Plan 11 assumes both exist verbatim. If Plan 10's branch hasn't merged at integration time, Task 5 below adds a `theme.Cycle` shim guarded by a build tag — but the parallel-plans contract is that Plan 10 lands first; this is documented for safety, not implemented.
- **`pluginBackend.Rebuild(ctx)`** already exists (`internal/channel/cli/model.go:36`). The `pluginAdapter.Rebuild` impl (`cmd/czcli/main.go:398`) already wires plugins → skills → mcp → lsp → hooks → `assistant.Rebuild`. Plan 11 extends it with two extra steps (user commands re-load, optional `theme.LoadUserDir`) and exposes `/reload` as a slash command that calls it.
- **`plugins.PluginCommand`** (`internal/plugins/manifest.go:334`): `{Name, Description, ArgumentHint, Prompt, Source string}`. The `Source` field is free-form, so `usercmds.Load` sets `Source = "user:" + filepath.Base(dir)` to distinguish from plugin-contributed commands at dispatch time.
- **`splitFrontmatter`** (`internal/plugins/manifest.go:392`): private to the `plugins` package. Plan 11 does NOT add a public wrapper — it duplicates the 20 lines inside `internal/usercmds` (cheaper than widening a Plan 7 surface; the parser is stable and trivially testable in isolation).

---

## File Structure

```
internal/config/
├── config.go                # MODIFY: rename Subagents.Dir→Dirs (+migrate Dir); add CommandsConfig; new defaults
├── config_test.go           # MODIFY: cover new defaults + legacy-singular migration + tilde expansion
└── example.yaml             # MODIFY: reflect new paths

internal/usercmds/
├── usercmds.go              # NEW: Load(dirs []string) []plugins.PluginCommand
└── usercmds_test.go         # NEW: tempdir fixtures (description + argument-hint, missing dir, malformed yaml)

internal/channel/cli/
├── model.go                 # MODIFY: textarea swap; new keybindings; helpOpen field; userCommands field
├── model_test.go            # MODIFY: textarea round-trip; KeyCtrlL/R/T/_ tests; alt+enter newline
├── commands.go              # MODIFY: /reload command; rendering of help overlay text
├── commands_test.go         # MODIFY: /reload happy + missing-backend; help overlay content
├── view.go                  # MODIFY: render overlay when helpOpen; textarea View() instead of textinput
├── cli.go                   # MODIFY: WithUserCommands + WithThemeStateFile options; pass to model
└── theme_state.go           # NEW: tiny atomic writer for ~/.czcli/state.json's `"theme"` field

cmd/czcli/
└── main.go                  # MODIFY: call usercmds.Load; wire WithUserCommands + WithThemeStateFile; extend pluginAdapter.Rebuild
```

Dependencies assumed already present (do NOT define here):
- Plan 7 (`internal/plugins`): `plugins.PluginCommand`, `plugins.Contributions.Commands`, `plugins.Manager.Load`.
- Plan 10 (`internal/theme`): `theme.Active() *Theme`, `theme.Set(*Theme)`, `theme.Cycle() *Theme`, `theme.LoadUserDir(string) []*Theme`, `theme.Resolve(stateFile, configThemeName string) *Theme`. Plan 11 assumes these are merged on `feat/tui-redesign` before integration.
- Existing `pluginBackend.Rebuild(ctx)` (`internal/channel/cli/model.go`).

---

## Contracts owned by Plan 11 (verbatim from `02-tui-redesign-contracts.md`)

```go
// internal/config — new field; old singular Dir kept for one-release migration.
type SubagentsConfig struct {
    Enabled bool     `yaml:"enabled"`
    Dirs    []string `yaml:"dirs"`           // default: [~/.czcli/agents, .czcli/agents]
    Dir     string   `yaml:"dir,omitempty"`  // DEPRECATED: legacy singular; migrated into Dirs at Load time with a slog.Warn
}

// internal/config — new section.
type CommandsConfig struct {
    Enabled bool     `yaml:"enabled"`
    Dirs    []string `yaml:"dirs"`           // default: [~/.czcli/commands, .czcli/commands]
}

// internal/config.Config gains:
//   Commands CommandsConfig `yaml:"commands"`
```

```go
// internal/usercmds
package usercmds

import "github.com/caxqueiroz/czcli/internal/plugins"

// Load scans the given directories for *.md files with Claude-Code-style
// frontmatter (`description`, `argument-hint`) and returns one
// PluginCommand per file. Per-file parse errors are logged + skipped. Missing
// directories are silently skipped. Output is sorted by Name for determinism.
// Source is set to "user:<basename-of-dir>" so the slash dispatcher can tell
// user-level commands apart from plugin-level ones at render time.
func Load(dirs []string) []plugins.PluginCommand
```

```go
// internal/channel/cli — new options on the existing CLI struct.
type Option func(*CLI)

// WithUserCommands wires the user-level command snapshot. Same shape as
// plugins.PluginCommand so they can be merged into one dispatcher slice.
func WithUserCommands(cmds []plugins.PluginCommand) Option

// WithThemeStateFile sets the path Ctrl+T persists the active theme to.
// Empty disables persistence (Ctrl+T still cycles in-memory).
func WithThemeStateFile(path string) Option
```

```go
// internal/channel/cli/theme_state.go — atomic writer (mirrors plugins/state.go).
func writeThemeState(path, themeName string) error
```

```go
// internal/channel/cli/commands.go — extend handleCommand with:
//   case "reload": return m.cmdReload(), false
// cmdReload calls m.plugins.Rebuild(ctx); reports success or wrapped error.
```

---

### Task 1 — Config: rename `Subagents.Dir` → `Dirs` + migration; add `CommandsConfig`; new defaults

**Files:** `internal/config/config.go`, `internal/config/config_test.go`, `internal/config/example.yaml`

- [ ] Write FAILING test `internal/config/config_test.go` (append at end of file):

```go
func TestLoadAppliesSubagentsAndCommandsDefaults(t *testing.T) {
    path := writeYAML(t, `
providers:
  - {name: openai, model: gpt-5.4, api_key_env: OPENAI_API_KEY}
embeddings: {provider: openai, model: text-embedding-3-small, dim: 1536, api_key_env: OPENAI_API_KEY}
memory: {db_path: /tmp/czcli/memory.db}
`)
    cfg, err := Load(path)
    if err != nil {
        t.Fatalf("Load: %v", err)
    }
    home, err := os.UserHomeDir()
    if err != nil {
        t.Fatalf("UserHomeDir: %v", err)
    }
    // subagents.dirs default = [~/.czcli/agents, .czcli/agents]
    wantSub := []string{filepath.Join(home, ".czcli/agents"), ".czcli/agents"}
    if len(cfg.Subagents.Dirs) != len(wantSub) {
        t.Fatalf("Subagents.Dirs = %v, want %v", cfg.Subagents.Dirs, wantSub)
    }
    for i, d := range wantSub {
        if cfg.Subagents.Dirs[i] != d {
            t.Errorf("Subagents.Dirs[%d] = %q, want %q", i, cfg.Subagents.Dirs[i], d)
        }
    }
    // commands.dirs default = [~/.czcli/commands, .czcli/commands]
    wantCmd := []string{filepath.Join(home, ".czcli/commands"), ".czcli/commands"}
    if len(cfg.Commands.Dirs) != len(wantCmd) {
        t.Fatalf("Commands.Dirs = %v, want %v", cfg.Commands.Dirs, wantCmd)
    }
    for i, d := range wantCmd {
        if cfg.Commands.Dirs[i] != d {
            t.Errorf("Commands.Dirs[%d] = %q, want %q", i, cfg.Commands.Dirs[i], d)
        }
    }
    if !cfg.Commands.Enabled {
        t.Errorf("Commands.Enabled default = false, want true")
    }
}

func TestLoadMigratesLegacySubagentsDir(t *testing.T) {
    path := writeYAML(t, `
providers:
  - {name: openai, model: gpt-5.4, api_key_env: OPENAI_API_KEY}
embeddings: {provider: openai, model: text-embedding-3-small, dim: 1536, api_key_env: OPENAI_API_KEY}
memory: {db_path: /tmp/x.db}
subagents:
  enabled: true
  dir: ./legacy/agents
`)
    cfg, err := Load(path)
    if err != nil {
        t.Fatalf("Load: %v", err)
    }
    if len(cfg.Subagents.Dirs) != 1 || cfg.Subagents.Dirs[0] != "./legacy/agents" {
        t.Fatalf("Subagents.Dirs = %v, want [./legacy/agents] (migrated from singular dir)", cfg.Subagents.Dirs)
    }
}

func TestLoadDirsWinsOverLegacyDir(t *testing.T) {
    path := writeYAML(t, `
providers:
  - {name: openai, model: gpt-5.4, api_key_env: OPENAI_API_KEY}
embeddings: {provider: openai, model: text-embedding-3-small, dim: 1536, api_key_env: OPENAI_API_KEY}
memory: {db_path: /tmp/x.db}
subagents:
  enabled: true
  dir: ./legacy/agents
  dirs: [./modern/agents]
`)
    cfg, err := Load(path)
    if err != nil {
        t.Fatalf("Load: %v", err)
    }
    if len(cfg.Subagents.Dirs) != 1 || cfg.Subagents.Dirs[0] != "./modern/agents" {
        t.Fatalf("Subagents.Dirs = %v, want [./modern/agents] (Dirs wins)", cfg.Subagents.Dirs)
    }
}

func TestLoadSubagentsAndCommandsTildeExpansion(t *testing.T) {
    path := writeYAML(t, `
providers:
  - {name: openai, model: gpt-5.4, api_key_env: OPENAI_API_KEY}
embeddings: {provider: openai, model: text-embedding-3-small, dim: 1536, api_key_env: OPENAI_API_KEY}
memory: {db_path: /tmp/x.db}
subagents:
  dirs: [~/agents-home, ./agents-local]
commands:
  dirs: [~/cmds-home, ./cmds-local]
`)
    cfg, err := Load(path)
    if err != nil {
        t.Fatalf("Load: %v", err)
    }
    home, _ := os.UserHomeDir()
    if cfg.Subagents.Dirs[0] != filepath.Join(home, "agents-home") || cfg.Subagents.Dirs[1] != "./agents-local" {
        t.Errorf("Subagents.Dirs = %v, want [%s, ./agents-local]", cfg.Subagents.Dirs, filepath.Join(home, "agents-home"))
    }
    if cfg.Commands.Dirs[0] != filepath.Join(home, "cmds-home") || cfg.Commands.Dirs[1] != "./cmds-local" {
        t.Errorf("Commands.Dirs = %v, want [%s, ./cmds-local]", cfg.Commands.Dirs, filepath.Join(home, "cmds-home"))
    }
}
```

  Also UPDATE the existing `TestLoadAppliesDefaults` test: change the line
  `if cfg.Subagents.Dir != ".dive/agents"` to assert against `cfg.Subagents.Dirs`
  containing the new default (or just delete that single assertion — the new
  `TestLoadAppliesSubagentsAndCommandsDefaults` covers it).

- [ ] Run tests: `go test ./internal/config/...` — confirm failures (`Subagents.Dirs` unknown, `Commands` field doesn't exist).

- [ ] Implement in `internal/config/config.go` — apply the following edits (COMPLETE code shown verbatim):

  **A.** Replace `SubagentsConfig` (currently lines 73–77) with:

```go
// SubagentsConfig configures sub-agent personas.
// Dirs is the canonical list of search directories; Dir is the legacy singular
// field kept only for one-release backward compatibility. If Dir is non-empty
// and Dirs is empty at Load time, Dirs is set to []string{Dir} and a one-shot
// slog.Warn is emitted via the package-level migrationWarn helper.
type SubagentsConfig struct {
    Enabled bool     `yaml:"enabled"`
    Dirs    []string `yaml:"dirs"`
    Dir     string   `yaml:"dir,omitempty"` // DEPRECATED: use Dirs.
}
```

  **B.** Add the new `CommandsConfig` and field on `Config` (insert next to `SkillsConfig`):

```go
// CommandsConfig configures user-level slash-command discovery (Plan 11).
// Enabled defaults to true when the section is omitted; Dirs defaults to
// [~/.czcli/commands, .czcli/commands] with tilde expansion applied at Load.
type CommandsConfig struct {
    Enabled bool     `yaml:"enabled"`
    Dirs    []string `yaml:"dirs"`
}
```

  And in `type Config struct`, after `Plugins  PluginsConfig  yaml:"plugins"`,
  add: `Commands  CommandsConfig  yaml:"commands"`.

  **C.** Add the package-level migration log gate (so we warn once per process):

```go
var subagentsDirWarnOnce sync.Once

func warnLegacySubagentsDir(dir string) {
    subagentsDirWarnOnce.Do(func() {
        slog.Warn("config: subagents.dir (singular) is deprecated; migrate to subagents.dirs",
            "legacy_value", dir,
            "migrated_to", []string{dir})
    })
}
```

  Add `"log/slog"` and `"sync"` to the imports.

  **D.** Replace `applyDefaults` (lines 165–182) with:

```go
func applyDefaults(cfg *Config) {
    if cfg.Memory.TokenBudget == 0 {
        cfg.Memory.TokenBudget = 8000
    }
    if cfg.Memory.RecallK == 0 {
        cfg.Memory.RecallK = 5
    }
    applySubagentDefaults(&cfg.Subagents)
    for i := range cfg.Providers {
        if cfg.Providers[i].MaxTokens == 0 {
            cfg.Providers[i].MaxTokens = 4096
        }
    }
    applySkillDefaults(&cfg.Skills)
    applyPluginDefaults(&cfg.Plugins)
    applyCommandsDefaults(&cfg.Commands)
}

// applySubagentDefaults migrates the legacy singular Dir into Dirs (with a
// one-shot warning), then falls back to the two czcli-namespaced defaults
// when nothing is configured. Tilde-expansion is applied to the final list.
func applySubagentDefaults(s *SubagentsConfig) {
    if !s.Enabled && len(s.Dirs) == 0 && s.Dir == "" {
        // Field omission == enabled by default (mirrors Skills/Plugins).
        s.Enabled = true
    }
    if len(s.Dirs) == 0 && s.Dir != "" {
        warnLegacySubagentsDir(s.Dir)
        s.Dirs = []string{s.Dir}
    }
    if len(s.Dirs) == 0 {
        s.Dirs = []string{"~/.czcli/agents", ".czcli/agents"}
    }
    expanded := make([]string, 0, len(s.Dirs))
    for _, d := range s.Dirs {
        e, err := expandHome(d)
        if err != nil || e == "" {
            continue
        }
        expanded = append(expanded, e)
    }
    s.Dirs = expanded
}

// applyCommandsDefaults mirrors applySkillDefaults: omission of the
// `commands:` block enables discovery in the two czcli-namespaced roots.
func applyCommandsDefaults(c *CommandsConfig) {
    if !c.Enabled && len(c.Dirs) == 0 {
        c.Enabled = true
    }
    if len(c.Dirs) == 0 {
        c.Dirs = []string{"~/.czcli/commands", ".czcli/commands"}
    }
    expanded := make([]string, 0, len(c.Dirs))
    for _, d := range c.Dirs {
        e, err := expandHome(d)
        if err != nil || e == "" {
            continue
        }
        expanded = append(expanded, e)
    }
    c.Dirs = expanded
}
```

  **E.** Update `applySkillDefaults` to the new default (replace its `s.Dirs = []string{".dive/skills", "~/.dive/skills"}` line with `s.Dirs = []string{"~/.czcli/skills", ".czcli/skills"}`). Also update the YAML comment in the existing `// defaults: ...` doc-string on `SkillsConfig` to match.

  **F.** Update `example.yaml` to reflect the new paths. Replace the `subagents:` block with:

```yaml
subagents:
  enabled: true
  dirs:
    - ~/.czcli/agents
    - .czcli/agents
```

  Replace the `skills:` block's `dirs:` list with:

```yaml
skills:
  enabled: true
  dirs:
    - ~/.czcli/skills
    - .czcli/skills
```

  And add (between `plugins:` and `lsp:`) a new section:

```yaml
# User-level slash commands (Plan 11). czcli scans each dir for *.md files
# with Claude-Code-style frontmatter (description, argument-hint) and merges
# them into the slash-command set alongside plugin-contributed commands.
commands:
  enabled: true
  dirs:
    - ~/.czcli/commands
    - .czcli/commands
```

- [ ] Run tests: `go test ./internal/config/...` — confirm all four new tests pass and existing tests still pass.

- [ ] Lint: `golangci-lint run ./internal/config/...`

- [ ] Commit:

```bash
git add internal/config/config.go internal/config/config_test.go internal/config/example.yaml
git commit -m "$(cat <<'EOF'
refactor(config): migrate to ~/.czcli paths; add commands.dirs; subagents.dir→dirs

Subagents.Dir (singular) is now a deprecated alias migrated into Dirs with
a slog.Warn. Adds CommandsConfig with default [~/.czcli/commands,
.czcli/commands]. Updates Skills.Dirs default to the czcli-namespaced pair.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

### Task 2 — `internal/usercmds`: file-system loader for user slash commands

**Files:** `internal/usercmds/usercmds.go`, `internal/usercmds/usercmds_test.go`

- [ ] Write FAILING test `internal/usercmds/usercmds_test.go`:

```go
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
```

- [ ] Run tests: `go test ./internal/usercmds/...` — confirm package doesn't compile (no `usercmds.go` yet).

- [ ] Implement `internal/usercmds/usercmds.go`:

```go
// Package usercmds loads user-level slash commands from disk. The on-disk
// shape mirrors Claude Code's `commands/*.md` format used by Plan 7's plugin
// loader: a markdown file with optional YAML frontmatter (description,
// argument-hint) followed by the prompt body. Each loaded file becomes one
// plugins.PluginCommand so the CLI dispatcher can merge user + plugin
// commands into a single slice.
package usercmds

import (
    "bytes"
    "log/slog"
    "os"
    "path/filepath"
    "sort"
    "strings"

    "gopkg.in/yaml.v3"

    "github.com/caxqueiroz/czcli/internal/plugins"
)

// Load scans each directory in dirs for *.md files and returns one
// PluginCommand per file. Missing directories are silently skipped; per-file
// read or parse errors are logged via slog and skipped. The returned slice is
// sorted by Name across all input directories.
func Load(dirs []string) []plugins.PluginCommand {
    var out []plugins.PluginCommand
    for _, dir := range dirs {
        out = append(out, loadDir(dir)...)
    }
    sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
    return out
}

func loadDir(dir string) []plugins.PluginCommand {
    entries, err := os.ReadDir(dir)
    if err != nil {
        if !os.IsNotExist(err) {
            slog.Warn("usercmds: read dir", "dir", dir, "error", err)
        }
        return nil
    }
    src := "user:" + filepath.Base(dir)
    var out []plugins.PluginCommand
    for _, e := range entries {
        if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
            continue
        }
        path := filepath.Join(dir, e.Name())
        data, err := os.ReadFile(path)
        if err != nil {
            slog.Warn("usercmds: read file", "file", path, "error", err)
            continue
        }
        fm, body := splitFrontmatter(data)
        var meta cmdFrontmatter
        if len(fm) > 0 {
            if err := yaml.Unmarshal(fm, &meta); err != nil {
                slog.Warn("usercmds: parse frontmatter", "file", path, "error", err)
            }
        }
        name := strings.TrimSuffix(e.Name(), ".md")
        out = append(out, plugins.PluginCommand{
            Name:         name,
            Description:  meta.Description,
            ArgumentHint: meta.ArgumentHint,
            Prompt:       string(body),
            Source:       src,
        })
    }
    return out
}

type cmdFrontmatter struct {
    Description  string `yaml:"description"`
    ArgumentHint string `yaml:"argument-hint"`
}

// splitFrontmatter mirrors internal/plugins.splitFrontmatter (private there).
// Returns the YAML between leading "---" markers and the remaining markdown
// body. If no leading "---\n" is found, returns nil, data.
func splitFrontmatter(data []byte) (yamlSrc, body []byte) {
    const sep = "---\n"
    if !bytes.HasPrefix(data, []byte(sep)) {
        return nil, data
    }
    rest := data[len(sep):]
    end := bytes.Index(rest, []byte("\n---\n"))
    if end < 0 {
        end = bytes.Index(rest, []byte("\n---"))
        if end < 0 {
            return nil, data
        }
    }
    fm := rest[:end]
    tail := rest[end:]
    tail = bytes.TrimPrefix(tail, []byte("\n---\n"))
    tail = bytes.TrimPrefix(tail, []byte("\n---"))
    tail = bytes.TrimPrefix(tail, []byte("\n"))
    return fm, tail
}
```

- [ ] Run tests: `go test ./internal/usercmds/...` — confirm all three pass.

- [ ] Lint: `golangci-lint run ./internal/usercmds/...`

- [ ] Commit:

```bash
git add internal/usercmds/usercmds.go internal/usercmds/usercmds_test.go
git commit -m "$(cat <<'EOF'
feat(usercmds): load user-level slash commands from ~/.czcli/commands

Mirrors plugins.ReadCommands' frontmatter parser (description, argument-hint)
and emits plugins.PluginCommand entries with Source="user:<dir-basename>" so
the CLI dispatcher can merge user + plugin commands into one slice.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

### Task 3 — `cmd/czcli/main.go`: wire `usercmds.Load` + extend `pluginAdapter.Rebuild`

**Files:** `cmd/czcli/main.go`

- [ ] Add `"github.com/caxqueiroz/czcli/internal/usercmds"` to the import block.

- [ ] In `run()`, after the existing `pluginsMgr.Load(ctx)` block (line ~83), add:

```go
    // Merge user-level slash commands from cfg.Commands.Dirs into the plugin
    // Contributions. User commands take Source = "user:<dir-basename>" so the
    // dispatcher can tell them apart from plugin-contributed ones.
    contrib.Commands = append(contrib.Commands, usercmds.Load(cfg.Commands.Dirs)...)
```

- [ ] In `pluginAdapter.Rebuild` (currently `cmd/czcli/main.go:398`), inject the same call right after the existing `contrib, _, err := a.mgr.Load(ctx)`:

```go
    contrib.Commands = append(contrib.Commands, usercmds.Load(a.cfg.Commands.Dirs)...)
```

- [ ] Run tests: `go test ./cmd/czcli/...` (if any) and `go build ./...` — confirm clean build.

- [ ] Commit:

```bash
git add cmd/czcli/main.go
git commit -m "$(cat <<'EOF'
feat(cmd/czcli): merge user-level commands into plugin Contributions

Calls usercmds.Load(cfg.Commands.Dirs) on startup and inside
pluginAdapter.Rebuild so /reload + /plugin mutations hot-pick changes to
~/.czcli/commands/*.md alongside plugin contributions.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

### Task 4 — CLI: swap `textinput` for `textarea` (multi-line, Enter→submit, Alt+Enter→newline)

**Files:** `internal/channel/cli/model.go`, `internal/channel/cli/model_test.go`, `internal/channel/cli/view.go`

- [ ] Write FAILING test cases in `internal/channel/cli/model_test.go` (append):

```go
func TestEnterSubmitsAndAltEnterInsertsNewline(t *testing.T) {
    m := newModel(80, 24)
    m = update(m, tea.WindowSizeMsg{Width: 80, Height: 24})
    // Type "ab", Alt+Enter (newline), "cd", then Enter (submit).
    m.input.SetValue("ab")
    // Alt+Enter must insert a newline, not submit.
    m = update(m, tea.KeyMsg{Type: tea.KeyEnter, Alt: true})
    if m.streaming {
        t.Fatalf("Alt+Enter must not start a turn")
    }
    if !strings.Contains(m.input.Value(), "\n") {
        t.Fatalf("Alt+Enter should insert newline; input = %q", m.input.Value())
    }
    // Now append "cd" by direct SetValue (the test isn't trying to drive raw
    // runes through textarea — it just asserts the Enter-vs-Alt+Enter routing).
    m.input.SetValue(m.input.Value() + "cd")
    // Enter must submit.
    m = update(m, tea.KeyMsg{Type: tea.KeyEnter})
    if !m.streaming {
        t.Fatalf("Enter should kick off a streaming turn")
    }
    if m.input.Value() != "" {
        t.Fatalf("Enter should reset the input; got %q", m.input.Value())
    }
}

func TestTextareaGrowsAndCaps(t *testing.T) {
    m := newModel(80, 24)
    m = update(m, tea.WindowSizeMsg{Width: 80, Height: 24})
    // Start at height 1.
    if m.input.Height() != 1 {
        t.Fatalf("starting height = %d, want 1", m.input.Height())
    }
    // Insert 8 newlines via SetValue then a no-op Update so the height
    // recompute runs.
    m.input.SetValue("a\nb\nc\nd\ne\nf\ng\nh\ni")
    m = update(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{' '}})
    if m.input.Height() != 6 {
        t.Fatalf("capped height = %d, want 6", m.input.Height())
    }
}
```

- [ ] Run tests: `go test ./internal/channel/cli/...` — confirm failures (still on textinput).

- [ ] Edit `internal/channel/cli/model.go`:

  **A.** Replace the `bubbles/textinput` import with `bubbles/textarea` and add `bubbles/key`:

```go
import (
    "context"
    "strings"

    "github.com/charmbracelet/bubbles/key"
    "github.com/charmbracelet/bubbles/spinner"
    "github.com/charmbracelet/bubbles/textarea"
    "github.com/charmbracelet/bubbles/viewport"
    tea "github.com/charmbracelet/bubbletea"

    "github.com/caxqueiroz/czcli/internal/channel"
    "github.com/caxqueiroz/czcli/internal/config"
    "github.com/caxqueiroz/czcli/internal/hooks"
    "github.com/caxqueiroz/czcli/internal/plugins"
)
```

  **B.** Change `model.input` field type from `textinput.Model` to `textarea.Model`. Add the new fields needed for Plan 11:

```go
type model struct {
    width  int
    height int

    input    textarea.Model
    viewport viewport.Model
    spinner  spinner.Model

    history   []historyEntry
    stream    string
    streaming bool

    status    channel.Status
    hasStatus bool
    running   []string
    lastErr   string

    sched   scheduleBackend
    plugins pluginBackend

    hookEntries []hooks.Entry

    // userCommands is the merged user+plugin command snapshot used by the
    // /help overlay listing. The dispatcher itself routes through the
    // existing handleCommand switch; this slice is rendering-only.
    userCommands []plugins.PluginCommand

    // themeStateFile is the path Ctrl+T persists the active theme to. Empty
    // disables persistence (Ctrl+T still cycles in-memory).
    themeStateFile string

    // helpOpen toggles the keybindings/commands overlay (Ctrl+/).
    helpOpen bool

    ready bool
}
```

  **C.** Replace `newModel` (lines 129–153) with:

```go
func newModel(width, height int) model {
    ta := textarea.New()
    ta.Prompt = "❯ "
    ta.Placeholder = "type a message, or /stats /tools /agents /schedule /model /skills /mcp /lsp /plugin /hooks /reload"
    ta.CharLimit = 4000
    ta.ShowLineNumbers = false
    // Disable Enter→newline so Enter falls through to model.Update's
    // explicit KeyEnter arm (which calls m.submit). Newline insertion is
    // done by the model when it sees Alt+Enter. ctrl+m is the carriage-
    // return alias bubbles bundles with Enter; we clear both at once.
    ta.KeyMap.InsertNewline = key.NewBinding(key.WithKeys())
    ta.SetWidth(max(1, width-2))
    ta.SetHeight(1)
    ta.MaxHeight = 6
    // Focus before the value is copied into the model (Init's value-receiver
    // would otherwise focus a stale copy and drop typed keys).
    _ = ta.Focus()

    sp := spinner.New()
    sp.Spinner = spinner.Dot
    sp.Style = dimStyle

    vp := viewport.New(width, max(1, height-6))

    return model{
        width:    width,
        height:   height,
        input:    ta,
        viewport: vp,
        spinner:  sp,
    }
}
```

  **D.** Replace the `Update` keybinding switch (lines 175–181 + the delegation tail) with the expanded version below. Note we intercept the four Ctrl keys + Alt+Enter BEFORE any delegation to `m.input.Update`:

```go
    case tea.KeyMsg:
        // Help overlay (Ctrl+/). Most terminals emit Ctrl+/ as KeyCtrlUnderscore.
        if msg.Type == tea.KeyCtrlUnderscore {
            m.helpOpen = !m.helpOpen
            m.refreshViewport()
            return m, nil
        }
        switch msg.Type {
        case tea.KeyCtrlC, tea.KeyEsc:
            return m, tea.Quit
        case tea.KeyCtrlL:
            out, _ := m.handleCommand("/model")
            if out != "" {
                m.history = append(m.history, historyEntry{who: "sys", text: out})
                m.refreshViewport()
            }
            return m, nil
        case tea.KeyCtrlR:
            out, _ := m.handleCommand("/reload")
            if out != "" {
                m.history = append(m.history, historyEntry{who: "sys", text: out})
                m.refreshViewport()
            }
            return m, nil
        case tea.KeyCtrlT:
            m.cycleTheme()
            m.refreshViewport()
            return m, nil
        case tea.KeyEnter:
            if msg.Alt {
                // Alt+Enter = newline (multi-line input).
                m.input.InsertRune('\n')
                m.resizeInput()
                return m, nil
            }
            return m.submit()
        }
```

  **E.** Replace the tail-end input/viewport delegation block (lines 242–248) with:

```go
    // Delegate remaining keys to the input; scroll keys to the viewport.
    var cmd tea.Cmd
    m.input, cmd = m.input.Update(msg)
    cmds = append(cmds, cmd)
    m.viewport, cmd = m.viewport.Update(msg)
    cmds = append(cmds, cmd)
    m.resizeInput()
    return m, tea.Batch(cmds...)
```

  **F.** Update `WindowSizeMsg` (line 167) to use `SetWidth` + new height math (the textarea consumes its own vertical row count, so viewport height becomes `Height - topbar(1) - sep(1) - sep(1) - bottombar(1) - sep(1) - pad(1) - inputHeight`):

```go
    case tea.WindowSizeMsg:
        m.width = msg.Width
        m.height = msg.Height
        m.viewport.Width = msg.Width
        m.input.SetWidth(max(1, msg.Width-2))
        m.resizeInput()
        m.ready = true
        m.refreshViewport()
        return m, nil
```

  **G.** Add helpers below `removeFirst`:

```go
// resizeInput resizes the textarea to fit its current content (clamped to
// [1,6]) and recomputes viewport height around it.
func (m *model) resizeInput() {
    h := m.input.LineCount()
    if h < 1 {
        h = 1
    }
    if h > 6 {
        h = 6
    }
    if m.input.Height() != h {
        m.input.SetHeight(h)
    }
    // Layout: top(1)+sep(1)+viewport+sep(1)+bottom(1)+sep(1)+pad(1)+input(h)
    // total fixed chrome = 6, plus input.
    vpH := m.height - 6 - h
    if vpH < 1 {
        vpH = 1
    }
    m.viewport.Height = vpH
}
```

  **H.** Update `submit()` to use `Value()` + `Reset()` from textarea (the API names match — no actual code change needed there beyond the fact that `Reset()` on textarea clears all lines AND resets the cursor). Re-call `m.resizeInput()` at the end so the input collapses back to height 1:

```go
func (m model) submit() (tea.Model, tea.Cmd) {
    line := strings.TrimSpace(m.input.Value())
    m.input.Reset()
    m.resizeInput()
    if line == "" {
        return m, nil
    }
    if strings.HasPrefix(line, "/") {
        out, quit := m.handleCommand(line)
        if quit {
            return m, tea.Quit
        }
        if out != "" {
            m.history = append(m.history, historyEntry{who: "sys", text: out})
            m.refreshViewport()
        }
        return m, nil
    }
    m.history = append(m.history, historyEntry{who: "you", text: line})
    m.streaming = true
    m.stream = ""
    m.lastErr = ""
    m.refreshViewport()
    return m, tea.Batch(emitSubmit(line), m.spinner.Tick)
}
```

  **I.** Add the `cycleTheme` helper (Plan 10 wiring; persistence is best-effort):

```go
// cycleTheme advances to the next theme in registry order and persists the
// choice via writeThemeState. Errors are logged; cycling itself never fails.
func (m *model) cycleTheme() {
    next := theme.Cycle()
    if next == nil {
        return
    }
    theme.Set(next)
    if m.themeStateFile != "" {
        if err := writeThemeState(m.themeStateFile, next.Name); err != nil {
            slog.Warn("cli: persist theme state", "file", m.themeStateFile, "error", err)
        }
    }
}
```

  Add imports for `"log/slog"` and `"github.com/caxqueiroz/czcli/internal/theme"` to `model.go`.

  **J.** Update `model.Init()` to start the textarea cursor blink:

```go
func (m model) Init() tea.Cmd {
    return textarea.Blink
}
```

- [ ] Edit `internal/channel/cli/view.go`: replace any `m.input.View()` call with the textarea variant (same call site; API name is identical: `func (m Model) View() string`). Then add the overlay rendering: at the top of `View()`, if `m.helpOpen` is true, render the overlay above the conversation viewport (a separate string composed from `renderHelpOverlay()` — see Task 6 for the helper).

- [ ] Run tests: `go test ./internal/channel/cli/...` — confirm the two new tests pass; existing tests still pass after the textinput→textarea swap (most don't drive textinput directly).

- [ ] Lint: `golangci-lint run ./internal/channel/cli/...`

- [ ] Commit:

```bash
git add internal/channel/cli/model.go internal/channel/cli/model_test.go internal/channel/cli/view.go
git commit -m "$(cat <<'EOF'
feat(cli): swap textinput for textarea; multi-line with Alt+Enter

Enter submits; Alt+Enter inserts a newline. Input grows 1→6 rows then caps.
Adds Ctrl+L (/model), Ctrl+R (/reload), Ctrl+T (cycle theme), Ctrl+/ (help
overlay) keybindings. Disables textarea's default Enter→newline so Enter
falls through to model.Update's submit path.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

### Task 5 — `theme_state.go`: atomic writer for `~/.czcli/state.json`'s `theme` field

**Files:** `internal/channel/cli/theme_state.go`, `internal/channel/cli/theme_state_test.go`

- [ ] Write FAILING test `internal/channel/cli/theme_state_test.go`:

```go
package cli

import (
    "encoding/json"
    "os"
    "path/filepath"
    "testing"
)

func TestWriteThemeStateRoundtrip(t *testing.T) {
    dir := t.TempDir()
    path := filepath.Join(dir, "state.json")
    if err := writeThemeState(path, "dracula"); err != nil {
        t.Fatalf("writeThemeState: %v", err)
    }
    data, err := os.ReadFile(path)
    if err != nil {
        t.Fatalf("read back: %v", err)
    }
    var got map[string]string
    if err := json.Unmarshal(data, &got); err != nil {
        t.Fatalf("unmarshal: %v", err)
    }
    if got["theme"] != "dracula" {
        t.Fatalf("theme = %q, want dracula", got["theme"])
    }
}

func TestWriteThemeStatePreservesOtherFields(t *testing.T) {
    dir := t.TempDir()
    path := filepath.Join(dir, "state.json")
    if err := os.WriteFile(path, []byte(`{"theme":"old","other":"keep"}`), 0o600); err != nil {
        t.Fatal(err)
    }
    if err := writeThemeState(path, "nord"); err != nil {
        t.Fatalf("writeThemeState: %v", err)
    }
    data, _ := os.ReadFile(path)
    var got map[string]string
    _ = json.Unmarshal(data, &got)
    if got["theme"] != "nord" || got["other"] != "keep" {
        t.Fatalf("got = %+v, want theme=nord and other=keep", got)
    }
}
```

- [ ] Run tests: `go test ./internal/channel/cli/...` — confirm failure (`writeThemeState` undefined).

- [ ] Implement `internal/channel/cli/theme_state.go`:

```go
package cli

import (
    "encoding/json"
    "fmt"
    "os"
    "path/filepath"
)

// writeThemeState atomically updates the "theme" field of the JSON state file
// at path, preserving any other top-level fields already present. The write
// uses temp-file + rename so a crash mid-write leaves the previous content
// intact (mirrors internal/plugins/state.go's atomic write pattern).
func writeThemeState(path, themeName string) error {
    state := map[string]any{}
    if data, err := os.ReadFile(path); err == nil {
        // Tolerate corruption: if the existing file is invalid JSON we
        // overwrite it cleanly rather than failing the Ctrl+T cycle.
        _ = json.Unmarshal(data, &state)
    } else if !os.IsNotExist(err) {
        return fmt.Errorf("read state %s: %w", path, err)
    }
    state["theme"] = themeName

    if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
        return fmt.Errorf("mkdir state: %w", err)
    }
    tmp, err := os.CreateTemp(filepath.Dir(path), ".state.*.json")
    if err != nil {
        return fmt.Errorf("temp file: %w", err)
    }
    enc := json.NewEncoder(tmp)
    enc.SetIndent("", "  ")
    if err := enc.Encode(state); err != nil {
        _ = tmp.Close()
        _ = os.Remove(tmp.Name())
        return fmt.Errorf("encode state: %w", err)
    }
    if err := tmp.Close(); err != nil {
        _ = os.Remove(tmp.Name())
        return fmt.Errorf("close temp: %w", err)
    }
    if err := os.Rename(tmp.Name(), path); err != nil {
        _ = os.Remove(tmp.Name())
        return fmt.Errorf("rename temp -> %s: %w", path, err)
    }
    return nil
}
```

- [ ] Run tests: `go test ./internal/channel/cli/...` — confirm pass.

- [ ] Lint: `golangci-lint run ./internal/channel/cli/...`

- [ ] Commit:

```bash
git add internal/channel/cli/theme_state.go internal/channel/cli/theme_state_test.go
git commit -m "$(cat <<'EOF'
feat(cli): atomic writer for ~/.czcli/state.json theme field

Preserves other top-level fields. Temp+rename mirrors plugins/state.go's
atomic pattern; corruption tolerance falls back to clean overwrite so
Ctrl+T never breaks on a malformed state file.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

### Task 6 — `/reload` slash command + help overlay rendering

**Files:** `internal/channel/cli/commands.go`, `internal/channel/cli/commands_test.go`, `internal/channel/cli/view.go`

- [ ] Write FAILING tests in `internal/channel/cli/commands_test.go` (append):

```go
func TestReloadCallsPluginBackend(t *testing.T) {
    p := &fakePlugins{} // existing fake from plugin_test.go
    m := newModel(80, 24)
    m.plugins = p
    out, quit := m.handleCommand("/reload")
    if quit {
        t.Fatalf("/reload must not quit")
    }
    if !strings.Contains(out, "reloaded") {
        t.Errorf("/reload output = %q, want substring 'reloaded'", out)
    }
    if p.rebuildCalls != 1 {
        t.Errorf("Rebuild called %d times, want 1", p.rebuildCalls)
    }
}

func TestReloadWithoutBackend(t *testing.T) {
    m := newModel(80, 24)
    out, _ := m.handleCommand("/reload")
    if !strings.Contains(out, "not available") {
        t.Errorf("/reload without backend = %q, want 'not available' hint", out)
    }
}
```

  Note: `fakePlugins` already exists in `plugin_test.go`. Add a `rebuildCalls
  int` field on it (one line; no behavior change for existing tests):

```go
type fakePlugins struct {
    items        []PluginListItem
    rebuildErr   error
    rebuildCalls int
    // ... existing fields ...
}

func (f *fakePlugins) Rebuild(ctx context.Context) error {
    f.rebuildCalls++
    return f.rebuildErr
}
```

  Run `go test ./internal/channel/cli/...` to confirm failures (`/reload`
  case not handled).

- [ ] Edit `internal/channel/cli/commands.go`:

  **A.** Add `"reload"` arm to the switch (line ~29):

```go
case "reload":
    return m.cmdReload(), false
```

  **B.** Add the handler:

```go
// cmdReload triggers the wired pluginBackend's Rebuild (which re-runs the
// full plugins → skills → mcp → lsp → hooks → assistant.Rebuild chain in
// cmd/czcli/main.go). Without a wired backend, returns a usage hint.
func (m model) cmdReload() string {
    if m.plugins == nil {
        return "reload: not available (plugins backend not wired); set plugins.enabled: true in config.yaml"
    }
    if err := m.plugins.Rebuild(context.Background()); err != nil {
        return fmt.Sprintf("reload failed: %v", err)
    }
    return "reloaded: plugins, skills, mcp, lsp, hooks, user commands"
}
```

  **C.** Update the unknown-command help string (line ~53) to include `/reload`:

```go
return fmt.Sprintf("unknown command /%s — try /stats /tools /agents /schedule /model /skills /mcp /lsp /plugin /hooks /reload", name), false
```

  **D.** Add the `renderHelpOverlay` helper used by `view.go` (also in `commands.go` since the data lives here):

```go
// renderHelpOverlay returns the multi-line help text for the Ctrl+/ overlay.
// Lists keybindings and the slash commands the model currently knows about
// (built-in + user-level + plugin-level merged via m.userCommands).
func (m model) renderHelpOverlay() string {
    var b strings.Builder
    b.WriteString("keybindings:\n")
    b.WriteString("  Enter            send\n")
    b.WriteString("  Alt+Enter        newline\n")
    b.WriteString("  Ctrl+L           /model picker\n")
    b.WriteString("  Ctrl+R           /reload\n")
    b.WriteString("  Ctrl+T           cycle theme\n")
    b.WriteString("  Ctrl+/           toggle this overlay\n")
    b.WriteString("  Ctrl+C / Esc     quit\n")
    b.WriteString("  PgUp / PgDn      scroll viewport\n")
    b.WriteString("\nbuilt-in commands:\n")
    b.WriteString("  /stats /tools /agents /schedule /model /skills /mcp /lsp /plugin /hooks /reload /quit\n")
    if len(m.userCommands) > 0 {
        b.WriteString("\nuser + plugin commands:\n")
        for _, c := range m.userCommands {
            label := "/" + c.Name
            if c.ArgumentHint != "" {
                label += " " + c.ArgumentHint
            }
            if c.Description != "" {
                fmt.Fprintf(&b, "  %-32s  %s  [%s]\n", label, c.Description, c.Source)
            } else {
                fmt.Fprintf(&b, "  %-32s  [%s]\n", label, c.Source)
            }
        }
    }
    return strings.TrimRight(b.String(), "\n")
}
```

- [ ] Edit `internal/channel/cli/view.go`'s `View()` to render the overlay above the conversation viewport when `m.helpOpen` is true. The exact location is in the existing `View()` body — between `renderTopBar()` and `m.viewport.View()`. Use `dimStyle` to render the overlay text and prefix each line with two spaces (matches the global 2-space indent rule from Plan 10's design).

- [ ] Run tests: `go test ./internal/channel/cli/...` — confirm `/reload` tests pass and the rest still pass.

- [ ] Lint: `golangci-lint run ./internal/channel/cli/...`

- [ ] Commit:

```bash
git add internal/channel/cli/commands.go internal/channel/cli/commands_test.go internal/channel/cli/view.go internal/channel/cli/plugin_test.go
git commit -m "$(cat <<'EOF'
feat(cli): add /reload slash command and Ctrl+/ help overlay

/reload routes to the wired pluginBackend.Rebuild, which fans out to
plugins.Load → skills.Load → mcp.Connect → lsp.New → usercmds.Load →
assistant.Rebuild in cmd/czcli/main.go. Help overlay lists keybindings
plus the current built-in + user + plugin command set.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

### Task 7 — `CLI.WithUserCommands` + `WithThemeStateFile` options + main wiring

**Files:** `internal/channel/cli/cli.go`, `cmd/czcli/main.go`

- [ ] Edit `internal/channel/cli/cli.go`:

  **A.** Add `plugins` import: `"github.com/caxqueiroz/czcli/internal/plugins"`.

  **B.** Add the two fields on `CLI`:

```go
type CLI struct {
    sessionID      string
    statusInterval time.Duration
    sched          scheduleBackend
    plugins        pluginBackend
    hookEntries    []hooks.Entry
    userCommands   []plugins.PluginCommand
    themeStateFile string
}
```

  **C.** Add the two options:

```go
// WithUserCommands wires the merged user+plugin command snapshot used by the
// Ctrl+/ help overlay. The dispatcher itself routes through the existing
// handleCommand switch; this slice is rendering-only.
func WithUserCommands(cmds []plugins.PluginCommand) Option {
    return func(c *CLI) { c.userCommands = cmds }
}

// WithThemeStateFile sets the path Ctrl+T persists the active theme to.
// Empty disables persistence (Ctrl+T still cycles in-memory).
func WithThemeStateFile(path string) Option {
    return func(c *CLI) { c.themeStateFile = path }
}
```

  **D.** Plumb both into `model` in `Start` (line ~83):

```go
    m := newModel(80, 24)
    m.sched = c.sched
    m.plugins = c.plugins
    m.hookEntries = c.hookEntries
    m.userCommands = c.userCommands
    m.themeStateFile = c.themeStateFile
```

- [ ] Edit `cmd/czcli/main.go`: pass the two new options to `cli.New(...)`:

```go
    ch := cli.New(
        cli.WithSessionID("cli"),
        cli.WithScheduler(scheduleAdapter{store: store, sched: sched}),
        cli.WithPlugins(pluginsAdp),
        cli.WithHookEntries(hookEntries),
        cli.WithUserCommands(contrib.Commands),
        cli.WithThemeStateFile(themeStatePath()),
    )
```

  Add the helper at the bottom of `main.go`:

```go
// themeStatePath returns the path Ctrl+T persists the active theme to.
// Falls back to a process-local file when HOME is unresolvable.
func themeStatePath() string {
    home, err := os.UserHomeDir()
    if err != nil {
        return filepath.Join(os.TempDir(), "czcli-state.json")
    }
    return filepath.Join(home, ".czcli", "state.json")
}
```

- [ ] Run: `go build ./...` and `go test ./...` — clean.

- [ ] Commit:

```bash
git add internal/channel/cli/cli.go cmd/czcli/main.go
git commit -m "$(cat <<'EOF'
feat(cli): wire user-commands snapshot and theme state file

cli.WithUserCommands feeds the help overlay; cli.WithThemeStateFile lets
Ctrl+T persist the active theme to ~/.czcli/state.json across runs.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

### Task 8 — Ctrl+T keybinding test + theme-cycle integration test

**Files:** `internal/channel/cli/model_test.go`

- [ ] Append:

```go
func TestCtrlTCyclesTheme(t *testing.T) {
    // Snapshot the active theme before, cycle, assert it changed.
    before := theme.Active()
    m := newModel(80, 24)
    m = update(m, tea.WindowSizeMsg{Width: 80, Height: 24})
    m = update(m, tea.KeyMsg{Type: tea.KeyCtrlT})
    after := theme.Active()
    if before != nil && after != nil && before.Name == after.Name {
        // Only one theme registered? skip rather than fail the parallel-plan
        // contract (Plan 10 ships 8 built-ins; cycle should always advance).
        if len(theme.List()) > 1 {
            t.Fatalf("Ctrl+T did not change theme (still %q with %d themes registered)", before.Name, len(theme.List()))
        }
    }
}

func TestCtrlLDispatchesModelCommand(t *testing.T) {
    m := newModel(80, 24)
    m = update(m, tea.WindowSizeMsg{Width: 80, Height: 24})
    m = update(m, tea.KeyMsg{Type: tea.KeyCtrlL})
    // Ctrl+L should have appended a sys-history entry (the /model output).
    if len(m.history) == 0 {
        t.Fatalf("Ctrl+L should have appended a sys-history entry, got none")
    }
    if m.history[len(m.history)-1].who != "sys" {
        t.Errorf("last history who = %q, want sys", m.history[len(m.history)-1].who)
    }
}

func TestCtrlUnderscoreTogglesHelp(t *testing.T) {
    m := newModel(80, 24)
    if m.helpOpen {
        t.Fatalf("helpOpen should default to false")
    }
    m = update(m, tea.KeyMsg{Type: tea.KeyCtrlUnderscore})
    if !m.helpOpen {
        t.Fatalf("Ctrl+/ should have toggled helpOpen on")
    }
    m = update(m, tea.KeyMsg{Type: tea.KeyCtrlUnderscore})
    if m.helpOpen {
        t.Fatalf("Ctrl+/ should have toggled helpOpen back off")
    }
}
```

  Add the `theme` import to `model_test.go`.

- [ ] Run: `go test ./internal/channel/cli/...` — confirm all three pass.

- [ ] Lint + commit:

```bash
git add internal/channel/cli/model_test.go
git commit -m "$(cat <<'EOF'
test(cli): cover Ctrl+L/T/_ keybindings via synthetic KeyMsg

Asserts Ctrl+L appends /model output, Ctrl+T advances the active theme,
Ctrl+/ toggles helpOpen on the model.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

### Task 9 — Full repo verification (DoD)

- [ ] Run `go mod tidy` — confirm no diff.
- [ ] Run `go build ./...` — confirm clean build.
- [ ] Run `go test -count=1 ./...` — confirm all tests pass.
- [ ] Run `golangci-lint run ./...` — confirm clean.
- [ ] Run `go run ./cmd/czcli` from a directory without any `.czcli/config.yaml` — confirm it prints the "wrote a default config" message and exits 0 (the existing behavior is preserved; Plan 11 doesn't change startup ergonomics for new users).
- [ ] Manual smoke: from a directory with a valid config, launch `czcli`; type a message split across two lines via `Alt+Enter`; press `Ctrl+/` and confirm the overlay shows; press `Ctrl+T` and confirm a theme change message appears (or the View redraws with new accent colors per Plan 10).

No commit for this task — it's a verification gate before pushing.

---

## Backward compatibility

- Config files using `subagents.dir` (singular) keep working with a one-shot `slog.Warn`. Setting both `dir` and `dirs` honors `dirs` (the modern field wins).
- The default `~/.czcli/{skills,agents,commands}` directories may not exist yet on existing installs; all loaders treat "missing dir" as "empty contributions" (mirrors plugins behavior).
- The textinput→textarea swap preserves the single-line typing UX by default: input starts at height 1 and only grows when the user explicitly inserts a newline via `Alt+Enter`.
- `theme.Cycle` is a no-op when only one theme is registered; tests gate the assertion on `len(theme.List()) > 1` for safety.

## Out of scope (Plan 12 / backlog)

- Creator tools (`create_skill`, `create_agent`, `create_command`) and the `/new` wizard — owned by Plan 12.
- The visual redesign, glamour markdown rendering, and the theme package itself — owned by Plan 10.
- Custom keybinding remap via config — backlog (one set of defaults, per spec).
- Shift+Enter detection via kitty/CSI-u keyboard protocol — backlog (bubbletea v1.3.10 doesn't surface it; Alt+Enter is the documented Plan 11 newline binding).
