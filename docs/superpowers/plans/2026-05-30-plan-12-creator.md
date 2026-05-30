# Plan 12: Creator — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build `internal/creator`, the "just-ask" workflow that lets the user create a new skill, agent, or command — either by asking the model in natural language (it calls one of three new FuncTools: `create_skill` / `create_agent` / `create_command`) or via the `/new skill|agent|command [name]` wizard. Files are written under `~/.czcli/{skills,agents,commands}` with Claude-Code-compatible frontmatter, then `Reloader.Rebuild(ctx)` is called so the running agent picks the new file up on the next turn. Plan 12 also threads the new tool slice through `agent.Build` / `agent.BuildWithMCPInfos` / `agent.Rebuild` (adding a final `creatorTools []dive.Tool` parameter) and wires `cmd/czcli/main.go` with an `assistantReloader` shim that captures every Rebuild dependency so the create tools can rebuild without leaking those dependencies into the `creator` package.

**Architecture:** `creator.Writer` is a tiny value type holding the three target directories (`SkillsDir` / `AgentsDir` / `CommandsDir`, HOME-expanded by the caller). Each `Write*` method validates the name against `^[a-z0-9][a-z0-9-]{0,63}$` (no `..` segments, no path separators), refuses to overwrite an existing file unless `overwrite=true`, then materializes the YAML-frontmatter + body via a temp-file + rename atomic write (mirroring `internal/plugins/state.go`). `creator.Tools(w, r)` returns three `dive.FuncTool`s with typed input structs (`createSkillInput` / `createAgentInput` / `createCommandInput`); on a successful write each calls `r.Rebuild(ctx)` and returns a `dive.NewToolResultText` carrying the absolute path. `creator.Reloader` is a one-method interface — the concrete implementation (`assistantReloader`) lives in `cmd/czcli/main.go` and captures `cfg`, `skillRes`, `mcpTools`, `mcpInfos`, `lspTools`, `lspInfos`, `hooksDisp` so it can call `assistant.Rebuild` with the full v1.7 argument list. The reloader's captured args are refreshed inside `pluginAdapter.Rebuild` and the initial startup, so a `Rebuild` triggered by a create tool always uses the latest plugin contributions. `cmd/czcli/main.go` builds the reloader once, constructs `creator.Tools(writer, reloader)`, and threads the resulting `[]dive.Tool` through every `Build*`/`Rebuild` call site. `internal/channel/cli/commands.go` gains `/new skill|agent|command [name]`; the wizard adds a `createWizard` field to `model` (nil when not active) that routes textarea input through `description → tools → body → confirm` steps and on confirm calls a CLI-injected `creatorBackend` (interface satisfied in main by a thin adapter over `creator.Writer` + `creator.Reloader`). The wizard state is opt-in (nil by default) so existing Update tests keep passing.

**Tech Stack:** Go 1.25, `github.com/deepnoodle-ai/dive` v1.7.0 (`FuncTool`, `NewToolResultText`, `NewToolResultError`, `Tool`), `gopkg.in/yaml.v3` (frontmatter output), `os` + `path/filepath` (atomic write + name validation), `log/slog` (best-effort logging), `regexp` (name validation), `github.com/caxqueiroz/czcli/internal/agent` (Build/BuildWithMCPInfos/Rebuild — modified), `github.com/caxqueiroz/czcli/internal/plugins` (parser shapes — write output must round-trip through `splitFrontmatter` + `cmdFrontmatter`), `github.com/charmbracelet/bubbletea` (synthetic `tea.KeyMsg` events drive wizard tests).

---

## Research notes (authoritative — verified 2026-05-30)

- **`dive.FuncTool` signature.** Verified at `$(go env GOMODCACHE)/github.com/deepnoodle-ai/dive@v1.7.0/tool.go:464`:
  ```go
  func FuncTool[T any](name, description string, fn func(ctx context.Context, input T) (*ToolResult, error), opts ...FuncToolOption) Tool
  ```
  The existing `internal/tools/recall.go` uses `T = *recallInput` so the closure receives a `*recallInput` — Plan 12 mirrors that (pointer-to-struct input). `dive.NewToolResultText` and `dive.NewToolResultError` return `*dive.ToolResult` for success / error tool results respectively.

- **Existing `agent.Rebuild` signature (Plans 8 + 9 final).** Verified at `internal/agent/agent.go:169-194`:
  ```go
  func (a *Assistant) Rebuild(
      ctx context.Context,
      cfg *config.Config,
      skillRes *skills.LoadResult,
      mcpTools []dive.Tool,
      mcpInfos []mcp.ServerInfo,
      lspTools []dive.Tool,
      lspInfos []lsp.ServerInfo,
      hooksDisp *hooks.Dispatcher,
  ) error
  ```
  Plan 11 (per `02-tui-redesign-contracts.md`) does **not** add new parameters to `Rebuild`; it adds an LSP-tied user-commands loader that flows through `plugins.Contributions.Commands`. Plan 12 therefore appends exactly one new parameter (`creatorTools []dive.Tool`) at the end of `Build` / `BuildWithMCPInfos` / `Rebuild`. The `assistantReloader` captures the seven non-creator args so `Rebuild(ctx)` (the single-arg `Reloader` method) can fan out to the full eight-arg `Assistant.Rebuild`.

- **Atomic write pattern.** Verified at `internal/plugins/state.go:38-67`: `os.MkdirAll(dir, 0o755)` → `os.CreateTemp(dir, ".prefix.*")` → write data → `Close()` → `os.Rename(tmpName, path)`; on any error along the way, `os.Remove(tmpName)`. Plan 12 reuses this exact sequence in `creator.writeAtomic` (private helper shared by all three writers).

- **Frontmatter round-trip with `internal/plugins`.** Verified at `internal/plugins/manifest.go:392-412`: `splitFrontmatter` recognizes leading `"---\n"` then looks for `"\n---\n"` or trailing `"\n---"` as the closing fence. The body is the bytes after the closing fence (trimmed of leading newline). `cmdFrontmatter` decodes `description: string` and `argument-hint: string`. Plan 12's `WriteCommand` MUST emit the closing fence as `"\n---\n"` (not just `"---"`) so the parser strips it cleanly. Skill / agent files use the same frontmatter delimiter convention; the skill loader (dive's `skill.Loader`) accepts standard `name:` / `description:` keys, and agent files are consumed by dive's subagent loader which accepts `description:` and `tools:`.

- **Name validation.** Per `02-tui-redesign-contracts.md` §Creator: `^[a-z0-9][a-z0-9-]{0,63}$`, no `..` segments. The regex is sufficient (it disallows `.` so `..` can't appear), but Plan 12 ALSO rejects names containing `/` or `\` or starting with `-` for defense in depth, and uses `filepath.Clean` + `strings.Contains(clean, "..")` as a belt-and-suspenders check before any disk operation.

- **Wizard isolation strategy.** The existing `model.Update` is a value-receiver method that returns `(tea.Model, tea.Cmd)`; the wizard adds one optional pointer field `wizard *createWizard` on `model`. When `wizard == nil` (the default), Update behaves identically — existing tests keep passing. When the wizard is active, `Update` checks `m.wizard != nil` at the top of the `tea.KeyMsg` branch and routes input through `m.wizard.step(msg)` instead of `m.input.Update(msg)`. Tests for the wizard drive a fresh model with `m.wizard = &createWizard{kind: "skill"}` and synthesize key events with `tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("description text")}` + `tea.KeyMsg{Type: tea.KeyEnter}` to advance steps. If the wizard can't be cleanly bolted on without breaking model tests, the plan author stops at Task 8 and reports — Tasks 1–7 (writer + tools + agent + main wiring) stand alone and are valuable on their own.

- **dive.Tool interface.** `dive.Tool` only exposes `Name()` and metadata; the `creator.Tools` slice is appended to the existing `a.tools` chain in `BuildWithMCPInfos` exactly the way `mcpTools` and `lspTools` are appended today (`agent.go:113-116`).

---

## File Structure

```
internal/creator/
├── writer.go            # Writer struct + WriteSkill / WriteAgent / WriteCommand + atomic helper (Task 1)
├── writer_test.go       # Name validation table, frontmatter round-trip, conflict path errors, atomic write (Tasks 1–2)
├── reloader.go          # Reloader interface (Task 3)
├── tools.go             # Tools(w Writer, r Reloader) []dive.Tool: 3 FuncTools (Task 4)
├── tools_test.go        # FakeReloader + per-tool happy paths + reloader-called-once assertions (Task 4)
└── wizard.go            # createWizard state machine: kind, step, fields (Task 8 — optional)

internal/agent/
└── agent.go             # MODIFY: append creatorTools []dive.Tool param to Build / BuildWithMCPInfos / Rebuild (Task 5)

internal/channel/cli/
├── model.go             # MODIFY: add optional wizard *createWizard field; route input through wizard when non-nil (Task 8)
├── commands.go          # MODIFY: add /new dispatch + help additive (Tasks 7, 9)
├── commands_test.go     # MODIFY: /new wizard happy-path test (Task 8)
└── cli.go               # MODIFY: WithCreator option wiring the creatorBackend (Task 9)

cmd/czcli/
└── main.go              # MODIFY: build Writer + assistantReloader + creator.Tools; thread through Build/Rebuild;
                         #         build creatorAdapter implementing cli.creatorBackend; pass to cli.WithCreator (Task 6, 9)
```

Dependencies assumed already present (do NOT define here):
- Plan 10 (`internal/theme` + `internal/channel/cli/markdown.go`).
- Plan 11 (`internal/usercmds`, `bubbles/textarea` swap, `Ctrl+/` help overlay, `subagents.Dirs` plural).
- Contracts from `02-tui-redesign-contracts.md` §Creator — used VERBATIM below.

Contract signatures (verbatim from `02-tui-redesign-contracts.md` §Creator):

```go
type Reloader interface {
    Rebuild(ctx context.Context) error
}

type Writer struct {
    SkillsDir   string
    AgentsDir   string
    CommandsDir string
}

func (w Writer) WriteSkill(name, description, body string, overwrite bool) (string, error)
func (w Writer) WriteAgent(name, description string, tools, disallowedTools []string, body string, overwrite bool) (string, error)
func (w Writer) WriteCommand(name, description, argumentHint, body string, overwrite bool) (string, error)

func Tools(w Writer, r Reloader) []dive.Tool
```

---

### Task 1 — Writer: name validation + atomic write helper + WriteSkill

**Files:** `internal/creator/writer.go`, `internal/creator/writer_test.go`

- [ ] Write the FAILING test `internal/creator/writer_test.go` covering name validation, the skill file frontmatter shape, atomic write under a `t.TempDir()` HOME, and the conflict path:

```go
package creator

import (
    "os"
    "path/filepath"
    "strings"
    "testing"

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
    want := filepath.Join(w.SkillsDir, "explain-go-embedding", "SKILL.md")
    if path != want {
        t.Fatalf("path = %q want %q", path, want)
    }
    data, err := os.ReadFile(path)
    if err != nil {
        t.Fatalf("read written file: %v", err)
    }
    if !strings.HasPrefix(string(data), "---\n") {
        t.Fatalf("missing frontmatter open; got: %q", string(data[:min(40, len(data))]))
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
    // Sanity: the file must split cleanly into frontmatter + body via the
    // same delimiter the plugins parser uses (proves our closing fence shape).
    data, _ := os.ReadFile(path)
    fmBytes, body := plugins.SplitFrontmatterForTest(data)
    if len(fmBytes) == 0 {
        t.Fatalf("frontmatter empty; output not parseable: %s", string(data))
    }
    if !strings.HasPrefix(string(body), "body line") {
        t.Fatalf("body mis-split: %q", string(body))
    }
}

func min(a, b int) int { if a < b { return a }; return b }
```

Note: `plugins.SplitFrontmatterForTest` is added by this task as a tiny exported test helper in a new `internal/plugins/export_test.go`-style file, since `splitFrontmatter` is private. We expose it via a separate file `internal/plugins/frontmatter_export.go` with a deliberate `// Exported for cross-package round-trip tests in internal/creator.` comment.

- [ ] Run `go test ./internal/creator/...` → FAIL (package does not exist).

- [ ] Minimal impl `internal/creator/writer.go`:

```go
// Package creator implements the "just-ask" workflow for creating skills,
// agents, and slash commands. Writer materializes Claude-Code-compatible
// markdown files under czcli's namespaced directories; Tools wraps the three
// writers in dive FuncTools so the agent can call them in response to a
// natural-language request. After every successful write the package calls
// Reloader.Rebuild so the running agent picks up the new contribution on the
// next turn without restart.
package creator

import (
    "errors"
    "fmt"
    "os"
    "path/filepath"
    "regexp"
    "strings"
)

// nameRe enforces the contract in 02-tui-redesign-contracts.md §Creator:
// lower-kebab, 1-64 chars, must start with [a-z0-9].
var nameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,63}$`)

