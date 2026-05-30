# czcli TUI Redesign — Refined Chrome, Themes, "Just-Ask" Creator

**Date:** 2026-05-30
**Status:** Approved design, ready for implementation planning

## Summary

Three coordinated changes to bring the TUI from "functional but ugly" to a
deliberate, themed, extensible experience inspired by [pi.dev](https://pi.dev):

1. **Visual redesign — "refined chrome".** Same 4-region layout we have today, but
   redesigned: drop heavy backgrounds, add a global 2-space indent, replace borders
   with thin `─` separators, render assistant replies as markdown (headings, code
   blocks, lists) via [`glamour`](https://github.com/charmbracelet/glamour).
2. **Theming system.** A `theme.Theme` struct with named colors, 8 built-in themes
   (embedded YAML), live switching via `/theme`, user themes at
   `~/.czcli/themes/*.yaml`, persisted active theme.
3. **"Just-ask" creator.** Three new agent tools (`create_skill` / `create_agent` /
   `create_command`) plus a `/new` slash command. Files live under czcli's own
   directories, hot-reload via `agent.Rebuild`.

Plus a small set of pi.dev-inspired keybindings (`Shift+Enter` newline, `Ctrl+L`
model picker, `Ctrl+R` reload, `Ctrl+T` cycle themes, `Ctrl+/` help overlay).

## Goals

- Make the TUI look deliberately designed; eliminate the "shitty job" reaction.
- Adapt to light *and* dark terminals out of the box (no theme set).
- Let users theme everything without recompiling; ship a sensible default set.
- Make creating a new skill/agent/command a 30-second action — by asking in
  natural language or via a single slash command.
- Hot-reload everywhere: change a theme, write a new skill, `/reload` — no restart.

## Non-goals

- Changing terminal fonts (terminal-emulator-controlled; not addressable from inside czcli).
- Sidebar / session-tree layouts (separate scope, deferred to backlog if wanted).
- Custom keybinding remap via config (one set of defaults; backlog).
- Nerd-Font icon mode (deferred to backlog).
- Built-in commands marketplace (just files in dirs; backlog).

## Key decisions

| Decision | Choice |
|---|---|
| Layout | Refined chrome (same 4 regions, redesigned) |
| Markdown renderer | `github.com/charmbracelet/glamour` |
| Themes | 8 built-in YAML themes (embedded) + user YAML themes in `~/.czcli/themes/` |
| Live theme switch | `/theme <name>`, `/theme list`, `Ctrl+T` cycles |
| Active-theme persistence | `~/.czcli/state.json` (separate from `config.yaml`) |
| Auto light/dark | `default` family uses lipgloss `AdaptiveColor`; explicit themes override |
| Create-flow | Three FuncTools + `/new` wizard, sharing writer code |
| Skill dir | `~/.czcli/skills/`, `.czcli/skills/` (project-local) |
| Agent dir | `~/.czcli/agents/`, `.czcli/agents/` |
| Command dir | `~/.czcli/commands/`, `.czcli/commands/` |
| Multi-line input | `Shift+Enter` or `Alt+Enter` newline; `Enter` sends |

## Visual redesign

### Target look

```
  opus ✓                  hist 6.1k/8k ▓▓▓░ 76% ⚠
  ─────────────────────────────────────────────────

  ❯ explain Go embedding

  Go embedding lets a struct include another struct
  as a field — exposing its fields and methods at
  the top level:

      type Reader struct{ src io.Reader }
      type Logged struct{ Reader; tag string }

  Methods on Reader can be called directly on a
  Logged instance.

  ─────────────────────────────────────────────────
   1d 124k    1w 812k    1m 3.2M    mem 18MB    🔧 8
  ─────────────────────────────────────────────────

  ❯ _
```

### Rules

- **Global 2-space indent.** Nothing touches the terminal edge.
- **Thin separators.** Single `─` row in the theme's `Separator` color between regions, instead of full borders or heavy backgrounds.
- **No background fills.** Bottom bar text inherits the terminal background so it works in light *and* dark terminals. Color comes from foreground styling.
- **Markdown rendering for assistant output.** `glamour.NewTermRenderer` with a theme-mapped style (`markdown:` field). User messages and system notices stay raw text. Glamour handles wrapping; we don't wrap on top.
- **Spinner unchanged.** `working…` while waiting for the first delta.
- **User prefix** stays `❯` in `Accent`; assistant text has no prefix; blank line after each turn (already shipped).
- **Status row redesign.** Window markers (`1d`/`1w`/`1m`) in `Accent`; values bold in `Foreground`; dividers `│` in `Dim`. No background band.

### Layout math (per `WindowSizeMsg`)

```
top bar         1
sep             1
viewport        msg.Height - 7
sep             1
bottom bar      1
sep             1
input pad       1
input           1
                ─────
                msg.Height
```

## Theming system

### `internal/theme` (new package)

```go
package theme

type Theme struct {
    Name string

    // Base text and structural colors.
    Foreground string
    Dim        string
    Separator  string

    // Semantic colors.
    Accent string
    OK     string
    Amber  string
    Red    string

    // Conversation-specific.
    UserPrefix    string
    AssistantText string
    SysText       string
    CodeBG        string

    // Gauge.
    GaugeFilled string
    GaugeEmpty  string

    // Markdown rendering: glamour style name. One of glamour's built-ins
    // (dark, light, dracula, notty, ...) or "auto" to derive from terminal.
    Markdown string
}

// Active returns the currently active theme. Safe for concurrent reads;
// writes go through Set under an internal mutex.
func Active() *Theme
func Set(t *Theme)
func List() []string                  // built-in + user
func Get(name string) (*Theme, error) // by name
```

### Built-in themes (embedded YAML)

```
internal/theme/builtins/
  default-dark.yaml
  default-light.yaml
  mono.yaml
  dracula.yaml
  nord.yaml
  gruvbox-dark.yaml
  solarized-dark.yaml
  solarized-light.yaml
```

Embedded via `//go:embed builtins/*.yaml` so they ship in the binary.

`default-dark` and `default-light` use lipgloss `AdaptiveColor{Light, Dark}` for the
"adaptive" elements so they work in either terminal mode. Explicit themes
(dracula etc.) use fixed hex values.

### User themes

`~/.czcli/themes/<name>.yaml`. Same schema as built-ins. Loaded at startup; `/reload`
or `/theme` re-reads the dir so editing a theme file and re-applying is instant.

### Live switching

- `/theme list` — print names + which is active.
- `/theme <name>` — set as active; render with new theme on next frame.
- `Ctrl+T` — cycle to next theme in registry order (wraps).

### Persistence

```json
// ~/.czcli/state.json
{ "theme": "dracula" }
```

Separate from `config.yaml` so an interactive `/theme` swap doesn't churn user
config. On startup, resolution order: `state.json` → `config.cli.theme` → `default-dark`.

### Lipgloss integration

Style helpers in `internal/channel/cli/view.go` switch from package-level vars to
functions that read `theme.Active()`. Rendering recomputes per frame (cheap).

## "Just-ask" creator

### Tools registered on the agent

```go
// internal/creator

type Writer interface {
    WriteSkill(name, description, body string) (path string, err error)
    WriteAgent(name, description string, tools []string, body string) (path string, err error)
    WriteCommand(name, description, argumentHint, body string) (path string, err error)
}

type Reloader interface { Rebuild(ctx context.Context) error }

// Tools is the slice the agent's tool registry merges in.
func Tools(w Writer, r Reloader) []dive.Tool
```

The three `dive.FuncTool`s each:
1. Validate `name` (kebab-case, no path traversal).
2. Use the `Writer` to materialize the file in czcli's dir.
3. Call `Reloader.Rebuild(ctx)`.
4. Return a `dive.ToolResult` with the written path so the model can confirm.

### File formats

`~/.czcli/skills/<name>/SKILL.md`:
```markdown
---
name: <name>
description: <description>
---

<body>
```

`~/.czcli/agents/<name>.md`:
```markdown
---
description: <description>
tools:                        # optional
  - Read
  - Glob
---

<body>
```

`~/.czcli/commands/<name>.md`:
```markdown
---
description: <description>
argument-hint: <argument-hint> # optional
---

<body, may include $ARGUMENTS>
```

### `/new` slash command

`/new skill|agent|command [name]` → opens an inline wizard: prompts for
description, optional tools (for skill/agent), body. Sends the collected fields
straight to the matching `Writer` method.

### Permissions

Writes go through the same `PreToolUse` permission gate as the existing Write/Edit
tools (`tools.ConfirmDialog`), so the user confirms before a file is written
unless `tools.require_confirm: false`.

### Conflict handling

If a file already exists at the target path: tool returns an error suggesting
either a different name or `--overwrite` (a tool option) — explicit, no surprises.

### User commands loader

`~/.czcli/commands/*.md` and `.czcli/commands/*.md` are scanned on startup and on
every `Rebuild`. Parsed into `cli.PluginCommand`-shaped entries (the same shape
plugins already produce) and merged into the CLI's slash-command set. Calling
`/<command-name>` runs the prompt body with `$ARGUMENTS` expanded (Plan 7's
existing `ExpandArguments`).

## Keybindings

| Keys | Action |
|---|---|
| `Enter` | Send |
| `Shift+Enter` / `Alt+Enter` | Insert newline (textinput → textarea) |
| `Ctrl+L` | Show `/model` picker |
| `Ctrl+R` | Run `/reload` |
| `Ctrl+T` | Cycle themes |
| `Ctrl+/` | Open help overlay (keybinds + slash commands) |
| `Ctrl+C` / `Esc` | Quit |
| `PgUp` / `PgDn` / arrows | Scroll viewport |

Multi-line input requires swapping `bubbles/textinput` for `bubbles/textarea`
with a configurable height (1–6 visible rows; grows as the user types,
collapses when sent).

## Config additions

`config.Config`:
```go
type CLIConfig struct {
    Theme string `yaml:"theme"` // initial theme; falls back to state.json then default-dark
}
type CommandsConfig struct {
    Enabled bool     `yaml:"enabled"`
    Dirs    []string `yaml:"dirs"`
}
// existing:
//   SubagentsConfig becomes plural: Dirs []string  (was singular Dir)
```

Defaults:
- `skills.dirs`: `[~/.czcli/skills, .czcli/skills]` (was `[.dive/skills, ~/.dive/skills]`)
- `subagents.dirs`: `[~/.czcli/agents, .czcli/agents]` (was `.dive/agents`)
- `plugins.dirs`: `[~/.czcli/plugins, .czcli/plugins]` (unchanged)
- `commands.dirs`: `[~/.czcli/commands, .czcli/commands]` (new)
- `cli.theme`: `""` (resolve from state.json then default-dark)

## Files affected

```
internal/theme/                 NEW    theme.go, registry.go, loader.go, builtins.go (embed), builtins/*.yaml
internal/creator/               NEW    writer.go (writes files), tools.go (3 FuncTools), wizard.go (/new wizard state)
internal/channel/cli/markdown.go NEW    glamour wrapper keyed to theme.Active().Markdown
internal/channel/cli/view.go        MODIFY  theme-driven styles; refined layout; glamour render for assistant
internal/channel/cli/model.go       MODIFY  textinput → textarea for multi-line; new keybindings
internal/channel/cli/commands.go    MODIFY  /theme, /new, /reload, Ctrl-key handlers, help overlay
internal/channel/cli/state.go    NEW       read/write ~/.czcli/state.json
internal/agent/agent.go             MODIFY  inject creator tools via Build/Rebuild; pass Reloader
internal/config/config.go           MODIFY  CLIConfig, CommandsConfig; plural Subagents.Dirs; updated defaults
internal/config/example.yaml        MODIFY  reflect new paths + cli.theme placeholder
cmd/czcli/main.go                   MODIFY  load active theme on startup; scan commands dirs; wire Reloader into agent.Build
internal/config/migrate.go      NEW       one-shot migration: if user config has `subagents.dir` (singular) accept it and warn
```

## Data flow

### Startup

```
config.Load
  → theme.LoadBuiltins + theme.LoadUserDir(~/.czcli/themes)
  → theme.Resolve(state.json | config.cli.theme | "default-dark") → theme.Set(active)
  → plugins.Manager.Load → contributions
  → skills.Load(cfg.Skills.Dirs ∪ contributions.SkillDirs)
  → mcp.Connect, lsp.New
  → user commands loader: ~/.czcli/commands + ./.czcli/commands → contributions.Commands
  → hooks.Load
  → creator.Tools(writer, reloader) → injected via agent.Build
  → cli.New(...).Start(ctx, handle, status)
```

### Theme change (`/theme dracula` or `Ctrl+T`)

```
cmd model: handleCommand("/theme dracula")
  → theme.Get("dracula") → theme.Set(it)
  → state.json updated
  → next render uses new theme (styles read theme.Active())
```

### Create a skill (natural language: "create a skill that explains Go embedding")

```
agent.Handle
  → llm decides → calls create_skill(name="explain-go-embedding", description, body)
  → PreToolUse hook: ConfirmDialog (unless require_confirm: false) → user approves
  → creator.WriteSkill writes ~/.czcli/skills/explain-go-embedding/SKILL.md
  → creator.Reloader.Rebuild(ctx) → agent.Rebuild → skills.Load picks up the new file
  → tool result: "wrote ~/.czcli/skills/explain-go-embedding/SKILL.md; reloaded"
```

### `/new skill foo`

```
cmd model: enter wizard state
  → collects description, body (textarea input)
  → on confirm: call creator.WriteSkill + Reloader.Rebuild
```

## Error handling

- Theme not found → `/theme` reports the error inline; active theme unchanged.
- Theme YAML invalid → log at startup, skip that theme; built-ins always work as fallback.
- Creator file already exists → tool returns "exists; pick another name or pass overwrite=true".
- Glamour render error on assistant text → fall back to plain wrapped text + log debug.
- Reload error → reported via `/reload` output; agent keeps running with prior contributions.
- `state.json` corruption → log + fall back to config-resolved theme.

## Testing

- `theme`: load each built-in YAML, assert all required fields parse; `theme.Set` swaps active; `theme.Get(unknown)` errors.
- `markdown`: render a fixture markdown string with each built-in theme; assert no panic and non-empty output.
- `creator`: `t.TempDir()` HOME; write skill/agent/command; assert file contents and that a fake `Reloader` is called; conflict path errors.
- `cli` view: drive Update with synthetic keybind events (`Shift+Enter` adds a newline; `Ctrl+T` toggles theme via a stub `theme.Cycler`); View() asserts theme accent color appears in user prefix and gauge.
- `cli` commands: `/theme list`, `/theme dracula`, `/reload`, `/new skill foo` happy paths.
- `config` migration: legacy `subagents.dir` parses into the new `Dirs` (single-element slice) with a `slog.Warn`.

## Open items to verify during implementation

1. `glamour` width handling: ensure markdown output respects `viewport.Width` and doesn't add trailing blank lines.
2. `bubbles/textarea` API at the pinned version: how multi-line submission interacts with Enter vs Shift+Enter; how cursor/scroll behaves with a small max height (e.g. 6 rows).
3. Whether `lipgloss.AdaptiveColor` detection happens at first render or once per program — relevant for SSH scenarios where `$TERM` may mislead.
4. Atomic write semantics for `state.json` (temp + rename) — mirror `plugins.json`.
5. `/reload` interaction with in-flight turns: queue the rebuild until current turn finishes (mirror existing `Assistant.Rebuild` mutex behavior).

## Backward compatibility

- Greenfield project — clean break on default dirs. The migration helper accepts legacy
  `subagents.dir` (singular) so an explicitly-set config keeps working with a warning.
- Plugins still expose their own `commands/`, `skills/`, `agents/`; nothing changes for plugin
  authors. Plugin contributions and user-level files merge into the same registries.

## Out of scope (deferred to backlog)

- Custom keybinding remap.
- Sidebar / session tree.
- Nerd-Font glyph variants.
- Theme marketplace (`/theme install <git-url>`).
- Streaming-text typewriter animation.
- Inline image rendering (`kitty`/`iTerm` protocols).
