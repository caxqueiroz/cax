# czcli TUI Redesign — Shared Contracts (read before plans 10–12)

Pins the cross-cutting Go types for Plans 10/11/12. Augments
`00-shared-contracts.md` and `01-extensibility-contracts.md`; everything in
those still applies.

Spec: `docs/superpowers/specs/2026-05-30-tui-redesign-themes-creator-design.md`

## Dependencies (pinned at impl time)

| Dependency | Use | Plan |
|---|---|---|
| `github.com/charmbracelet/glamour` | markdown rendering for assistant text | 10 |
| `github.com/charmbracelet/bubbles/textarea` | multi-line input | 11 |
| `gopkg.in/yaml.v3` (already pinned) | parse theme YAMLs | 10 |

## Path defaults (Plan 11 migration)

| Config key | Old default | New default |
|---|---|---|
| `skills.dirs` | `[.dive/skills, ~/.dive/skills]` | `[~/.czcli/skills, .czcli/skills]` |
| `subagents.dir` (singular) | `.dive/agents` | replaced by `subagents.dirs: [~/.czcli/agents, .czcli/agents]` |
| `commands.dirs` (new) | — | `[~/.czcli/commands, .czcli/commands]` |
| `cli.theme` (new) | — | `""` (resolves via state.json → `default-dark`) |

Plan 11 migration helper: if YAML has the legacy singular `subagents.dir`, accept it as a single-element `Dirs` and `slog.Warn` the user. No silent breakage.

## Theme — `internal/theme` (Defined in: Plan 10)

```go
package theme

type Theme struct {
	Name string

	// Base text + structure.
	Foreground string
	Dim        string
	Separator  string

	// Semantic.
	Accent string
	OK     string
	Amber  string
	Red    string

	// Conversation.
	UserPrefix    string
	AssistantText string
	SysText       string
	CodeBG        string

	// Gauge.
	GaugeFilled string
	GaugeEmpty  string

	// Glamour markdown style name: "dark"|"light"|"dracula"|"notty"|"auto".
	Markdown string
}

// Lifecycle/registry.
func LoadBuiltins() []*Theme                    // parse embedded builtins/*.yaml
func LoadUserDir(path string) []*Theme          // parse ~/.czcli/themes/*.yaml; per-file errors logged
func Register(t *Theme)
func List() []string                            // sorted names; built-in first then user
func Get(name string) (*Theme, error)

// Active theme accessors. Concurrent reads are safe.
func Active() *Theme
func Set(t *Theme)

// Cycle returns the next theme in registry order (wraps).
func Cycle() *Theme

// Resolve returns the theme to use at startup. Order:
//   1. state.json "theme" field (if file exists and valid)
//   2. configThemeName (from config.cli.theme)
//   3. "default-dark"
func Resolve(stateFile, configThemeName string) *Theme
```

Built-in YAMLs embedded via `//go:embed builtins/*.yaml`. Names shipped:
`default-dark`, `default-light`, `mono`, `dracula`, `nord`, `gruvbox-dark`,
`solarized-dark`, `solarized-light`.

State file `~/.czcli/state.json`:
```json
{ "theme": "dracula" }
```
Atomic write (temp+rename); corruption → log and ignore.

## Markdown — `internal/channel/cli/markdown.go` (Defined in: Plan 10)

```go
// RenderMarkdown renders markdown text using a glamour TermRenderer keyed to
// the active theme's Markdown style. Width is the available column count
// (e.g. viewport.Width). On any glamour error it returns the input verbatim
// (so the TUI never breaks on weird markdown).
func RenderMarkdown(input string, width int) string
```

## User commands loader — `internal/usercmds` (Defined in: Plan 11)

```go
package usercmds

// Load scans dirs (the values of cfg.Commands.Dirs ∪ project-local) for
// *.md files with Claude-Code-style frontmatter and returns one
// plugins.PluginCommand per file. Per-file parse errors are logged + skipped.
func Load(dirs []string) []plugins.PluginCommand
```