// validateName rejects anything outside the contract regex plus belt-and-
// suspenders rejection of path-separators, parent-dir traversal and dot
// segments. (The regex already excludes '/', '\', '.', but we keep the
// explicit checks so any future regex change doesn't silently open a hole.)
func validateName(name string) error {
    if name == "" {
        return errors.New("name is required")
    }
    if !nameRe.MatchString(name) {
        return fmt.Errorf("invalid name %q: must match %s", name, nameRe.String())
    }
    if strings.ContainsAny(name, "/\\") {
        return fmt.Errorf("invalid name %q: contains path separator", name)
    }
    clean := filepath.Clean(name)
    if strings.Contains(clean, "..") || clean != name {
        return fmt.Errorf("invalid name %q: path traversal not allowed", name)
    }
    return nil
}

// Writer materializes files under czcli's namespaced directories. The three
// directory paths are caller-resolved (HOME-expanded) so the package itself
// performs no I/O outside of the configured roots.
type Writer struct {
    SkillsDir   string
    AgentsDir   string
    CommandsDir string
}

// WriteSkill writes <SkillsDir>/<name>/SKILL.md with the standard dive skill
// frontmatter (name + description). Returns the absolute path written.
// Errors if the file already exists and overwrite is false.
func (w Writer) WriteSkill(name, description, body string, overwrite bool) (string, error) {
    if err := validateName(name); err != nil {
        return "", fmt.Errorf("WriteSkill: %w", err)
    }
    if w.SkillsDir == "" {
        return "", errors.New("WriteSkill: SkillsDir not configured")
    }
    dir := filepath.Join(w.SkillsDir, name)
    path := filepath.Join(dir, "SKILL.md")
    var sb strings.Builder
    sb.WriteString("---\n")
    fmt.Fprintf(&sb, "name: %s\n", name)
    fmt.Fprintf(&sb, "description: %s\n", oneLine(description))
    sb.WriteString("---\n\n")
    sb.WriteString(ensureTrailingNewline(body))
    if err := writeAtomic(dir, path, []byte(sb.String()), overwrite); err != nil {
        return "", fmt.Errorf("WriteSkill: %w", err)
    }
    abs, err := filepath.Abs(path)
    if err != nil {
        return path, nil
    }
    return abs, nil
}

// WriteAgent writes <AgentsDir>/<name>.md with `description:`, optional
// `tools:` and optional `disallowed-tools:` keys plus the markdown body.
// Returns the absolute path written.
func (w Writer) WriteAgent(name, description string, tools, disallowedTools []string, body string, overwrite bool) (string, error) {
    if err := validateName(name); err != nil {
        return "", fmt.Errorf("WriteAgent: %w", err)
    }
    if w.AgentsDir == "" {
        return "", errors.New("WriteAgent: AgentsDir not configured")
    }
    path := filepath.Join(w.AgentsDir, name+".md")
    var sb strings.Builder
    sb.WriteString("---\n")
    fmt.Fprintf(&sb, "description: %s\n", oneLine(description))
    if len(tools) > 0 {
        sb.WriteString("tools:\n")
        for _, t := range tools {
            fmt.Fprintf(&sb, "  - %s\n", t)
        }
    }
    if len(disallowedTools) > 0 {
        sb.WriteString("disallowed-tools:\n")
        for _, t := range disallowedTools {
            fmt.Fprintf(&sb, "  - %s\n", t)
        }
    }
    sb.WriteString("---\n\n")
    sb.WriteString(ensureTrailingNewline(body))
    if err := writeAtomic(w.AgentsDir, path, []byte(sb.String()), overwrite); err != nil {
        return "", fmt.Errorf("WriteAgent: %w", err)
    }
    abs, err := filepath.Abs(path)
    if err != nil {
        return path, nil
    }
    return abs, nil
}

// WriteCommand writes <CommandsDir>/<name>.md with `description:` and optional
// `argument-hint:` keys. The body may include the literal $ARGUMENTS token
// which the CLI command dispatcher expands at invocation. Returns the
// absolute path written.
func (w Writer) WriteCommand(name, description, argumentHint, body string, overwrite bool) (string, error) {
    if err := validateName(name); err != nil {
        return "", fmt.Errorf("WriteCommand: %w", err)
    }
    if w.CommandsDir == "" {
        return "", errors.New("WriteCommand: CommandsDir not configured")
    }
    path := filepath.Join(w.CommandsDir, name+".md")
    var sb strings.Builder
    sb.WriteString("---\n")
    fmt.Fprintf(&sb, "description: %s\n", oneLine(description))
    if strings.TrimSpace(argumentHint) != "" {
        fmt.Fprintf(&sb, "argument-hint: %s\n", oneLine(argumentHint))
    }
    sb.WriteString("---\n\n")
    sb.WriteString(ensureTrailingNewline(body))
    if err := writeAtomic(w.CommandsDir, path, []byte(sb.String()), overwrite); err != nil {
        return "", fmt.Errorf("WriteCommand: %w", err)
    }
    abs, err := filepath.Abs(path)
    if err != nil {
        return path, nil
    }
    return abs, nil
}

// writeAtomic mirrors internal/plugins/state.go writeState: create parent dir
// (0755), write to a temp file in the same dir, rename over the target. If
// overwrite is false and the target already exists, returns "already exists".
func writeAtomic(dir, path string, data []byte, overwrite bool) error {
    if !overwrite {
        if _, err := os.Stat(path); err == nil {
            return fmt.Errorf("file %s already exists (pass overwrite=true to replace)", path)
        }
    }
    if err := os.MkdirAll(dir, 0o755); err != nil {
        return fmt.Errorf("mkdir %s: %w", dir, err)
    }
    tmp, err := os.CreateTemp(dir, ".creator.*.md")
    if err != nil {
        return fmt.Errorf("temp file: %w", err)
    }
    tmpName := tmp.Name()
    if _, err := tmp.Write(data); err != nil {
        _ = tmp.Close()
        _ = os.Remove(tmpName)
        return fmt.Errorf("write temp: %w", err)
    }
    if err := tmp.Close(); err != nil {
        _ = os.Remove(tmpName)
        return fmt.Errorf("close temp: %w", err)
    }
    if err := os.Rename(tmpName, path); err != nil {
        _ = os.Remove(tmpName)
        return fmt.Errorf("rename to %s: %w", path, err)
    }
    return nil
}

// oneLine collapses interior newlines so a multi-line description doesn't
// blow up the single-line YAML scalar. Leading/trailing whitespace trimmed.
func oneLine(s string) string {
    s = strings.TrimSpace(s)
    s = strings.ReplaceAll(s, "\r\n", " ")
    s = strings.ReplaceAll(s, "\n", " ")
    return s
}

// ensureTrailingNewline keeps file endings POSIX-correct so editors and the
// plugins parser don't choke on a missing terminal newline.
func ensureTrailingNewline(s string) string {
    if s == "" {
        return ""
    }
    if !strings.HasSuffix(s, "\n") {
        return s + "\n"
    }
    return s
}
```

- [ ] Add `internal/plugins/frontmatter_export.go` (tiny export shim, lives in plugins package because `splitFrontmatter` is unexported and Plan 12 needs round-trip parse coverage from `internal/creator`):

```go
package plugins

// SplitFrontmatterForTest is the cross-package entry point that exposes
// splitFrontmatter to internal/creator's round-trip tests. Behavior identical
// to splitFrontmatter; this file exists to keep the public surface tight
// (callers outside tests should keep going through ReadCommands).
func SplitFrontmatterForTest(data []byte) (yamlSrc, body []byte) {
    return splitFrontmatter(data)
}
```

- [ ] Run `go test ./internal/creator/... ./internal/plugins/...` → PASS.

- [ ] Commit:
```
git add internal/creator/writer.go internal/creator/writer_test.go internal/plugins/frontmatter_export.go
git commit -m "$(cat <<'EOF'
feat(creator): add Writer with WriteSkill + name validation + atomic write

Mirrors internal/plugins/state.go atomic-write pattern. Names enforce
^[a-z0-9][a-z0-9-]{0,63}$ with belt-and-suspenders path-traversal
checks. Conflict path errors with "already exists" unless overwrite=true.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

### Task 2 — Writer: WriteAgent + WriteCommand round-trip tests

**Files:** `internal/creator/writer_test.go` (extend)

WriteAgent and WriteCommand are already implemented in Task 1 (the impl is small enough that splitting writers across two tasks would inflate noise without test value). Task 2 adds the missing test coverage so each writer is independently verified.

- [ ] Extend `internal/creator/writer_test.go` with:

```go
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
    want := []string{
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
    for _, w := range want {
        if !strings.Contains(got, w) {
            t.Fatalf("agent file missing segment %q in:\n%s", w, got)
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
```

- [ ] Update the `import` block at the top of `internal/creator/writer_test.go` to include `"gopkg.in/yaml.v3"`.

- [ ] Run `go test ./internal/creator/...` → PASS.

- [ ] Commit:
```
git add internal/creator/writer_test.go
git commit -m "$(cat <<'EOF'
test(creator): cover WriteAgent + WriteCommand round-trip + omissions

Asserts each writer emits the expected frontmatter keys (and omits the
optional ones when blank), and that WriteCommand's output round-trips
through internal/plugins.splitFrontmatter so the user-commands loader
parses it cleanly.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

### Task 3 — Reloader interface

**Files:** `internal/creator/reloader.go`

- [ ] Write `internal/creator/reloader.go`:

```go
package creator

import "context"

// Reloader is the single-method contract the create tools call after a
// successful write. The concrete implementation lives in cmd/czcli/main.go
// (assistantReloader) and captures every dependency *agent.Assistant.Rebuild
// needs, so this package doesn't import internal/agent (cycle-safe).
//
// Implementations MUST be safe for concurrent calls — the dive agent runs
// tools in goroutines; Rebuild's atomic-swap mutex in internal/agent handles
// the actual swap. Errors are returned to the tool caller which surfaces
// them as a ToolResult error so the model can adapt.
type Reloader interface {
    Rebuild(ctx context.Context) error
}
```

- [ ] Run `go build ./internal/creator/...` → PASS.

- [ ] Commit:
```
git add internal/creator/reloader.go
git commit -m "$(cat <<'EOF'
feat(creator): declare Reloader interface for post-write hot-reload

Single-method interface; concrete impl in cmd/czcli/main.go captures the
Assistant.Rebuild dependencies so internal/creator stays independent of
internal/agent (no import cycle).

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

### Task 4 — Tools: three FuncTools wired to Writer + Reloader

**Files:** `internal/creator/tools.go`, `internal/creator/tools_test.go`

- [ ] Write the FAILING test `internal/creator/tools_test.go`:

```go
package creator

import (
    "context"
    "os"
    "path/filepath"
    "strings"
    "sync/atomic"
    "testing"

    "github.com/deepnoodle-ai/dive"
)

// fakeReloader records each Rebuild call so tests can assert the
// reload-after-write contract without spinning up a real *dive.Agent.
type fakeReloader struct {
    calls atomic.Int32
    err   error
}

func (f *fakeReloader) Rebuild(_ context.Context) error {
    f.calls.Add(1)
    return f.err
}

func newWriter(t *testing.T) (Writer, string) {
    t.Helper()
    home := t.TempDir()
    return Writer{
        SkillsDir:   filepath.Join(home, "skills"),
        AgentsDir:   filepath.Join(home, "agents"),
        CommandsDir: filepath.Join(home, "commands"),
    }, home
}

// callTool finds a tool by name and invokes it via its dive.Tool surface.
// dive's FuncTool exposes Call(ctx, json.RawMessage) via the Tool interface;
// the test uses the tool's documented invoke path with a JSON-encoded input.
func callTool(t *testing.T, tools []dive.Tool, name string, inputJSON string) *dive.ToolResult {
    t.Helper()
    for _, tl := range tools {
        if tl.Name() == name {
            res, err := tl.Call(context.Background(), []byte(inputJSON))
            if err != nil {
                t.Fatalf("tool %s call: %v", name, err)
            }
            return res
        }
    }
    t.Fatalf("tool %s not registered", name)
    return nil
}

func TestTools_RegistersThreeNames(t *testing.T) {
    w, _ := newWriter(t)
    got := Tools(w, &fakeReloader{})
    names := map[string]bool{}
    for _, tl := range got {
        names[tl.Name()] = true
    }
    for _, want := range []string{"create_skill", "create_agent", "create_command"} {
        if !names[want] {
            t.Fatalf("missing tool %q; got %v", want, names)
        }
    }
}

func TestCreateSkill_WritesFileAndRebuilds(t *testing.T) {
    w, _ := newWriter(t)
    fr := &fakeReloader{}
    tools := Tools(w, fr)
    res := callTool(t, tools, "create_skill", `{
        "name":"explain-go-embedding",
        "description":"Explain Go embedding succinctly.",
        "body":"Use a worked example.\n"
    }`)
    if res == nil || res.IsError {
        t.Fatalf("create_skill tool returned error: %+v", res)
    }
    if fr.calls.Load() != 1 {
        t.Fatalf("Reloader.Rebuild calls = %d, want 1", fr.calls.Load())
    }
    if _, err := os.Stat(filepath.Join(w.SkillsDir, "explain-go-embedding", "SKILL.md")); err != nil {
        t.Fatalf("expected SKILL.md to exist: %v", err)
    }
    out := toolResultText(res)
    if !strings.Contains(out, "SKILL.md") {
        t.Fatalf("expected tool result to mention SKILL.md; got: %q", out)
    }
}

func TestCreateAgent_WritesFileAndRebuilds(t *testing.T) {
    w, _ := newWriter(t)
    fr := &fakeReloader{}
    tools := Tools(w, fr)
    res := callTool(t, tools, "create_agent", `{
        "name":"reviewer",
        "description":"Reviews Go diffs.",
        "tools":["Read","Glob"],
        "body":"Be terse.\n"
    }`)
    if res == nil || res.IsError {
        t.Fatalf("create_agent error: %+v", res)
    }
    if fr.calls.Load() != 1 {
        t.Fatalf("Reloader.Rebuild calls = %d, want 1", fr.calls.Load())
    }
    if _, err := os.Stat(filepath.Join(w.AgentsDir, "reviewer.md")); err != nil {
        t.Fatalf("expected reviewer.md: %v", err)
    }
}

func TestCreateCommand_WritesFileAndRebuilds(t *testing.T) {
    w, _ := newWriter(t)
    fr := &fakeReloader{}
    tools := Tools(w, fr)
    res := callTool(t, tools, "create_command", `{
        "name":"greet",
        "description":"Greet the user.",
        "argument_hint":"<name>",
        "body":"Hello $ARGUMENTS!\n"
    }`)
    if res == nil || res.IsError {
        t.Fatalf("create_command error: %+v", res)
    }
    if fr.calls.Load() != 1 {
        t.Fatalf("Reloader.Rebuild calls = %d, want 1", fr.calls.Load())
    }
    if _, err := os.Stat(filepath.Join(w.CommandsDir, "greet.md")); err != nil {
        t.Fatalf("expected greet.md: %v", err)
    }
}

func TestCreateSkill_ConflictReturnsToolError(t *testing.T) {
    w, _ := newWriter(t)
    fr := &fakeReloader{}
    tools := Tools(w, fr)
    _ = callTool(t, tools, "create_skill", `{"name":"dup","description":"d","body":"b\n"}`)
    res := callTool(t, tools, "create_skill", `{"name":"dup","description":"d2","body":"b2\n"}`)
    if res == nil || !res.IsError {
        t.Fatalf("expected IsError tool result on conflict; got: %+v", res)
    }
    if fr.calls.Load() != 1 {
        t.Fatalf("Reloader.Rebuild calls = %d, want 1 (no reload on failure)", fr.calls.Load())
    }
}

func TestCreateSkill_InvalidNameReturnsToolError(t *testing.T) {
    w, _ := newWriter(t)
    fr := &fakeReloader{}
    tools := Tools(w, fr)
    res := callTool(t, tools, "create_skill", `{"name":"BAD/NAME","description":"d","body":"b\n"}`)
    if res == nil || !res.IsError {
        t.Fatalf("expected IsError on invalid name; got: %+v", res)
    }
    if fr.calls.Load() != 0 {
        t.Fatalf("Reloader.Rebuild calls = %d, want 0 (no reload on validation failure)", fr.calls.Load())
    }
}
```

`toolResultText` is a tiny test helper that flattens `dive.ToolResult.Content` to a string — add it to `tools_test.go`:

```go
// toolResultText returns the concatenated text content of a dive.ToolResult.
// dive's ToolResult content blocks each carry a Text field; for create tools
// the result is always a single text block produced by NewToolResultText.
func toolResultText(r *dive.ToolResult) string {
    var sb strings.Builder
    for _, c := range r.Content {
        sb.WriteString(c.Text)
    }
    return sb.String()
}
```