The returned slice is merged into `plugins.Contributions.Commands` in
`cmd/czcli/main.go` (loader output appended after plugin Load). The CLI's
slash-command dispatcher (Plan 7's `Contributions.Commands` consumer) renders
them with the same `$ARGUMENTS` expansion.

## Creator — `internal/creator` (Defined in: Plan 12)

```go
package creator

// Reloader is what create tools call after writing a file. The concrete impl
// is *agent.Assistant; injected at agent.Build time so plan 12 doesn't
// import agent directly.
type Reloader interface {
	Rebuild(ctx context.Context) error
}

// Writer materializes files in czcli-namespaced directories.
type Writer struct {
	SkillsDir   string // ~/.czcli/skills (HOME-expanded)
	AgentsDir   string // ~/.czcli/agents
	CommandsDir string // ~/.czcli/commands
}

// WriteSkill writes a SKILL.md under <SkillsDir>/<name>/. Returns abs path.
// Errors if a file already exists at the target and overwrite is false.
func (w Writer) WriteSkill(name, description, body string, overwrite bool) (string, error)

// WriteAgent writes <AgentsDir>/<name>.md.
func (w Writer) WriteAgent(name, description string, tools, disallowedTools []string, body string, overwrite bool) (string, error)

// WriteCommand writes <CommandsDir>/<name>.md (frontmatter: description,
// argument-hint; body may include $ARGUMENTS).
func (w Writer) WriteCommand(name, description, argumentHint, body string, overwrite bool) (string, error)

// Tools registers the three FuncTools on the agent. After a successful write
// each calls r.Rebuild(ctx) before returning the result.
func Tools(w Writer, r Reloader) []dive.Tool
```

Name validation (all writers): `^[a-z0-9][a-z0-9-]{0,63}$`, no path-traversal segments.

## Agent integration — `internal/agent/agent.go` (Plan 12 modifies)

`Build`/`BuildWithMCPInfos`/`Rebuild` get a final `creator.Tools(...)` argument
appended to the assembled tool list. `*Assistant` satisfies `creator.Reloader`
(it already has a `Rebuild` method; just make sure the signature lines up:
`Rebuild(ctx context.Context, …) error`).

To avoid passing every Rebuild arg through the create tools, Plan 12 introduces
a thin shim: `type assistantReloader struct{ a *Assistant; … captured args }`
that satisfies `creator.Reloader` by re-running `a.Rebuild` with the captured
deps. Updated on each main-loop reload so the captured args stay fresh.

## CLI/keybindings — `internal/channel/cli` (Plan 11 modifies)

`textinput.Model` → `bubbles/textarea.Model` (height grows up to 6 lines).
Submit semantics:

- `Enter` → submit (current behavior; textarea also has Enter → newline by default — REMAP it).
- `Alt+Enter` → newline. (Reality check from plan writers: most terminals
  encode Shift+Enter identically to bare Enter, and bubbletea v1.3.10 does
  not expose `KeyShiftEnter`. Detection uses the canonical
  `msg.Alt && msg.Type == tea.KeyEnter` — equivalent to
  `KeyMsg.String() == "alt+enter"`. The spec's mention of Shift+Enter is
  thus aspirational; ship Alt+Enter and document the limitation.)
- `Ctrl+L` → `m.handleCommand("/model")`.
- `Ctrl+R` → `m.handleCommand("/reload")`.
- `Ctrl+T` → `theme.Set(theme.Cycle())`; persist; refresh viewport.
- `Ctrl+/` → toggle a `helpOpen bool` on the model; View renders an overlay
  listing keybinds + slash commands.
- `Ctrl+C`, `Esc` → quit (current).
- `PgUp` / `PgDn` / arrows → viewport scroll (current).

## Signature evolution across plans

These two surfaces grow across Plans 10/11/12. Each plan picks up the shape
the prior plan left:

### `model.handleCommand`

| After | Signature |
|---|---|
| Plan 10 | `func (m *model) handleCommand(line string) (output string, quit bool)` — receiver flipped to pointer so `/theme` can call `m.refreshViewport()`. |
| Plan 12 | `func (m *model) handleCommand(line string) (output string, quit bool, wiz *creator.Wizard)` — third return installs wizard state when `/new` is invoked. |

Plan 11 does not change the signature.

### `agent.Build` / `BuildWithMCPInfos` / `Rebuild`

The arg list grows by one (`creatorTools []dive.Tool` at the END) in Plan 12.
Plans 10/11 do not change the agent signatures.

Final shape after Plan 12:

```go
func Build(ctx, cfg, store, model,
    skillRes, mcpTools, creatorTools) (*Assistant, error)

func BuildWithMCPInfos(ctx, cfg, store, model,
    skillRes, mcpTools, mcpInfos, lspTools, lspInfos,
    hooksDisp, creatorTools) (*Assistant, error)

func (a *Assistant) Rebuild(ctx, cfg,
    skillRes, mcpTools, mcpInfos, lspTools, lspInfos,
    hooksDisp, creatorTools) error
```

## Conventions

- Go 1.25 module, `go test ./...`, `golangci-lint run ./...` clean, `go mod tidy` no-op.
- TDD per-step. Trailer `Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>` on every commit.
- Stay on `feat/tui-redesign` branch for these plans.