- [ ] Run `go test ./internal/creator/...` → FAIL (`tools.go` not yet present).

- [ ] Minimal impl `internal/creator/tools.go`:

```go
package creator

import (
    "context"
    "fmt"

    "github.com/deepnoodle-ai/dive"
)

// createSkillInput is the JSON contract for the create_skill tool. The model
// passes a kebab-case name, a one-line description, and the markdown body.
type createSkillInput struct {
    Name        string `json:"name" description:"Kebab-case skill name (^[a-z0-9][a-z0-9-]{0,63}$)."`
    Description string `json:"description" description:"One-line description for the skill frontmatter."`
    Body        string `json:"body" description:"Markdown body. Will be written verbatim under the frontmatter."`
    Overwrite   bool   `json:"overwrite,omitempty" description:"Replace an existing file with the same name."`
}

// createAgentInput is the JSON contract for the create_agent tool.
type createAgentInput struct {
    Name            string   `json:"name" description:"Kebab-case agent name."`
    Description     string   `json:"description" description:"One-line description for the agent frontmatter."`
    Tools           []string `json:"tools,omitempty" description:"Optional allow-list of tool names available to the agent."`
    DisallowedTools []string `json:"disallowed_tools,omitempty" description:"Optional deny-list of tool names blocked for the agent."`
    Body            string   `json:"body" description:"Markdown system-prompt body."`
    Overwrite       bool     `json:"overwrite,omitempty" description:"Replace an existing file with the same name."`
}

// createCommandInput is the JSON contract for the create_command tool. The
// body may contain $ARGUMENTS which the CLI dispatcher expands at call time.
type createCommandInput struct {
    Name         string `json:"name" description:"Kebab-case command name (used as /<name>)."`
    Description  string `json:"description" description:"One-line description for the command frontmatter."`
    ArgumentHint string `json:"argument_hint,omitempty" description:"Optional argument hint shown in /help."`
    Body         string `json:"body" description:"Markdown prompt body. May include the literal $ARGUMENTS token."`
    Overwrite    bool   `json:"overwrite,omitempty" description:"Replace an existing file with the same name."`
}

// Tools returns the three FuncTools that materialize a skill / agent / command
// file and trigger a hot-reload. They share the same shape: validate +
// delegate to Writer + on success call Reloader.Rebuild + return a tool
// result naming the absolute path. Writer-side failures (name validation,
// already-exists conflict) are surfaced via NewToolResultError so the model
// can retry; Reloader failures are surfaced the same way but the file is
// already on disk and a subsequent /reload (or a successful next mutation)
// will pick it up.
func Tools(w Writer, r Reloader) []dive.Tool {
    skillTool := dive.FuncTool(
        "create_skill",
        "Create a new skill: writes a SKILL.md under czcli's skills directory and reloads the agent so the new skill is immediately available.",
        func(ctx context.Context, in *createSkillInput) (*dive.ToolResult, error) {
            path, err := w.WriteSkill(in.Name, in.Description, in.Body, in.Overwrite)
            if err != nil {
                return dive.NewToolResultError(fmt.Sprintf("create_skill failed: %s", err.Error())), nil
            }
            if err := r.Rebuild(ctx); err != nil {
                return dive.NewToolResultError(
                    fmt.Sprintf("create_skill: wrote %s but reload failed: %s", path, err.Error()),
                ), nil
            }
            return dive.NewToolResultText(fmt.Sprintf("wrote %s; agent reloaded", path)), nil
        },
    )

    agentTool := dive.FuncTool(
        "create_agent",
        "Create a new sub-agent persona: writes <name>.md under czcli's agents directory and reloads the agent so the new persona is immediately available.",
        func(ctx context.Context, in *createAgentInput) (*dive.ToolResult, error) {
            path, err := w.WriteAgent(in.Name, in.Description, in.Tools, in.DisallowedTools, in.Body, in.Overwrite)
            if err != nil {
                return dive.NewToolResultError(fmt.Sprintf("create_agent failed: %s", err.Error())), nil
            }
            if err := r.Rebuild(ctx); err != nil {
                return dive.NewToolResultError(
                    fmt.Sprintf("create_agent: wrote %s but reload failed: %s", path, err.Error()),
                ), nil
            }
            return dive.NewToolResultText(fmt.Sprintf("wrote %s; agent reloaded", path)), nil
        },
    )

    commandTool := dive.FuncTool(
        "create_command",
        "Create a new slash command: writes <name>.md under czcli's commands directory and reloads the agent so /<name> is immediately available.",
        func(ctx context.Context, in *createCommandInput) (*dive.ToolResult, error) {
            path, err := w.WriteCommand(in.Name, in.Description, in.ArgumentHint, in.Body, in.Overwrite)
            if err != nil {
                return dive.NewToolResultError(fmt.Sprintf("create_command failed: %s", err.Error())), nil
            }
            if err := r.Rebuild(ctx); err != nil {
                return dive.NewToolResultError(
                    fmt.Sprintf("create_command: wrote %s but reload failed: %s", path, err.Error()),
                ), nil
            }
            return dive.NewToolResultText(fmt.Sprintf("wrote %s; agent reloaded", path)), nil
        },
    )

    return []dive.Tool{skillTool, agentTool, commandTool}
}
```

- [ ] Run `go test ./internal/creator/...` → PASS.

- [ ] Commit:
```
git add internal/creator/tools.go internal/creator/tools_test.go
git commit -m "$(cat <<'EOF'
feat(creator): add create_skill/create_agent/create_command FuncTools

Each tool validates + writes the file via Writer, calls Reloader.Rebuild on
success, and surfaces failures (invalid name, conflict, reload error) as a
ToolResult error so the model can adapt. fakeReloader-driven tests cover
the happy paths plus the no-reload-on-failure contract.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

### Task 5 — Agent: append creatorTools to Build / BuildWithMCPInfos / Rebuild

**Files:** `internal/agent/agent.go`, `internal/agent/agent_test.go`

The agent surface gains a single trailing parameter on each constructor and on `Rebuild`. The parameter is `creatorTools []dive.Tool`; passing `nil` keeps the prior behavior exactly.

- [ ] Write the FAILING test `internal/agent/agent_test.go` (add a new test; existing tests in that file MUST keep working — adapt their call sites to pass `nil` for the new arg in a single sweep):

```go
func TestBuildWithMCPInfos_AppendsCreatorToolsToRegistry(t *testing.T) {
    cfg := minimalCfg(t)
    store := openTestStore(t)
    defer store.Close()
    model := &fakeStreamingModel{}

    fakeCreatorTool := dive.FuncTool(
        "create_skill",
        "fake",
        func(ctx context.Context, in *struct{}) (*dive.ToolResult, error) {
            return dive.NewToolResultText("ok"), nil
        },
    )
    a, err := agent.BuildWithMCPInfos(
        context.Background(), cfg, store, model, nil, nil, nil, nil, nil, nil,
        []dive.Tool{fakeCreatorTool},
    )
    if err != nil {
        t.Fatalf("BuildWithMCPInfos: %v", err)
    }
    st, err := a.Status(context.Background())
    if err != nil {
        t.Fatalf("Status: %v", err)
    }
    var found bool
    for _, n := range st.ToolNames {
        if n == "create_skill" {
            found = true
            break
        }
    }
    if !found {
        t.Fatalf("create_skill not in tool names: %v", st.ToolNames)
    }
}

func TestRebuild_PicksUpNewCreatorTools(t *testing.T) {
    cfg := minimalCfg(t)
    store := openTestStore(t)
    defer store.Close()
    model := &fakeStreamingModel{}

    a, err := agent.BuildWithMCPInfos(
        context.Background(), cfg, store, model, nil, nil, nil, nil, nil, nil, nil,
    )
    if err != nil {
        t.Fatalf("BuildWithMCPInfos: %v", err)
    }
    fakeCreatorTool := dive.FuncTool(
        "create_command",
        "fake",
        func(ctx context.Context, in *struct{}) (*dive.ToolResult, error) {
            return dive.NewToolResultText("ok"), nil
        },
    )
    if err := a.Rebuild(context.Background(), cfg, nil, nil, nil, nil, nil, nil, []dive.Tool{fakeCreatorTool}); err != nil {
        t.Fatalf("Rebuild: %v", err)
    }
    st, _ := a.Status(context.Background())
    var found bool
    for _, n := range st.ToolNames {
        if n == "create_command" {
            found = true
            break
        }
    }
    if !found {
        t.Fatalf("create_command not present after Rebuild: %v", st.ToolNames)
    }
}
```

(`minimalCfg`, `openTestStore`, `fakeStreamingModel` are assumed present from Plan 3's agent tests; if absent, copy the standard 3-line stubs Plan 3 uses.)

- [ ] Run `go test ./internal/agent/...` → FAIL (`BuildWithMCPInfos` doesn't accept the new arg; `Rebuild` doesn't accept it either).

- [ ] Modify `internal/agent/agent.go` — append `creatorTools []dive.Tool` to all three signatures and inject it after the LSP tools (so the final tool order is: builtins, mcp, lsp, creator):

```go
func Build(
    ctx context.Context,
    cfg *config.Config,
    store *memory.Store,
    model llm.StreamingLLM,
    skillRes *skills.LoadResult,
    mcpTools []dive.Tool,
    creatorTools []dive.Tool,
) (*Assistant, error) {
    return BuildWithMCPInfos(ctx, cfg, store, model, skillRes, mcpTools, nil, nil, nil, nil, creatorTools)
}

func BuildWithMCPInfos(
    ctx context.Context,
    cfg *config.Config,
    store *memory.Store,
    model llm.StreamingLLM,
    skillRes *skills.LoadResult,
    mcpTools []dive.Tool,
    mcpInfos []mcp.ServerInfo,
    lspTools []dive.Tool,
    lspInfos []lsp.ServerInfo,
    hooksDisp *hooks.Dispatcher,
    creatorTools []dive.Tool,
) (*Assistant, error) {
    // ... existing body up to and including the lspTools append ...
    a.tools = append(a.tools, mcpTools...)
    a.tools = append(a.tools, lspTools...)
    a.tools = append(a.tools, creatorTools...)  // NEW
    // ... rest of body unchanged ...
}

func (a *Assistant) Rebuild(
    ctx context.Context,
    cfg *config.Config,
    skillRes *skills.LoadResult,
    mcpTools []dive.Tool,
    mcpInfos []mcp.ServerInfo,
    lspTools []dive.Tool,
    lspInfos []lsp.ServerInfo,
    hooksDisp *hooks.Dispatcher,
    creatorTools []dive.Tool,
) error {
    next, err := BuildWithMCPInfos(ctx, cfg, a.store, a.model, skillRes, mcpTools, mcpInfos, lspTools, lspInfos, hooksDisp, creatorTools)
    if err != nil {
        return fmt.Errorf("agent: rebuild: %w", err)
    }
    // ... existing atomic-swap body unchanged ...
}
```

- [ ] Update every other call site in the tree in this same task (so the build never breaks): `cmd/czcli/main.go` two call sites (`BuildWithMCPInfos` in `run()` and `a.assistant.Rebuild` in `pluginAdapter.Rebuild`) get a trailing `nil` argument as a temporary placeholder; the real wiring lands in Task 6.

- [ ] Run `go build ./... && go test ./internal/agent/... ./cmd/...` → PASS.

- [ ] Commit:
```
git add internal/agent/agent.go internal/agent/agent_test.go cmd/czcli/main.go
git commit -m "$(cat <<'EOF'
feat(agent): append creatorTools param to Build/BuildWithMCPInfos/Rebuild

Single new trailing parameter on each constructor. Existing call sites in
cmd/czcli/main.go pass nil temporarily; Task 6 wires the real creator
tool slice. Test asserts the tools land in Status.ToolNames after both
Build and Rebuild.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

### Task 6 — main.go: construct Writer + assistantReloader + wire creator tools

**Files:** `cmd/czcli/main.go`, `cmd/czcli/main_test.go` (new tiny test that compiles the wiring under a fake home dir)

- [ ] Add the `assistantReloader` shim + the home-dir resolver to `cmd/czcli/main.go`:

```go
// assistantReloader satisfies creator.Reloader by capturing the seven
// non-creator dependencies *agent.Assistant.Rebuild needs (cfg, skillRes,
// mcpTools, mcpInfos, lspTools, lspInfos, hooksDisp) plus the creator tool
// slice itself (so Rebuild re-supplies the same set the live agent holds).
// pluginAdapter.Rebuild calls update() after every reload so the captured
// args track the latest plugin contributions.
type assistantReloader struct {
    mu          sync.Mutex
    assistant   *agent.Assistant
    cfg         *config.Config
    skillRes    *skills.LoadResult
    mcpTools    []dive.Tool
    mcpInfos    []mcp.ServerInfo
    lspTools    []dive.Tool
    lspInfos    []lsp.ServerInfo
    hooksDisp   *hooks.Dispatcher
    creatorTools []dive.Tool
}

func (r *assistantReloader) Rebuild(ctx context.Context) error {
    r.mu.Lock()
    args := *r // shallow copy under lock so the actual rebuild runs unlocked
    r.mu.Unlock()
    return args.assistant.Rebuild(ctx, args.cfg, args.skillRes,
        args.mcpTools, args.mcpInfos, args.lspTools, args.lspInfos,
        args.hooksDisp, args.creatorTools)
}

// update refreshes the captured arg snapshot. Called by pluginAdapter.Rebuild
// (and the initial wiring in run) so a creator-triggered Rebuild always sees
// the latest plugin contributions.
func (r *assistantReloader) update(
    skillRes *skills.LoadResult,
    mcpTools []dive.Tool, mcpInfos []mcp.ServerInfo,
    lspTools []dive.Tool, lspInfos []lsp.ServerInfo,
    hooksDisp *hooks.Dispatcher,
) {
    r.mu.Lock()
    defer r.mu.Unlock()
    r.skillRes = skillRes
    r.mcpTools = mcpTools
    r.mcpInfos = mcpInfos
    r.lspTools = lspTools
    r.lspInfos = lspInfos
    r.hooksDisp = hooksDisp
}

// creatorPaths resolves the three target directories under the user's HOME.
// Falls back to a process-local tmp dir when HOME is unresolvable so czcli
// still starts on broken environments (the create tools will still work; the
// files just land in a temp dir).
func creatorPaths() (skillsDir, agentsDir, commandsDir string) {
    home, err := os.UserHomeDir()
    if err != nil {
        slog.Warn("creator: cannot resolve home dir; using temp dir", "err", err)
        home = os.TempDir()
    }
    base := filepath.Join(home, ".czcli")
    return filepath.Join(base, "skills"),
        filepath.Join(base, "agents"),
        filepath.Join(base, "commands")
}
```

- [ ] Modify `run()` in `cmd/czcli/main.go` to construct the writer + reloader + tools and thread them through `BuildWithMCPInfos`. Replace the existing `agent.BuildWithMCPInfos(...)` call (currently at `main.go:119`) with:

```go
    skillsDir, agentsDir, commandsDir := creatorPaths()
    writer := creator.Writer{SkillsDir: skillsDir, AgentsDir: agentsDir, CommandsDir: commandsDir}
    reloader := &assistantReloader{
        cfg:         cfg,
        skillRes:    skillRes,
        mcpTools:    mcpTools,
        mcpInfos:    mcpInfos,
        lspTools:    lspTools,
        lspInfos:    lspInfos,
        hooksDisp:   hooksDisp,
    }
    creatorTools := creator.Tools(writer, reloader)
    reloader.creatorTools = creatorTools

    assistant, err := agent.BuildWithMCPInfos(ctx, cfg, store, model, skillRes, mcpTools, mcpInfos, lspTools, lspInfos, hooksDisp, creatorTools)
    if err != nil {
        return fmt.Errorf("build assistant: %w", err)
    }
    reloader.assistant = assistant
```

- [ ] Add a `creatorTools []dive.Tool` field on `pluginAdapter` and a `reloader *assistantReloader` field, so `pluginAdapter.Rebuild` can both (a) re-pass the same `creatorTools` slice through `assistant.Rebuild` and (b) refresh the reloader's captured args before the rebuild completes:

```go
type pluginAdapter struct {
    mgr           *plugins.Manager
    cfg           *config.Config
    assistant     *agent.Assistant
    lspHolder     *lspHolder
    creatorTools  []dive.Tool       // NEW
    reloader      *assistantReloader // NEW
}
```

Constructor: `pluginsAdp := pluginAdapter{mgr: pluginsMgr, cfg: cfg, assistant: assistant, lspHolder: holder, creatorTools: creatorTools, reloader: reloader}`.

- [ ] Modify `pluginAdapter.Rebuild` (currently `main.go:398`) to call `a.reloader.update(...)` immediately before `a.assistant.Rebuild(...)` and to pass `a.creatorTools` as the trailing arg:

```go
func (a pluginAdapter) Rebuild(ctx context.Context) error {
    contrib, _, err := a.mgr.Load(ctx)
    if err != nil {
        return fmt.Errorf("plugins: reload: %w", err)
    }
    skillRes, err := skills.Load(a.cfg.Skills, contrib.SkillDirs)
    if err != nil {
        slog.Warn("plugins: rebuild: skills.Load failed; continuing without skills", "err", err)
        skillRes = nil
    }
    mcpServers := mergeMCPServers(a.cfg.MCP.Servers, contrib.MCPServers)
    mcpTools, mcpInfos, err := mcp.Connect(ctx, mcpServers, mcpTokenPath())
    if err != nil {
        slog.Warn("plugins: rebuild: mcp.Connect failed; continuing without MCP tools", "err", err)
    }
    if a.lspHolder != nil {
        a.lspHolder.swap(buildLSP(ctx, a.cfg, contrib))
    }
    var lspTools []dive.Tool
    var lspInfos []lsp.ServerInfo
    if a.lspHolder != nil {
        lspTools, lspInfos = a.lspHolder.current()
    }
    hookEntries := contribsToHookEntries(contrib.Hooks, slog.Default())
    hooksDisp := hooks.Load(hookEntries, slog.Default())

    // Refresh the reloader's captured args BEFORE the rebuild so any in-flight
    // create-tool call that runs during the swap sees the latest contributions.
    if a.reloader != nil {
        a.reloader.update(skillRes, mcpTools, mcpInfos, lspTools, lspInfos, hooksDisp)
    }
    if err := a.assistant.Rebuild(ctx, a.cfg, skillRes, mcpTools, mcpInfos, lspTools, lspInfos, hooksDisp, a.creatorTools); err != nil {
        return fmt.Errorf("plugins: agent rebuild: %w", err)
    }
    slog.Info("plugins: hot-reload complete",
        "skills_dirs", len(contrib.SkillDirs),
        "mcp_servers", len(contrib.MCPServers),
        "lsp_servers", len(contrib.LSPServers),
        "hooks", len(contrib.Hooks),
        "commands", len(contrib.Commands),
    )
    return nil
}
```

- [ ] Add the missing imports to `cmd/czcli/main.go`:

```go
import (
    // existing imports ...
    "sync"

    "github.com/caxqueiroz/czcli/internal/creator"
)
```

- [ ] Run `go build ./... && go vet ./... && go run ./cmd/czcli` (no config, on a clean HOME) → exits gracefully with the "wrote a default config" message (existing first-run path). Verify by running the binary in a temp HOME:

```
HOME=$(mktemp -d) CZCLI_CONFIG= go run ./cmd/czcli
```

Expect output: `czcli: wrote a default config to <HOME>/.czcli/config.yaml ...` and exit 0.

- [ ] Commit:
```
git add cmd/czcli/main.go
git commit -m "$(cat <<'EOF'
feat(czcli): wire creator tools + assistantReloader through agent build

Constructs creator.Writer from ~/.czcli/{skills,agents,commands}; the
assistantReloader shim captures every Assistant.Rebuild dependency so the
create tools can trigger hot-reload via the one-method creator.Reloader
contract. pluginAdapter.Rebuild refreshes the captured args before each
rebuild so a creator-triggered reload always sees the latest plugin
contributions.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

### Task 7 — /new slash command dispatch (no wizard yet)

**Files:** `internal/channel/cli/commands.go`, `internal/channel/cli/commands_test.go`

Land the `/new` dispatcher first as a thin dispatcher returning a usage string when no subcommand is supplied and a one-shot "wizard not yet active" stub when one is. Task 8 swaps the stub for the wizard impl.

- [ ] Write the FAILING test `internal/channel/cli/commands_test.go` (add to the existing test file):

```go
func TestHandleCommand_NewUsage(t *testing.T) {
    m := newTestModel(t)
    out, quit := m.handleCommand("/new")
    if quit {
        t.Fatalf("/new must not quit")
    }
    if !strings.Contains(out, "/new skill|agent|command") {
        t.Fatalf("/new usage missing kind list: %q", out)
    }
}

func TestHandleCommand_NewUnknownKind(t *testing.T) {
    m := newTestModel(t)
    out, _ := m.handleCommand("/new foo bar")
    if !strings.Contains(out, "unknown kind") {
        t.Fatalf("expected unknown-kind error: %q", out)
    }
}
```

(`newTestModel` is assumed present from existing CLI tests; if not, add `func newTestModel(t *testing.T) model { return newModel(80, 24) }`.)

- [ ] Run `go test ./internal/channel/cli/...` → FAIL (no `/new` case in `handleCommand`).

- [ ] Modify `internal/channel/cli/commands.go`:

Add `"new"` to the `switch name` block in `handleCommand` (between `"hooks"` and `default`):

```go
case "new":
    return m.cmdNew(args), false
```

Update the default case's "try /…" hint to include `/new`:

```go
default:
    return fmt.Sprintf("unknown command /%s — try /stats /tools /agents /schedule /model /skills /mcp /lsp /plugin /hooks /new", name), false
```

Add the `cmdNew` method at the end of the file:

```go
const newUsage = "usage: /new skill|agent|command [name]"

// cmdNew is the entry point for the just-ask creator wizard. Subcommands:
//   /new skill   [name]   — start the skill wizard
//   /new agent   [name]   — start the agent wizard
//   /new command [name]   — start the command wizard
// Without a kind the command prints usage; with an unsupported kind it
// reports the error inline. The wizard state machine lives in wizard.go and
// is activated by setting m.wizard; Task 8 wires that.
func (m model) cmdNew(args string) string {
    fields := tokenizeArgs(args)
    if len(fields) == 0 {
        return newUsage
    }
    kind := strings.ToLower(fields[0])
    switch kind {
    case "skill", "agent", "command":
        // Task 8 swaps this stub for the real wizard activation.
        return fmt.Sprintf("/new %s: wizard not yet active (Task 8 wires it)", kind)
    default:
        return fmt.Sprintf("unknown kind %q; %s", kind, newUsage)
    }
}
```

- [ ] Run `go test ./internal/channel/cli/...` → PASS.

- [ ] Commit:
```
git add internal/channel/cli/commands.go internal/channel/cli/commands_test.go
git commit -m "$(cat <<'EOF'
feat(cli): add /new slash command dispatcher (skill|agent|command)

Thin dispatcher; the wizard state-machine lands in the next task. Unknown
kinds and missing args report usage inline. Default-case hint updated to
include /new so /help (via Plan 11's overlay) lists it.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

### Task 8 — /new wizard state machine + creatorBackend interface

**Files:** `internal/creator/wizard.go` (NEW, lives in creator only as the data model — the wizard's *model integration* stays in cli), `internal/channel/cli/model.go`, `internal/channel/cli/commands.go`, `internal/channel/cli/cli.go`, `internal/channel/cli/commands_test.go`

The wizard's data is just a step counter + collected fields; the actual key routing lives in `model.Update`. We add one optional pointer field (`wizard *createWizard`) so existing tests keep passing.

> **Blocking check:** if any existing model_test.go or commands_test.go assertion fails due to the wizard field's presence (e.g. snapshot tests comparing model struct shape), STOP and report. The plan author should not bolt the wizard on if it forces a model snapshot rewrite.

- [ ] Write the FAILING test in `internal/channel/cli/commands_test.go`:

```go
// fakeCreatorBackend records the most recent create call for assertion.
type fakeCreatorBackend struct {
    kind   string
    name   string
    desc   string
    body   string
    tools  []string
    hint   string
    called int
}

func (f *fakeCreatorBackend) CreateSkill(_ context.Context, name, desc, body string) (string, error) {
    f.called++
    f.kind, f.name, f.desc, f.body = "skill", name, desc, body
    return "/tmp/" + name + "/SKILL.md", nil
}
func (f *fakeCreatorBackend) CreateAgent(_ context.Context, name, desc string, tools []string, body string) (string, error) {
    f.called++
    f.kind, f.name, f.desc, f.tools, f.body = "agent", name, desc, tools, body
    return "/tmp/" + name + ".md", nil
}
func (f *fakeCreatorBackend) CreateCommand(_ context.Context, name, desc, hint, body string) (string, error) {
    f.called++
    f.kind, f.name, f.desc, f.hint, f.body = "command", name, desc, hint, body
    return "/tmp/" + name + ".md", nil
}

func TestNewWizard_SkillHappyPath(t *testing.T) {
    fb := &fakeCreatorBackend{}
    m := newTestModel(t)
    m.creator = fb

    // /new skill explain-go-embedding
    out, _ := m.handleCommand("/new skill explain-go-embedding")
    if !strings.Contains(out, "description?") {
        t.Fatalf("expected wizard to ask for description; got: %q", out)
    }
    if m.wizard == nil || m.wizard.kind != "skill" || m.wizard.name != "explain-go-embedding" {
        t.Fatalf("wizard state not set; got: %+v", m.wizard)
    }

    // Synthesize description input + Enter.
    typed, _ := m.feedLine("Explain Go embedding succinctly.")
    if !strings.Contains(typed, "body?") {
        t.Fatalf("expected body prompt after description; got: %q", typed)
    }

    // Body + Enter.
    typed, _ = m.feedLine("Use a worked example.")
    if !strings.Contains(typed, "confirm? (y/n)") {
        t.Fatalf("expected confirm prompt after body; got: %q", typed)
    }

    // Confirm.
    typed, _ = m.feedLine("y")
    if fb.called != 1 || fb.kind != "skill" || fb.name != "explain-go-embedding" {
        t.Fatalf("backend not invoked correctly: %+v", fb)
    }
    if m.wizard != nil {
        t.Fatalf("wizard should clear after confirm; got: %+v", m.wizard)
    }
    if !strings.Contains(typed, "wrote /tmp/explain-go-embedding/SKILL.md") {
        t.Fatalf("expected success message; got: %q", typed)
    }
}

func TestNewWizard_CancelWithSlashCancel(t *testing.T) {
    fb := &fakeCreatorBackend{}
    m := newTestModel(t)
    m.creator = fb
    _, _ = m.handleCommand("/new skill foo")
    out, _ := m.handleCommand("/cancel")
    if !strings.Contains(out, "cancelled") {
        t.Fatalf("expected cancellation message; got: %q", out)
    }
    if m.wizard != nil {
        t.Fatalf("wizard should clear on cancel; got: %+v", m.wizard)
    }
    if fb.called != 0 {
        t.Fatalf("backend must not be called on cancel; calls=%d", fb.called)
    }
}
```

`m.feedLine` is a test helper that simulates "user types <line> then presses Enter" by setting the input value and calling `m.submit()`. Add:

```go
// feedLine pushes a single line through the model's submit path and returns
// the system-output text from the wizard step + a quit flag (false for these
// tests). Used by wizard tests to drive the description → body → confirm
// machine without spinning up bubbletea.
func (m *model) feedLine(line string) (string, bool) {
    m.input.SetValue(line)
    tm, _ := m.submit()
    *m = tm.(model)
    if len(m.history) == 0 {
        return "", false
    }
    return m.history[len(m.history)-1].text, false
}
```

(`feedLine` mutates the model in-place via the pointer; the existing `submit()` returns a value `tea.Model` so we re-assign through the pointer. If `model` is a value type, the test helper takes/returns the value instead — both shapes work.)

- [ ] Run `go test ./internal/channel/cli/...` → FAIL (no `wizard`, `creator`, `cmdNew` activates only the stub).

- [ ] Add `internal/creator/wizard.go` for the pure-data wizard step model (lives in creator so the cli model can refer to it without circular imports):

```go
package creator

// WizardKind identifies which writer the wizard targets.
type WizardKind string

const (
    WizardKindSkill   WizardKind = "skill"
    WizardKindAgent   WizardKind = "agent"
    WizardKindCommand WizardKind = "command"
)

// WizardStep is the current step in the /new wizard state machine.
type WizardStep int

const (
    WizardStepDescription WizardStep = iota
    WizardStepTools                  // skill: skipped; agent: comma-separated tools; command: argument hint
    WizardStepBody
    WizardStepConfirm
    WizardStepDone
)

// Wizard is the pure-data state shared by /new across kinds. It carries no
// rendering logic; the cli package owns prompts and input routing.
type Wizard struct {
    Kind         WizardKind
    Step         WizardStep
    Name         string
    Description  string
    Tools        []string   // agent only
    ArgumentHint string     // command only
    Body         string
}

// Prompt returns the prompt text the cli renders for the wizard's current step.
func (w *Wizard) Prompt() string {
    switch w.Step {
    case WizardStepDescription:
        return "description?"
    case WizardStepTools:
        switch w.Kind {
        case WizardKindAgent:
            return "tools? (comma-separated, blank for default)"
        case WizardKindCommand:
            return "argument-hint? (blank for none)"
        default:
            return ""
        }
    case WizardStepBody:
        return "body? (paste prompt body; press Enter to finish line)"
    case WizardStepConfirm:
        return "confirm? (y/n)"
    }
    return ""
}

// Advance records the typed input into the appropriate field and transitions
// to the next step. Returns the prompt for the new step or "" if WizardStepDone.
func (w *Wizard) Advance(line string) string {
    switch w.Step {
    case WizardStepDescription:
        w.Description = line
        // skill has no tools/hint step
        if w.Kind == WizardKindSkill {
            w.Step = WizardStepBody
        } else {
            w.Step = WizardStepTools
        }
    case WizardStepTools:
        switch w.Kind {
        case WizardKindAgent:
            w.Tools = splitCommaList(line)
        case WizardKindCommand:
            w.ArgumentHint = line
        }
        w.Step = WizardStepBody
    case WizardStepBody:
        w.Body = line
        w.Step = WizardStepConfirm
    case WizardStepConfirm:
        w.Step = WizardStepDone
    }
    return w.Prompt()
}

func splitCommaList(s string) []string {
    if s == "" {
        return nil
    }
    var out []string
    for _, p := range splitByComma(s) {
        p = trimSpace(p)
        if p != "" {
            out = append(out, p)
        }
    }
    return out
}

// splitByComma + trimSpace are tiny local helpers so wizard.go doesn't pull
// strings just for two stdlib calls (kept private to creator).
func splitByComma(s string) []string {
    var out []string
    cur := ""
    for _, r := range s {
        if r == ',' {
            out = append(out, cur)
            cur = ""
            continue
        }
        cur += string(r)
    }
    out = append(out, cur)
    return out
}

func trimSpace(s string) string {
    start, end := 0, len(s)
    for start < end && (s[start] == ' ' || s[start] == '\t') {
        start++
    }
    for end > start && (s[end-1] == ' ' || s[end-1] == '\t') {
        end--
    }
    return s[start:end]
}
```

- [ ] Add the `creatorBackend` interface and the `wizard` + `creator` fields to `internal/channel/cli/model.go`:

```go
import (
    // existing imports
    "github.com/caxqueiroz/czcli/internal/creator"
)

// creatorBackend is the surface the /new wizard drives. Implementations call
// the Writer + Reloader pair (cmd/czcli wires the real one over the same
// Writer + Reloader the create tools use, so /new and natural-language
// requests produce identical files).
type creatorBackend interface {
    CreateSkill(ctx context.Context, name, description, body string) (path string, err error)
    CreateAgent(ctx context.Context, name, description string, tools []string, body string) (path string, err error)
    CreateCommand(ctx context.Context, name, description, argumentHint, body string) (path string, err error)
}

// On the model struct, ADD:
//
//   creator creatorBackend  // optional; nil when /new is not wired
//   wizard  *creator.Wizard // nil when no wizard is active
```

- [ ] Update `model.submit()` so wizard input is routed through `Advance` when active:

```go
func (m model) submit() (tea.Model, tea.Cmd) {
    line := strings.TrimSpace(m.input.Value())
    m.input.Reset()
    if line == "" {
        return m, nil
    }
    if m.wizard != nil && !strings.HasPrefix(line, "/") {
        return m.wizardStep(line)
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

// wizardStep advances the active wizard by one input line. On WizardStepConfirm
// the creator backend is invoked; on cancel ("/cancel" handled in handleCommand)
// the wizard is cleared.
func (m model) wizardStep(line string) (tea.Model, tea.Cmd) {
    next := m.wizard.Advance(line)
    if m.wizard.Step == creator.WizardStepDone {
        return m.runWizardCreate()
    }
    m.history = append(m.history, historyEntry{who: "sys", text: next})
    m.refreshViewport()
    return m, nil
}

// runWizardCreate dispatches the collected wizard fields to the creator
// backend and clears the wizard state. The success message includes the
// written absolute path so the user has a copy-paste-ready breadcrumb.
func (m model) runWizardCreate() (tea.Model, tea.Cmd) {
    w := m.wizard
    m.wizard = nil
    if m.creator == nil {
        m.history = append(m.history, historyEntry{who: "sys", text: "/new: creator backend not wired (cli.WithCreator not set)"})
        m.refreshViewport()
        return m, nil
    }
    ctx := context.Background()
    var (
        path string
        err  error
    )
    switch w.Kind {
    case creator.WizardKindSkill:
        path, err = m.creator.CreateSkill(ctx, w.Name, w.Description, w.Body)
    case creator.WizardKindAgent:
        path, err = m.creator.CreateAgent(ctx, w.Name, w.Description, w.Tools, w.Body)
    case creator.WizardKindCommand:
        path, err = m.creator.CreateCommand(ctx, w.Name, w.Description, w.ArgumentHint, w.Body)
    }
    if err != nil {
        m.history = append(m.history, historyEntry{who: "sys", text: fmt.Sprintf("/new failed: %v", err)})
    } else {
        m.history = append(m.history, historyEntry{who: "sys", text: fmt.Sprintf("wrote %s; agent reloaded", path)})
    }
    m.refreshViewport()
    return m, nil
}
```

(Add the missing `"context"` and `"fmt"` imports to `model.go` if not already present.)

- [ ] Replace the stub in `cmdNew` (Task 7) with real wizard activation:

```go
func (m model) cmdNew(args string) string {
    fields := tokenizeArgs(args)
    if len(fields) == 0 {
        return newUsage
    }
    kind := strings.ToLower(fields[0])
    var wk creator.WizardKind
    switch kind {
    case "skill":
        wk = creator.WizardKindSkill
    case "agent":
        wk = creator.WizardKindAgent
    case "command":
        wk = creator.WizardKindCommand
    default:
        return fmt.Sprintf("unknown kind %q; %s", kind, newUsage)
    }
    name := ""
    if len(fields) >= 2 {
        name = fields[1]
    }
    // ASSIGNS to the receiver-local copy of model; for the wizard activation
    // to stick, cmdNew is paired with an Update in submit() — but cmdNew is
    // invoked from handleCommand which is invoked from submit() which returns
    // the model by value. We mutate via the side-channel: handleCommand
    // returns a wizard signal in the output string AND mutates m via the
    // pointer pathway below. See the wizard activation in handleCommand
    // wrapper.
    return fmt.Sprintf("__WIZARD__:%s:%s", wk, name)
}
```

Because `model.handleCommand` is a value-receiver method, we can't mutate `m.wizard` directly inside `cmdNew`. The cleanest fix: rework `handleCommand` to return `(out string, quit bool, wizard *creator.Wizard)` and have `submit()` install the wizard on the model copy before returning. Update the dispatcher:

```go
// handleCommand returns the textual output, the quit flag, AND an optional
// wizard to install on the model. Only /new sets the wizard; every other
// command returns nil.
func (m model) handleCommand(line string) (string, bool, *creator.Wizard) {
    name, args := parseCommand(line)
    switch name {
    case "quit", "exit":
        return "", true, nil
    case "cancel":
        if m.wizard != nil {
            return "/new: cancelled", false, nil
        }
        return "nothing to cancel", false, nil
    case "stats":
        return m.cmdStats(), false, nil
    case "tools":
        return m.cmdTools(), false, nil
    case "agents":
        return m.cmdAgents(), false, nil
    case "schedule":
        return m.cmdSchedule(args), false, nil
    case "model":
        return m.cmdModel(), false, nil
    case "skills":
        return m.cmdSkills(), false, nil
    case "mcp":
        return m.cmdMCP(), false, nil
    case "lsp":
        return m.cmdLSP(), false, nil
    case "plugin":
        return m.cmdPlugin(args), false, nil
    case "hooks":
        return m.cmdHooks(), false, nil
    case "new":
        return m.cmdNewWithWizard(args)
    default:
        return fmt.Sprintf("unknown command /%s — try /stats /tools /agents /schedule /model /skills /mcp /lsp /plugin /hooks /new", name), false, nil
    }
}

// cmdNewWithWizard activates the wizard and returns the initial prompt.
func (m model) cmdNewWithWizard(args string) (string, bool, *creator.Wizard) {
    fields := tokenizeArgs(args)
    if len(fields) == 0 {
        return newUsage, false, nil
    }
    kind := strings.ToLower(fields[0])
    var wk creator.WizardKind
    switch kind {
    case "skill":
        wk = creator.WizardKindSkill
    case "agent":
        wk = creator.WizardKindAgent
    case "command":
        wk = creator.WizardKindCommand
    default:
        return fmt.Sprintf("unknown kind %q; %s", kind, newUsage), false, nil
    }
    name := ""
    if len(fields) >= 2 {
        name = fields[1]
    }
    w := &creator.Wizard{Kind: wk, Name: name, Step: creator.WizardStepDescription}
    // If no name supplied, ask for it first; we model this as a synthetic
    // "name?" step inlined into the first message rather than adding a new
    // WizardStep so the state machine stays tight.
    initial := w.Prompt()
    if name == "" {
        initial = "name? (kebab-case, then) " + initial
    }
    return fmt.Sprintf("/new %s: %s", kind, initial), false, w
}
```

Update `submit()` to install the returned wizard:

```go
if strings.HasPrefix(line, "/") {
    out, quit, wiz := m.handleCommand(line)
    if quit {
        return m, tea.Quit
    }
    if wiz != nil {
        m.wizard = wiz
    }
    if out != "" {
        m.history = append(m.history, historyEntry{who: "sys", text: out})
        m.refreshViewport()
    }
    return m, nil
}
```

Remove the obsolete `cmdNew` stub from Task 7 (replaced by `cmdNewWithWizard`).

- [ ] Add `cli.WithCreator` in `internal/channel/cli/cli.go`:

```go
// WithCreator wires the /new wizard's creatorBackend. When unset, /new
// activates the wizard but the final confirm step reports the backend is
// not configured (the create_skill/agent/command FuncTools still work
// regardless because they go through the agent, not the CLI).
func WithCreator(b creatorBackend) Option {
    return func(c *CLI) { c.creator = b }
}
```

And on `CLI`: `creator creatorBackend`. Thread it into the model in `Start`:

```go
m := newModel(...)
m.creator = c.creator
m.sched = c.sched
m.plugins = c.plugins
m.hookEntries = c.hookEntries
```

- [ ] Run `go test ./internal/channel/cli/... ./internal/creator/...` → PASS.

- [ ] Commit:
```
git add internal/creator/wizard.go internal/channel/cli/model.go internal/channel/cli/commands.go internal/channel/cli/commands_test.go internal/channel/cli/cli.go
git commit -m "$(cat <<'EOF'
feat(cli): add /new wizard state machine routed through creatorBackend

internal/creator/wizard.go owns the pure-data Wizard (Kind/Step/fields)
and Advance/Prompt helpers. internal/channel/cli adds an optional
*creator.Wizard field on the model; when active, submit() routes input
through Advance instead of /command dispatch or normal turn flow. /cancel
clears the wizard. /new kind [name] activates it; the final confirm step
calls the creatorBackend which cmd/czcli wires over the same Writer +
Reloader as the create_* tools so /new and natural-language requests
produce identical files.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

### Task 9 — main.go: wire creatorBackend + help text additive

**Files:** `cmd/czcli/main.go`, `internal/channel/cli/help.go` (NEW or amend) or `internal/channel/cli/commands.go` (help-overlay additive depending on Plan 11's landing surface)

- [ ] Add the `creatorAdapter` type in `cmd/czcli/main.go` that wraps `creator.Writer` + `creator.Reloader` and satisfies `cli.creatorBackend`:

```go
// creatorAdapter satisfies cli.creatorBackend by delegating to the shared
// Writer + Reloader the create_* FuncTools use. Both /new wizard finalizes
// and natural-language create_* calls produce identical files this way.
type creatorAdapter struct {
    writer   creator.Writer
    reloader creator.Reloader
}

func (a creatorAdapter) CreateSkill(ctx context.Context, name, desc, body string) (string, error) {
    path, err := a.writer.WriteSkill(name, desc, body, false)
    if err != nil {
        return "", err
    }
    if err := a.reloader.Rebuild(ctx); err != nil {
        return path, fmt.Errorf("wrote %s but reload failed: %w", path, err)
    }
    return path, nil
}

func (a creatorAdapter) CreateAgent(ctx context.Context, name, desc string, tools []string, body string) (string, error) {
    path, err := a.writer.WriteAgent(name, desc, tools, nil, body, false)
    if err != nil {
        return "", err
    }
    if err := a.reloader.Rebuild(ctx); err != nil {
        return path, fmt.Errorf("wrote %s but reload failed: %w", path, err)
    }
    return path, nil
}

func (a creatorAdapter) CreateCommand(ctx context.Context, name, desc, hint, body string) (string, error) {
    path, err := a.writer.WriteCommand(name, desc, hint, body, false)
    if err != nil {
        return "", err
    }
    if err := a.reloader.Rebuild(ctx); err != nil {
        return path, fmt.Errorf("wrote %s but reload failed: %w", path, err)
    }
    return path, nil
}
```

- [ ] Pass the adapter into `cli.New`:

```go
ch := cli.New(
    cli.WithSessionID("cli"),
    cli.WithScheduler(scheduleAdapter{store: store, sched: sched}),
    cli.WithPlugins(pluginsAdp),
    cli.WithHookEntries(hookEntries),
    cli.WithCreator(creatorAdapter{writer: writer, reloader: reloader}),
)
```

- [ ] Help-text additive. Plan 11's `Ctrl+/` overlay (per `02-tui-redesign-contracts.md` §CLI/keybindings) renders a help block. If Plan 11 has already shipped its help registry by the time Plan 12 lands, append two entries:
  - `/new skill|agent|command [name] — start the creator wizard`
  - `create_skill / create_agent / create_command — ask in natural language; the model calls these tools`

  If Plan 11 has NOT yet shipped the overlay or its help registry, add a minimal `internal/channel/cli/help.go` with a single exported `HelpText()` function that lists ALL slash commands + keybinds (so the overlay has something to render). Pseudocode shape:

```go
// help.go (added only if Plan 11 hasn't shipped a help registry yet)
package cli

// HelpText returns the static help block shown by the Ctrl+/ overlay. Plan 12
// adds /new + create_* entries; Plans 10/11 contribute their keybinds and
// /theme + /reload entries via the same function (merge on land).
func HelpText() string {
    return strings.Join([]string{
        "Keybinds:",
        "  Enter           send",
        "  Shift+Enter     newline (Plan 11)",
        "  Ctrl+L          /model picker (Plan 11)",
        "  Ctrl+R          /reload (Plan 11)",
        "  Ctrl+T          cycle themes (Plan 10)",
        "  Ctrl+/          toggle this help (Plan 11)",
        "  Ctrl+C, Esc     quit",
        "",
        "Slash commands:",
        "  /stats /tools /agents /schedule /model /skills /mcp /lsp /plugin /hooks",
        "  /new skill|agent|command [name]   start the creator wizard",
        "  /cancel                            cancel the active wizard",
        "",
        "Tools (ask in natural language; the agent calls them):",
        "  create_skill   create_agent   create_command",
    }, "\n")
}
```

- [ ] Run `go build ./... && go test -count=1 ./... && golangci-lint run ./... && go mod tidy` → all clean.

- [ ] Run `go run ./cmd/czcli` against a clean HOME (`HOME=$(mktemp -d) CZCLI_CONFIG= go run ./cmd/czcli`) → first-run path writes the default config and exits cleanly (existing behavior; Plan 12 doesn't affect first-run gating).

- [ ] Commit:
```
git add cmd/czcli/main.go internal/channel/cli/help.go
git commit -m "$(cat <<'EOF'
feat(czcli): wire creator backend into CLI; add help additive for /new

creatorAdapter satisfies cli.creatorBackend by delegating to the same
Writer + Reloader the create_* FuncTools use, so /new and natural-
language requests produce identical files and share a single reload
path. Help text lists /new + create_* alongside the existing slash
commands so users can discover the workflow via Ctrl+/.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Definition of done (final verification)

Run the following sequence on a clean checkout of `feat/tui-redesign` after the last commit:

```
go build ./...
go test -count=1 ./...
golangci-lint run ./...
go mod tidy && git diff --exit-code go.mod go.sum
HOME=$(mktemp -d) CZCLI_CONFIG= go run ./cmd/czcli      # writes default config, exits 0
```

All five must pass with no diff to `go.mod`/`go.sum`. Each task's commit lives on `feat/tui-redesign` with the `Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>` trailer.

## Open items to verify during implementation

1. **`dive.Tool.Call` shape on v1.7.0.** The tools_test uses `tl.Call(ctx, []byte(jsonInput))` to drive each FuncTool. Verify the exact entry point at `$(go env GOMODCACHE)/github.com/deepnoodle-ai/dive@v1.7.0/tool.go` and adjust the test helper if the surface is `Invoke` / `Run` instead. (The FuncTool internal closure receives `*createSkillInput` because dive's JSON adapter decodes input bytes into the typed parameter; this is the same path `internal/tools/recall.go` already relies on.)

2. **`dive.ToolResult.IsError` field.** Verify the field name (could be `Error` or `IsError`). If different, update the tools_test assertions accordingly. The contract is "tool failure must be a tool-result error, not a Go error" — whichever flag name dive uses for that distinction.

3. **`dive.ToolResult.Content[i].Text` field shape.** Verify against `tool.go`. If content blocks are typed (`TextBlock`, `ImageBlock`), adjust `toolResultText` to switch on the concrete type.

4. **Wizard model surgery vs Plan 11's textarea swap.** Plan 11 replaces `textinput.Model` with `textarea.Model`. The wizard test helper (`feedLine`) sets the input value via `m.input.SetValue` — verify both `textinput` and `textarea` expose `SetValue`. If Plan 11 has landed first, the test helper signature is identical; if not, the helper works against `textinput` until Plan 11 swaps.

5. **`/cancel` collision with Plan 11.** If Plan 11 introduces its own `/cancel` semantics (e.g. cancelling an in-flight turn), Plan 12's wizard cancel must coexist — only consume `/cancel` when `m.wizard != nil`, fall through to Plan 11's handling otherwise. The current Task 8 dispatcher already does this (`if m.wizard != nil` then cancel-wizard, else "nothing to cancel"; Plan 11 can swap the else branch for its turn-cancel hook).

6. **`pluginAdapter` value vs pointer receivers.** The current code uses value receivers (`func (a pluginAdapter) ...`). Adding `creatorTools` and `reloader` as fields keeps that — both fields are interface / slice types that survive value copies. If Plan 11 changes the receiver to pointer, the captured `reloader.update(...)` call still works because `*assistantReloader` is the pointer type from the start.

## Backward compatibility

- Greenfield package; nothing in `internal/creator` to migrate.
- `internal/agent.Build` / `BuildWithMCPInfos` / `Rebuild` gain a trailing param. The single sweep in Task 5 updates `cmd/czcli/main.go` to pass `nil` (which Task 6 replaces with the real `creatorTools`). Any external consumer of `agent.Build*` (none today) would need to add one trailing argument.
- `internal/channel/cli.New` gains `WithCreator(...)`; existing callers continue to compile (it's an optional functional option). When unwired, `/new` reports the missing backend instead of crashing.
- `internal/plugins` gains one exported test shim `SplitFrontmatterForTest`; no behavior change. Lives in a separate file so it's easy to delete if the round-trip test eventually moves into the plugins package itself.
