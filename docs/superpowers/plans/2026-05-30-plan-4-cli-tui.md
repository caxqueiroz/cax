# Plan 4: CLI/TUI Channel — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Implement a Bubble Tea TUI that satisfies `channel.Channel` — a top status bar (model + fallback + context gauge), a scrolling conversation pane, a bottom status bar (1d/1w/1m token usage + memory size + tool/subagent counts), and an input line that streams the agent's reply live and supports the slash-commands `/stats`, `/tools`, `/agents`, `/schedule`, `/model` — then wire `cmd/czcli/main.go` to launch it.

**Architecture:** A single `tea.Model` (`internal/channel/cli/model.go`) holds all UI state (viewport history, textinput, the latest `channel.Status`, the in-flight streaming buffer, and the running-subagent list). `cli.go` owns the `*CLI` type implementing `channel.Channel.Start`; it runs the bubbletea program on the main goroutine and, on each submitted line, spawns a worker goroutine that calls the `channel.Handler` with a `channel.EventSink` whose callbacks `p.Send(...)` custom `tea.Msg`s back into the model (live deltas), then refreshes the dashboard via the `channel.StatusFunc`. View rendering (`view.go`) and slash-command handling (`commands.go`) are pure functions so they unit-test as `Update`/`View` transitions without a real terminal.

**Tech Stack:** Go 1.24+; `github.com/charmbracelet/bubbletea@v1.3.10`, `github.com/charmbracelet/bubbles@v1.0.0` (`textinput`, `viewport`), `github.com/charmbracelet/lipgloss@v1.1.0`; depends on Plan 1 `internal/channel` interfaces and Plan 3 `agent.Assistant` (`Handle`, `Status`).

---

## File Structure

```
internal/channel/cli/
├── cli.go            # CLI type: New(...) + Start(ctx, handle, status); bubbletea program + EventSink bridge
├── cli_test.go       # Start wiring smoke test (program construction, message plumbing via exported helpers)
├── model.go          # tea.Model: state + Init/Update/View; custom tea.Msg types
├── model_test.go     # Update transition tests (key submit, stream deltas, status refresh, scroll)
├── view.go           # lipgloss rendering: top bar, viewport, bottom bar, input; gauge + humanize
├── view_test.go      # View() substring assertions + gauge threshold coloring
├── commands.go       # slash-command parse + handlers (/stats /tools /agents /schedule /model)
├── commands_test.go  # parse table + handler-output assertions
├── humanize.go       # humanizeTokens / humanizeBytes helpers
└── humanize_test.go  # table-driven helper tests
cmd/czcli/main.go     # construct CLI channel, call Start with assistant.Handle / assistant.Status
```

**Assumed-existing contract types (do NOT redefine — import from Plan 1 `internal/channel`):**

```go
package channel

type Message struct { SessionID, Text string }
type Reply struct { Text string }
type StreamEvent struct { Type string; Text string } // Type ∈ text|tool_start|tool_end|subagent_start|subagent_end|error
type EventSink func(ev StreamEvent)
type Handler func(ctx context.Context, msg Message, emit EventSink) (Reply, error)
type UsageTotals struct { InputTokens, OutputTokens int }
type UsageRollup struct { Day, Week, Month UsageTotals }
type Status struct {
    Provider string; Model string; OnFallback bool; FallbackIndex int
    ContextTokens int; ContextBudget int
    Usage UsageRollup
    MemSizeBytes int64; MessageCount int; MemoryCount int
    ToolNames []string; SubagentNames []string; RunningSubagents []string
}
type StatusFunc func(ctx context.Context) (Status, error)
type Channel interface {
    Start(ctx context.Context, handle Handler, status StatusFunc) error
}
```

**Module path:** `github.com/caxqueiroz/czcli`. Package for all files under `internal/channel/cli/` is `package cli`.

**Verified API facts (charmbracelet, 2026-05, versions above):**
- `tea.Model` = `Init() tea.Cmd`, `Update(tea.Msg) (tea.Model, tea.Cmd)` (value receiver, returns `tea.Model`), `View() string`.
- `tea.NewProgram(model, opts...) *tea.Program`; `(*Program).Run() (tea.Model, error)`; `(*Program).Send(msg tea.Msg)`; `(*Program).Quit()`; `tea.WithAltScreen()`; `tea.Quit` (a `tea.Cmd`); `tea.Batch(...tea.Cmd) tea.Cmd`; `tea.Tick(d, fn) tea.Cmd`.
- `tea.KeyMsg` has `.String() string` and `.Type` (`tea.KeyEnter`, `tea.KeyCtrlC`); `tea.WindowSizeMsg{Width, Height int}`.
- `textinput.New() textinput.Model`; fields `Prompt`, `Placeholder`, `CharLimit`, `Width`; methods `Focus() tea.Cmd`, `SetValue`, `Value() string`, `Reset()`, `Update(tea.Msg) (Model, tea.Cmd)`, `View() string`.
- `viewport.New(width, height int) viewport.Model`; fields `Width`, `Height`, `Style`; methods `SetContent(string)`, `GotoBottom() []string`, `Update(tea.Msg) (Model, tea.Cmd)`, `View() string`.
- `lipgloss.NewStyle()`, chainable `.Foreground(lipgloss.Color)`, `.Bold(bool)`, `.Width(int)`, `.Padding(...)`, `.Border(...)`, `.Render(...string) string`; `lipgloss.JoinVertical(pos, ...string)`, `lipgloss.JoinHorizontal(pos, ...string)`, `lipgloss.Width(str) int`, `lipgloss.Color(string)`.

**Testing approach:** `teatest` (`github.com/charmbracelet/x/exp/teatest`) is NOT in the local module cache, so this plan uses **pure `Update`/`View` unit tests** — construct the model, feed synthetic `tea.Msg`s (including `tea.KeyMsg`, `tea.WindowSizeMsg`, and our custom stream/status messages) through `Update`, then assert substrings of `View()`. This is fully deterministic, needs no PTY, and does not add a dependency. (If a future task wants golden-file end-to-end coverage, `teatest` can be added then; it is intentionally out of scope here.)

---

### Task 1: Humanize helpers (tokens + bytes)

**Files:** `internal/channel/cli/humanize.go`, `internal/channel/cli/humanize_test.go`

Small pure functions for the status bars: `humanizeTokens` renders `124k`, `3.2M`, `812` (no unit suffix; integers under 1000 are plain, k/M with one decimal trimmed of trailing `.0`), and `humanizeBytes` renders `18MB`, `512KB`, `2.0GB`.

- [ ] **Failing test** — create `internal/channel/cli/humanize_test.go`:
  ```go
  package cli

  import "testing"

  func TestHumanizeTokens(t *testing.T) {
      cases := []struct {
          in   int
          want string
      }{
          {0, "0"},
          {7, "7"},
          {812, "812"},
          {999, "999"},
          {1000, "1.0k"},
          {1234, "1.2k"},
          {124000, "124k"},
          {812345, "812k"},
          {1500000, "1.5M"},
          {3200000, "3.2M"},
          {1000000000, "1.0G"},
      }
      for _, c := range cases {
          if got := humanizeTokens(c.in); got != c.want {
              t.Errorf("humanizeTokens(%d) = %q, want %q", c.in, got, c.want)
          }
      }
  }

  func TestHumanizeBytes(t *testing.T) {
      cases := []struct {
          in   int64
          want string
      }{
          {0, "0B"},
          {512, "512B"},
          {1024, "1KB"},
          {1536, "1.5KB"},
          {18 * 1024 * 1024, "18MB"},
          {int64(2.0 * 1024 * 1024 * 1024), "2GB"},
      }
      for _, c := range cases {
          if got := humanizeBytes(c.in); got != c.want {
              t.Errorf("humanizeBytes(%d) = %q, want %q", c.in, got, c.want)
          }
      }
  }
  ```
- [ ] **Run + FAIL:** `go test ./internal/channel/cli/ -run 'TestHumanize'` (build error: undefined functions).
- [ ] **Minimal impl** — create `internal/channel/cli/humanize.go`:
  ```go
  package cli

  import (
      "fmt"
      "strings"
  )

  // humanizeTokens renders a token count compactly: <1000 as-is, thousands as
  // "k", millions as "M", billions as "G". One decimal is kept only when the
  // value is below 10 of its unit; a trailing ".0" is preserved for exactly
  // round 1.0k/1.0M/1.0G to signal the unit boundary.
  func humanizeTokens(n int) string {
      if n < 1000 {
          return fmt.Sprintf("%d", n)
      }
      return scale(float64(n), []struct {
          div  float64
          unit string
      }{
          {1e9, "G"},
          {1e6, "M"},
          {1e3, "k"},
      })
  }

  // humanizeBytes renders a byte count as B/KB/MB/GB.
  func humanizeBytes(n int64) string {
      if n < 1024 {
          return fmt.Sprintf("%dB", n)
      }
      return scale(float64(n), []struct {
          div  float64
          unit string
      }{
          {1 << 30, "GB"},
          {1 << 20, "MB"},
          {1 << 10, "KB"},
      })
  }

  func scale(v float64, units []struct {
      div  float64
      unit string
  }) string {
      for _, u := range units {
          if v < u.div {
              continue
          }
          q := v / u.div
          // Keep one decimal only for small magnitudes (< 10 of the unit).
          if q < 10 {
              s := fmt.Sprintf("%.1f", q)
              return s + u.unit
          }
          return fmt.Sprintf("%.0f", q) + u.unit
      }
      // v smaller than the smallest unit divisor; fall back to integer.
      return strings.TrimSuffix(fmt.Sprintf("%.0f", v), ".0")
  }
  ```
- [ ] **Run + PASS:** `go test ./internal/channel/cli/ -run 'TestHumanize'`.
- [ ] **Commit:** `test(cli): add humanize helpers for tokens and bytes`.

---

### Task 2: Model state, custom messages, and Init

**Files:** `internal/channel/cli/model.go`, `internal/channel/cli/model_test.go`

Define the `tea.Model` struct, the custom `tea.Msg` types the EventSink/status worker will send, a `newModel` constructor, and `Init`. `Update`/`View` are stubbed minimally here and fleshed out in Tasks 3–5; keep this task compiling and testing only construction + `Init`.

- [ ] **Failing test** — create `internal/channel/cli/model_test.go`:
  ```go
  package cli

  import "testing"

  func TestNewModelDefaults(t *testing.T) {
      m := newModel(80, 24)
      if m.width != 80 || m.height != 24 {
          t.Fatalf("size = %dx%d, want 80x24", m.width, m.height)
      }
      if m.input.Value() != "" {
          t.Errorf("input should start empty, got %q", m.input.Value())
      }
      if len(m.history) != 0 {
          t.Errorf("history should start empty, got %d entries", len(m.history))
      }
      if m.streaming {
          t.Errorf("model should not start in streaming state")
      }
  }

  func TestInitReturnsCmd(t *testing.T) {
      m := newModel(80, 24)
      if cmd := m.Init(); cmd == nil {
          t.Errorf("Init should return a focus command, got nil")
      }
  }
  ```
- [ ] **Run + FAIL:** `go test ./internal/channel/cli/ -run 'TestNewModel|TestInit'`.
- [ ] **Minimal impl** — create `internal/channel/cli/model.go`:
  ```go
  package cli

  import (
      tea "github.com/charmbracelet/bubbletea"
      "github.com/charmbracelet/bubbles/textinput"
      "github.com/charmbracelet/bubbles/viewport"

      "github.com/caxqueiroz/czcli/internal/channel"
  )

  // historyEntry is one rendered conversation line ("you:" / "bot:" / system).
  type historyEntry struct {
      who  string // "you" | "bot" | "sys"
      text string
  }

  // --- custom messages pushed in from the worker goroutine via program.Send ---

  // streamDeltaMsg carries a streamed text fragment for the in-flight reply.
  type streamDeltaMsg struct{ text string }

  // toolEventMsg notes a tool starting/ending (Type from channel.StreamEvent).
  type toolEventMsg struct {
      kind string // "tool_start" | "tool_end"
      name string
  }

  // subagentEventMsg notes a subagent starting/ending.
  type subagentEventMsg struct {
      kind string // "subagent_start" | "subagent_end"
      name string
  }

  // turnDoneMsg signals the worker finished a turn with a final reply or error.
  type turnDoneMsg struct {
      reply string
      err   error
  }

  // statusMsg delivers a fresh dashboard snapshot.
  type statusMsg struct {
      status channel.Status
      err    error
  }

  // submitMsg is produced internally when the user presses Enter; it carries the
  // submitted line so cli.go can dispatch it to the worker. (Used in Task 6.)
  type submitMsg struct{ line string }

  // model is the bubbletea model for the CLI channel.
  type model struct {
      width  int
      height int

      input    textinput.Model
      viewport viewport.Model

      history   []historyEntry
      stream    string // in-flight assistant text being streamed
      streaming bool

      status     channel.Status
      hasStatus  bool
      running    []string // running subagent names (live)
      lastErr    string

      ready bool // viewport sized at least once
  }

  func newModel(width, height int) model {
      ti := textinput.New()
      ti.Prompt = "> "
      ti.Placeholder = "type a message, or /stats /tools /agents /schedule /model"
      ti.CharLimit = 4000

      vp := viewport.New(width, max(1, height-6))

      return model{
          width:    width,
          height:   height,
          input:    ti,
          viewport: vp,
      }
  }

  func (m model) Init() tea.Cmd {
      return m.input.Focus()
  }

  func max(a, b int) int {
      if a > b {
          return a
      }
      return b
  }
  ```
- [ ] **Add minimal `Update`/`View` stubs** so the file builds as a `tea.Model` (replaced in later tasks). Append to `model.go`:
  ```go
  func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
      return m, nil
  }

  func (m model) View() string {
      return ""
  }
  ```
- [ ] **Run + PASS:** `go test ./internal/channel/cli/ -run 'TestNewModel|TestInit'`.
- [ ] **Commit:** `feat(cli): add bubbletea model state and message types`.

---

### Task 3: View rendering — top bar, conversation, bottom bar, input

**Files:** `internal/channel/cli/view.go`, `internal/channel/cli/view_test.go`

Replace the `View()` stub with real lipgloss rendering matching the approved layout. Render functions are pure (take `model`, return string) so they assert easily. The context gauge bar colors amber at ≥75% and red at ≥90% of budget and appends `⚠` when amber/red.

- [ ] **Failing test** — create `internal/channel/cli/view_test.go`:
  ```go
  package cli

  import (
      "strings"
      "testing"

      "github.com/caxqueiroz/czcli/internal/channel"
  )

  func statusFixture() channel.Status {
      return channel.Status{
          Provider:      "anthropic",
          Model:         "claude-opus",
          OnFallback:    false,
          FallbackIndex: 0,
          ContextTokens: 6100,
          ContextBudget: 8000,
          Usage: channel.UsageRollup{
              Day:   channel.UsageTotals{InputTokens: 100000, OutputTokens: 24000},
              Week:  channel.UsageTotals{InputTokens: 700000, OutputTokens: 112000},
              Month: channel.UsageTotals{InputTokens: 3000000, OutputTokens: 200000},
          },
          MemSizeBytes: 18 * 1024 * 1024,
          MessageCount: 42,
          MemoryCount:  17,
          ToolNames:    []string{"a", "b", "c", "d", "e", "f", "g", "h"},
          SubagentNames: []string{"explore", "plan", "general"},
      }
  }

  func TestTopBarShowsModelAndGauge(t *testing.T) {
      m := newModel(80, 24)
      m.status = statusFixture()
      m.hasStatus = true
      bar := m.renderTopBar()
      for _, want := range []string{"claude-opus", "ctx", "8k", "76%"} {
          if !strings.Contains(bar, want) {
              t.Errorf("top bar missing %q\n%s", want, bar)
          }
      }
  }

  func TestTopBarFallbackIndicator(t *testing.T) {
      m := newModel(80, 24)
      s := statusFixture()
      s.OnFallback = true
      s.FallbackIndex = 2
      m.status = s
      m.hasStatus = true
      bar := m.renderTopBar()
      if !strings.Contains(bar, "fallback") {
          t.Errorf("expected fallback indicator, got\n%s", bar)
      }
  }

  func TestGaugeAmberWarningAtThreshold(t *testing.T) {
      // 6100/8000 = 76% → amber, must include the ⚠ marker.
      m := newModel(80, 24)
      m.status = statusFixture()
      m.hasStatus = true
      if !strings.Contains(m.renderTopBar(), "⚠") {
          t.Errorf("expected ⚠ at 76%% context usage")
      }
  }

  func TestBottomBarShowsUsageMemAndCounts(t *testing.T) {
      m := newModel(80, 24)
      m.status = statusFixture()
      m.hasStatus = true
      bar := m.renderBottomBar()
      for _, want := range []string{"1d", "124k", "1w", "1m", "mem", "18MB", "8", "3"} {
          if !strings.Contains(bar, want) {
              t.Errorf("bottom bar missing %q\n%s", want, bar)
          }
      }
  }

  func TestViewIncludesAllRegions(t *testing.T) {
      m := newModel(80, 24)
      m.status = statusFixture()
      m.hasStatus = true
      m.history = []historyEntry{{who: "you", text: "hey"}, {who: "bot", text: "hi!"}}
      m.refreshViewport()
      out := m.View()
      for _, want := range []string{"claude-opus", "you:", "hey", "bot:", "hi!", "1d", "mem", "> "} {
          if !strings.Contains(out, want) {
              t.Errorf("View missing %q", want)
          }
      }
  }
  ```
- [ ] **Run + FAIL:** `go test ./internal/channel/cli/ -run 'TestTopBar|TestGauge|TestBottomBar|TestViewIncludes'`.
- [ ] **Minimal impl** — create `internal/channel/cli/view.go`:
  ```go
  package cli

  import (
      "fmt"
      "strings"

      "github.com/charmbracelet/lipgloss"
  )

  const (
      gaugeAmberPct = 0.75
      gaugeRedPct   = 0.90
      gaugeCells    = 8
  )

  var (
      barStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("250"))
      okStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("42"))  // green
      amberStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("214")) // amber
      redStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("196")) // red
      dimStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("244"))
      youStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("39")).Bold(true)
      botStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("42")).Bold(true)
      sysStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("244")).Italic(true)
      sepStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("240"))
  )

  // renderTopBar: "claude-opus ✓ │ ctx 6.1k/8k ▓▓▓░ 76% ⚠".
  func (m model) renderTopBar() string {
      if !m.hasStatus {
          return barStyle.Width(m.width).Render("connecting…")
      }
      s := m.status

      modelPart := s.Model
      if s.OnFallback {
          modelPart = fmt.Sprintf("%s ⚠ fallback #%d", s.Model, s.FallbackIndex)
      } else {
          modelPart = okStyle.Render(s.Model + " ✓")
      }

      gauge := m.renderGauge(s.ContextTokens, s.ContextBudget)
      line := modelPart + sepStyle.Render(" │ ") + gauge
      return barStyle.Width(m.width).Render(line)
  }

  // renderGauge: "ctx 6.1k/8k ▓▓▓░ 76% [⚠]" with threshold coloring.
  func (m model) renderGauge(tokens, budget int) string {
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
      bar := strings.Repeat("▓", filled) + strings.Repeat("░", gaugeCells-filled)

      style := okStyle
      warn := ""
      switch {
      case pct >= gaugeRedPct:
          style = redStyle
          warn = " ⚠"
      case pct >= gaugeAmberPct:
          style = amberStyle
          warn = " ⚠"
      }

      pctStr := fmt.Sprintf("%d%%", int(pct*100))
      return fmt.Sprintf("ctx %s/%s %s %s%s",
          humanizeTokensTenths(tokens),
          humanizeTokens(budget),
          style.Render(bar),
          style.Render(pctStr),
          style.Render(warn),
      )
  }

  // renderBottomBar: "tok 1d124k 1w812k 1m3.2M·mem18MB·🔧8 🤖3".
  func (m model) renderBottomBar() string {
      if !m.hasStatus {
          return barStyle.Width(m.width).Render("")
      }
      s := m.status
      day := s.Usage.Day.InputTokens + s.Usage.Day.OutputTokens
      week := s.Usage.Week.InputTokens + s.Usage.Week.OutputTokens
      month := s.Usage.Month.InputTokens + s.Usage.Month.OutputTokens

      line := fmt.Sprintf("tok 1d%s 1w%s 1m%s·mem%s·🔧%d 🤖%d",
          humanizeTokens(day),
          humanizeTokens(week),
          humanizeTokens(month),
          humanizeBytes(s.MemSizeBytes),
          len(s.ToolNames),
          len(s.SubagentNames),
      )
      return dimStyle.Width(m.width).Render(line)
  }

  // renderConversation builds the body string fed to the viewport.
  func (m model) renderConversation() string {
      var b strings.Builder
      for _, h := range m.history {
          b.WriteString(renderEntry(h))
          b.WriteByte('\n')
      }
      if m.streaming {
          b.WriteString(botStyle.Render("bot: ") + m.stream)
          b.WriteByte('\n')
      }
      if m.lastErr != "" {
          b.WriteString(redStyle.Render("err: "+m.lastErr) + "\n")
      }
      return strings.TrimRight(b.String(), "\n")
  }

  func renderEntry(h historyEntry) string {
      switch h.who {
      case "you":
          return youStyle.Render("you: ") + h.text
      case "bot":
          return botStyle.Render("bot: ") + h.text
      default:
          return sysStyle.Render(h.text)
      }
  }

  // refreshViewport recomputes viewport content and pins to the bottom.
  func (m *model) refreshViewport() {
      m.viewport.SetContent(m.renderConversation())
      m.viewport.GotoBottom()
  }

  func (m model) View() string {
      sep := sepStyle.Render(strings.Repeat("─", m.width))
      return lipgloss.JoinVertical(
          lipgloss.Left,
          m.renderTopBar(),
          sep,
          m.viewport.View(),
          sep,
          m.renderBottomBar(),
          sep,
          m.input.View(),
      )
  }

  // humanizeTokensTenths renders e.g. 6100 → "6.1k" for the gauge numerator,
  // keeping a tenth even above 10k when below the next unit, so "6.1k" matches
  // the mockup. Falls back to humanizeTokens for sub-1000 values.
  func humanizeTokensTenths(n int) string {
      switch {
      case n < 1000:
          return fmt.Sprintf("%d", n)
      case n < 1_000_000:
          return strings.TrimSuffix(fmt.Sprintf("%.1f", float64(n)/1000) , ".0") + "k"
      default:
          return strings.TrimSuffix(fmt.Sprintf("%.1f", float64(n)/1_000_000), ".0") + "M"
      }
  }
  ```
  > Note: remove the placeholder `View()` stub from `model.go` (Task 2) when adding this real `View()` — `View` now lives only in `view.go`. Leave the `Update` stub in `model.go`; it is replaced in Task 4.
- [ ] **Run + PASS:** `go test ./internal/channel/cli/ -run 'TestTopBar|TestGauge|TestBottomBar|TestViewIncludes'`.
- [ ] **Add gauge-color unit test** — append to `view_test.go`:
  ```go
  func TestGaugeNoWarningBelowAmber(t *testing.T) {
      m := newModel(80, 24)
      s := statusFixture()
      s.ContextTokens = 4000 // 50%
      m.status = s
      m.hasStatus = true
      g := m.renderGauge(s.ContextTokens, s.ContextBudget)
      if strings.Contains(g, "⚠") {
          t.Errorf("did not expect ⚠ at 50%%: %s", g)
      }
      if !strings.Contains(g, "50%") {
          t.Errorf("expected 50%% in gauge: %s", g)
      }
  }

  func TestGaugeRedAtNinety(t *testing.T) {
      m := newModel(80, 24)
      s := statusFixture()
      s.ContextTokens = 7600 // 95%
      m.status = s
      m.hasStatus = true
      g := m.renderGauge(s.ContextTokens, s.ContextBudget)
      if !strings.Contains(g, "⚠") || !strings.Contains(g, "95%") {
          t.Errorf("expected red warning + 95%%: %s", g)
      }
  }
  ```
- [ ] **Run + PASS:** `go test ./internal/channel/cli/ -run 'TestGauge'`.
- [ ] **Commit:** `feat(cli): render top/bottom status bars, gauge, and conversation`.

---

### Task 4: Update — keys, resize, streaming, status messages

**Files:** `internal/channel/cli/model.go` (replace `Update` stub), `internal/channel/cli/model_test.go` (extend)

Implement `Update` to handle: window resize (re-size viewport, re-wrap), Ctrl+C / Esc → quit, Enter → emit `submitMsg` (and locally echo the user line + enter streaming state), the four custom stream messages (append deltas / mark tool/subagent activity), `turnDoneMsg` (finalize the bot entry, exit streaming, request status), and `statusMsg` (store snapshot, sync running subagents). Delegate other keys to `textinput` and scroll keys to `viewport`.

- [ ] **Failing test** — extend `internal/channel/cli/model_test.go`:
  ```go
  package cli

  import (
      "strings"
      "testing"

      tea "github.com/charmbracelet/bubbletea"

      "github.com/caxqueiroz/czcli/internal/channel"
  )

  func update(m model, msg tea.Msg) model {
      next, _ := m.Update(msg)
      return next.(model)
  }

  func TestWindowResizeSizesViewport(t *testing.T) {
      m := newModel(10, 10)
      m = update(m, tea.WindowSizeMsg{Width: 100, Height: 40})
      if m.width != 100 || m.height != 40 {
          t.Fatalf("size = %dx%d, want 100x40", m.width, m.height)
      }
      if m.viewport.Width != 100 {
          t.Errorf("viewport width = %d, want 100", m.viewport.Width)
      }
  }

  func TestEnterEchoesUserAndStreams(t *testing.T) {
      m := newModel(80, 24)
      m.input.SetValue("hey")
      m = update(m, tea.KeyMsg{Type: tea.KeyEnter})
      if !m.streaming {
          t.Errorf("expected streaming after Enter")
      }
      if len(m.history) != 1 || m.history[0].who != "you" || m.history[0].text != "hey" {
          t.Fatalf("expected echoed user line, got %+v", m.history)
      }
      if m.input.Value() != "" {
          t.Errorf("input should be cleared after submit, got %q", m.input.Value())
      }
  }

  func TestStreamDeltaAccumulates(t *testing.T) {
      m := newModel(80, 24)
      m.streaming = true
      m = update(m, streamDeltaMsg{text: "hel"})
      m = update(m, streamDeltaMsg{text: "lo"})
      if m.stream != "hello" {
          t.Errorf("stream = %q, want %q", m.stream, "hello")
      }
  }

  func TestTurnDoneFinalizesBotEntry(t *testing.T) {
      m := newModel(80, 24)
      m.streaming = true
      m.stream = "partial"
      m = update(m, turnDoneMsg{reply: "final answer"})
      if m.streaming {
          t.Errorf("should leave streaming state when turn done")
      }
      last := m.history[len(m.history)-1]
      if last.who != "bot" || last.text != "final answer" {
          t.Errorf("expected finalized bot entry, got %+v", last)
      }
      if m.stream != "" {
          t.Errorf("stream buffer should be cleared, got %q", m.stream)
      }
  }

  func TestSubagentEventTracksRunning(t *testing.T) {
      m := newModel(80, 24)
      m = update(m, subagentEventMsg{kind: "subagent_start", name: "explore"})
      if len(m.running) != 1 || m.running[0] != "explore" {
          t.Fatalf("expected running [explore], got %v", m.running)
      }
      m = update(m, subagentEventMsg{kind: "subagent_end", name: "explore"})
      if len(m.running) != 0 {
          t.Errorf("expected no running subagents, got %v", m.running)
      }
  }

  func TestStatusMsgStored(t *testing.T) {
      m := newModel(80, 24)
      m = update(m, statusMsg{status: channel.Status{Model: "gpt-x"}})
      if !m.hasStatus || m.status.Model != "gpt-x" {
          t.Errorf("expected stored status, got %+v hasStatus=%v", m.status, m.hasStatus)
      }
      if !strings.Contains(m.View(), "gpt-x") {
          t.Errorf("View should reflect new model name")
      }
  }
  ```
- [ ] **Run + FAIL:** `go test ./internal/channel/cli/ -run 'TestWindowResize|TestEnter|TestStreamDelta|TestTurnDone|TestSubagentEvent|TestStatusMsg'`.
- [ ] **Minimal impl** — replace the `Update` stub in `model.go`:
  ```go
  func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
      var cmds []tea.Cmd

      switch msg := msg.(type) {
      case tea.WindowSizeMsg:
          m.width = msg.Width
          m.height = msg.Height
          m.viewport.Width = msg.Width
          m.viewport.Height = max(1, msg.Height-6)
          m.input.Width = max(1, msg.Width-2)
          m.ready = true
          m.refreshViewport()
          return m, nil

      case tea.KeyMsg:
          switch msg.Type {
          case tea.KeyCtrlC, tea.KeyEsc:
              return m, tea.Quit
          case tea.KeyEnter:
              return m.submit()
          }

      case streamDeltaMsg:
          m.stream += msg.text
          m.refreshViewport()
          return m, nil

      case toolEventMsg:
          // Surfaced via /tools; no inline echo to keep the pane clean.
          return m, nil

      case subagentEventMsg:
          switch msg.kind {
          case "subagent_start":
              m.running = append(m.running, msg.name)
          case "subagent_end":
              m.running = removeFirst(m.running, msg.name)
          }
          return m, nil

      case turnDoneMsg:
          m.streaming = false
          if msg.err != nil {
              m.lastErr = msg.err.Error()
          } else {
              text := msg.reply
              if text == "" {
                  text = m.stream
              }
              m.history = append(m.history, historyEntry{who: "bot", text: text})
          }
          m.stream = ""
          m.refreshViewport()
          return m, requestStatus

      case statusMsg:
          if msg.err == nil {
              m.status = msg.status
              m.hasStatus = true
              if len(msg.status.RunningSubagents) > 0 {
                  m.running = append([]string(nil), msg.status.RunningSubagents...)
              }
          }
          return m, nil
      }

      // Delegate remaining keys to the input; scroll keys to the viewport.
      var cmd tea.Cmd
      m.input, cmd = m.input.Update(msg)
      cmds = append(cmds, cmd)
      m.viewport, cmd = m.viewport.Update(msg)
      cmds = append(cmds, cmd)
      return m, tea.Batch(cmds...)
  }

  // submit handles Enter: slash-commands are dispatched locally; plain text is
  // echoed and a submitMsg is emitted so cli.go can run the turn.
  func (m model) submit() (tea.Model, tea.Cmd) {
      line := strings.TrimSpace(m.input.Value())
      m.input.Reset()
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
      return m, emitSubmit(line)
  }

  func emitSubmit(line string) tea.Cmd {
      return func() tea.Msg { return submitMsg{line: line} }
  }

  // requestStatus is a sentinel cmd; cli.go intercepts the resulting marker by
  // running the StatusFunc out of band. It is implemented as a no-op message the
  // program loop ignores, while cli.go schedules an actual refresh after a turn.
  func requestStatus() tea.Msg { return statusRequestMsg{} }

  type statusRequestMsg struct{}

  func removeFirst(xs []string, v string) []string {
      for i, x := range xs {
          if x == v {
              return append(xs[:i:i], xs[i+1:]...)
          }
      }
      return xs
  }
  ```
  Add the missing imports to `model.go` (`strings`):
  ```go
  import (
      "strings"

      tea "github.com/charmbracelet/bubbletea"
      "github.com/charmbracelet/bubbles/textinput"
      "github.com/charmbracelet/bubbles/viewport"

      "github.com/caxqueiroz/czcli/internal/channel"
  )
  ```
  > `m.handleCommand` is defined in Task 5; `statusRequestMsg` will be observed by `cli.go` in Task 6 (it triggers an async status refresh). For this task, add a no-op branch so the program ignores it: in the `Update` switch, add `case statusRequestMsg: return m, nil` (cli.go overrides scheduling externally). To keep Task 4 self-contained and compiling before Task 5 lands, temporarily stub `handleCommand`:
  ```go
  // Temporary stub; replaced by commands.go in Task 5. Remove when Task 5 lands.
  func (m model) handleCommand(line string) (string, bool) { return "", false }
  ```
- [ ] **Run + PASS:** `go test ./internal/channel/cli/ -run 'TestWindowResize|TestEnter|TestStreamDelta|TestTurnDone|TestSubagentEvent|TestStatusMsg'`.
- [ ] **Commit:** `feat(cli): handle keys, resize, streaming and status in Update`.

---

### Task 5: Slash-command parsing and handlers

**Files:** `internal/channel/cli/commands.go`, `internal/channel/cli/commands_test.go`; remove the temporary `handleCommand` stub from `model.go`.

`handleCommand(line)` parses `/cmd args...`, dispatches to per-command renderers that read `m.status`, and returns `(output string, quit bool)`. `/quit` (alias for ctrl+c) returns `quit=true`. Commands: `/stats` (full dashboard text), `/tools` (tool list), `/agents` (personas + running), `/schedule` (note: read-only listing of `status.*` is not available, so `/schedule` returns guidance — schedule CRUD belongs to Plan 5; here it lists nothing or a hint), `/model` (active provider:model + fallback state).

- [ ] **Failing test** — create `internal/channel/cli/commands_test.go`:
  ```go
  package cli

  import (
      "strings"
      "testing"
  )

  func TestParseCommand(t *testing.T) {
      cases := []struct {
          in     string
          name   string
          args   string
      }{
          {"/stats", "stats", ""},
          {"/model", "model", ""},
          {"/schedule list", "schedule", "list"},
          {"/tools   verbose", "tools", "verbose"},
      }
      for _, c := range cases {
          name, args := parseCommand(c.in)
          if name != c.name || args != c.args {
              t.Errorf("parseCommand(%q) = (%q,%q), want (%q,%q)", c.in, name, args, c.name, c.args)
          }
      }
  }

  func TestStatsCommand(t *testing.T) {
      m := newModel(80, 24)
      m.status = statusFixture()
      m.hasStatus = true
      out, quit := m.handleCommand("/stats")
      if quit {
          t.Fatalf("/stats should not quit")
      }
      for _, want := range []string{"claude-opus", "ctx", "1d", "mem", "messages", "vectors"} {
          if !strings.Contains(out, want) {
              t.Errorf("/stats missing %q in:\n%s", want, out)
          }
      }
  }

  func TestToolsCommand(t *testing.T) {
      m := newModel(80, 24)
      m.status = statusFixture()
      m.hasStatus = true
      out, _ := m.handleCommand("/tools")
      for _, want := range []string{"a", "h", "8"} {
          if !strings.Contains(out, want) {
              t.Errorf("/tools missing %q in:\n%s", want, out)
          }
      }
  }

  func TestAgentsCommand(t *testing.T) {
      m := newModel(80, 24)
      s := statusFixture()
      s.RunningSubagents = []string{"explore"}
      m.status = s
      m.hasStatus = true
      out, _ := m.handleCommand("/agents")
      for _, want := range []string{"explore", "plan", "general", "running"} {
          if !strings.Contains(out, want) {
              t.Errorf("/agents missing %q in:\n%s", want, out)
          }
      }
  }

  func TestModelCommand(t *testing.T) {
      m := newModel(80, 24)
      s := statusFixture()
      s.OnFallback = true
      s.FallbackIndex = 1
      m.status = s
      m.hasStatus = true
      out, _ := m.handleCommand("/model")
      for _, want := range []string{"anthropic", "claude-opus", "fallback", "#1"} {
          if !strings.Contains(out, want) {
              t.Errorf("/model missing %q in:\n%s", want, out)
          }
      }
  }

  func TestUnknownCommand(t *testing.T) {
      m := newModel(80, 24)
      out, _ := m.handleCommand("/nope")
      if !strings.Contains(out, "unknown") {
          t.Errorf("expected unknown-command hint, got %q", out)
      }
  }

  func TestQuitCommand(t *testing.T) {
      m := newModel(80, 24)
      _, quit := m.handleCommand("/quit")
      if !quit {
          t.Errorf("/quit should return quit=true")
      }
  }
  ```
- [ ] **Run + FAIL:** `go test ./internal/channel/cli/ -run 'TestParseCommand|TestStatsCommand|TestToolsCommand|TestAgentsCommand|TestModelCommand|TestUnknownCommand|TestQuitCommand'`.
- [ ] **Remove the temporary `handleCommand` stub** added in Task 4 from `model.go`.
- [ ] **Minimal impl** — create `internal/channel/cli/commands.go`:
  ```go
  package cli

  import (
      "fmt"
      "strings"
  )

  // parseCommand splits "/name args..." into ("name", "args"). The leading slash
  // is stripped; surrounding whitespace in args is trimmed.
  func parseCommand(line string) (name, args string) {
      line = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "/"))
      if line == "" {
          return "", ""
      }
      if i := strings.IndexFunc(line, func(r rune) bool { return r == ' ' || r == '\t' }); i >= 0 {
          return line[:i], strings.TrimSpace(line[i+1:])
      }
      return line, ""
  }

  // handleCommand dispatches a slash command and returns display output plus a
  // quit flag. It reads only the latest status snapshot already in the model.
  func (m model) handleCommand(line string) (string, bool) {
      name, args := parseCommand(line)
      switch name {
      case "quit", "exit":
          return "", true
      case "stats":
          return m.cmdStats(), false
      case "tools":
          return m.cmdTools(), false
      case "agents":
          return m.cmdAgents(), false
      case "schedule":
          return m.cmdSchedule(args), false
      case "model":
          return m.cmdModel(), false
      default:
          return fmt.Sprintf("unknown command /%s — try /stats /tools /agents /schedule /model", name), false
      }
  }

  func (m model) cmdStats() string {
      if !m.hasStatus {
          return "stats unavailable (no status yet)"
      }
      s := m.status
      day := s.Usage.Day.InputTokens + s.Usage.Day.OutputTokens
      week := s.Usage.Week.InputTokens + s.Usage.Week.OutputTokens
      month := s.Usage.Month.InputTokens + s.Usage.Month.OutputTokens
      var b strings.Builder
      fmt.Fprintf(&b, "model:   %s:%s\n", s.Provider, s.Model)
      fmt.Fprintf(&b, "context: %s/%s (%d%%)\n", humanizeTokensTenths(s.ContextTokens), humanizeTokens(s.ContextBudget), pctOf(s.ContextTokens, s.ContextBudget))
      fmt.Fprintf(&b, "tokens:  1d %s · 1w %s · 1m %s\n", humanizeTokens(day), humanizeTokens(week), humanizeTokens(month))
      fmt.Fprintf(&b, "memory:  %s · %d messages · %d vectors\n", humanizeBytes(s.MemSizeBytes), s.MessageCount, s.MemoryCount)
      fmt.Fprintf(&b, "tools:   %d · subagents %d", len(s.ToolNames), len(s.SubagentNames))
      return b.String()
  }

  func (m model) cmdTools() string {
      if !m.hasStatus || len(m.status.ToolNames) == 0 {
          return "no tools registered"
      }
      return fmt.Sprintf("tools (%d): %s", len(m.status.ToolNames), strings.Join(m.status.ToolNames, ", "))
  }

  func (m model) cmdAgents() string {
      if !m.hasStatus {
          return "no subagent info yet"
      }
      var b strings.Builder
      fmt.Fprintf(&b, "personas (%d): %s\n", len(m.status.SubagentNames), strings.Join(m.status.SubagentNames, ", "))
      running := m.running
      if len(running) == 0 {
          running = m.status.RunningSubagents
      }
      if len(running) == 0 {
          b.WriteString("running: none")
      } else {
          fmt.Fprintf(&b, "running: %s", strings.Join(running, ", "))
      }
      return b.String()
  }

  func (m model) cmdSchedule(args string) string {
      // Schedule CRUD is owned by Plan 5 (scheduler + store-backed persistence).
      // The TUI shows guidance here; wiring a store-backed handler is a Plan 5
      // follow-up that can replace this body.
      if args == "" {
          return "schedule: usage /schedule list (scheduler managed by config + Plan 5)"
      }
      return fmt.Sprintf("schedule %q: not yet wired in the TUI (see Plan 5)", args)
  }

  func (m model) cmdModel() string {
      if !m.hasStatus {
          return "model unavailable (no status yet)"
      }
      s := m.status
      if s.OnFallback {
          return fmt.Sprintf("active: %s:%s (⚠ fallback #%d)", s.Provider, s.Model, s.FallbackIndex)
      }
      return fmt.Sprintf("active: %s:%s (✓ primary)", s.Provider, s.Model)
  }

  func pctOf(n, d int) int {
      if d <= 0 {
          return 0
      }
      p := int(float64(n) / float64(d) * 100)
      if p > 100 {
          return 100
      }
      return p
  }
  ```
- [ ] **Run + PASS:** `go test ./internal/channel/cli/ -run 'TestParseCommand|TestStatsCommand|TestToolsCommand|TestAgentsCommand|TestModelCommand|TestUnknownCommand|TestQuitCommand'`.
- [ ] **Run the whole package** to confirm Tasks 1–5 integrate: `go test ./internal/channel/cli/`.
- [ ] **Commit:** `feat(cli): add slash-command parsing and handlers`.

---

### Task 6: CLI type — Start, EventSink bridge, status refresh

**Files:** `internal/channel/cli/cli.go`, `internal/channel/cli/cli_test.go`

`New(...) *CLI` constructs the channel (configurable session ID + status-refresh interval). `Start(ctx, handle, status)` builds the program, runs the worker bridge, and blocks on the bubbletea loop. The bridge: when a `submitMsg` reaches the program, a goroutine calls `handle(ctx, msg, emit)` where `emit` translates each `channel.StreamEvent` into a `p.Send(...)` of the matching custom message; on completion it sends `turnDoneMsg` then refreshes status via `status(ctx)` → `p.Send(statusMsg{...})`. A periodic `tea.Tick` (or a background ticker that calls `status`) keeps the dashboard fresh while idle. Because `submitMsg` originates inside `Update` (emitted by `emitSubmit`), the program must route it to the worker — do this by wrapping the model so `cli.go` observes submissions: the simplest correct approach is a small `programModel` that embeds `model` and, in its `Update`, intercepts `submitMsg`/`statusRequestMsg` to launch goroutines via a captured `*tea.Program` and the handler/status funcs.

- [ ] **Failing test** — create `internal/channel/cli/cli_test.go`:
  ```go
  package cli

  import (
      "context"
      "testing"
      "time"

      "github.com/caxqueiroz/czcli/internal/channel"
  )

  func TestNewCLIDefaults(t *testing.T) {
      c := New()
      if c.sessionID == "" {
          t.Errorf("expected a default session id")
      }
      if c.statusInterval <= 0 {
          t.Errorf("expected a positive status interval")
      }
  }

  func TestNewCLIOptions(t *testing.T) {
      c := New(WithSessionID("sess-1"), WithStatusInterval(7*time.Second))
      if c.sessionID != "sess-1" {
          t.Errorf("session id = %q, want sess-1", c.sessionID)
      }
      if c.statusInterval != 7*time.Second {
          t.Errorf("interval = %v, want 7s", c.statusInterval)
      }
  }

  // runTurn exercises the worker bridge without a live terminal: it feeds a
  // submitMsg through a captured sink recorder and asserts the emitted messages.
  func TestRunTurnEmitsStreamAndDone(t *testing.T) {
      var sent []tea.Msg
      send := func(m tea.Msg) { sent = append(sent, m) }

      handle := func(ctx context.Context, msg channel.Message, emit channel.EventSink) (channel.Reply, error) {
          emit(channel.StreamEvent{Type: "text", Text: "hel"})
          emit(channel.StreamEvent{Type: "text", Text: "lo"})
          emit(channel.StreamEvent{Type: "subagent_start", Text: "explore"})
          emit(channel.StreamEvent{Type: "subagent_end", Text: "explore"})
          return channel.Reply{Text: "hello!"}, nil
      }
      status := func(ctx context.Context) (channel.Status, error) {
          return channel.Status{Model: "m"}, nil
      }

      c := New(WithSessionID("s"))
      c.runTurn(context.Background(), send, handle, status, "hi")

      var gotDelta, gotDone, gotStatus, gotSubStart bool
      for _, m := range sent {
          switch mm := m.(type) {
          case streamDeltaMsg:
              gotDelta = true
          case turnDoneMsg:
              gotDone = true
              if mm.reply != "hello!" {
                  t.Errorf("turnDone reply = %q, want hello!", mm.reply)
              }
          case statusMsg:
              gotStatus = true
          case subagentEventMsg:
              if mm.kind == "subagent_start" {
                  gotSubStart = true
              }
          }
      }
      if !gotDelta || !gotDone || !gotStatus || !gotSubStart {
          t.Errorf("missing messages: delta=%v done=%v status=%v subStart=%v", gotDelta, gotDone, gotStatus, gotSubStart)
      }
  }
  ```
  Add the import for `tea` to the test file:
  ```go
  import (
      "context"
      "testing"
      "time"

      tea "github.com/charmbracelet/bubbletea"

      "github.com/caxqueiroz/czcli/internal/channel"
  )
  ```
- [ ] **Run + FAIL:** `go test ./internal/channel/cli/ -run 'TestNewCLI|TestRunTurn'`.
- [ ] **Minimal impl** — create `internal/channel/cli/cli.go`:
  ```go
  package cli

  import (
      "context"
      "fmt"
      "time"

      tea "github.com/charmbracelet/bubbletea"

      "github.com/caxqueiroz/czcli/internal/channel"
  )

  // CLI is the Bubble Tea implementation of channel.Channel.
  type CLI struct {
      sessionID      string
      statusInterval time.Duration
  }

  // Option configures a CLI.
  type Option func(*CLI)

  // WithSessionID sets the session id used for every inbound message.
  func WithSessionID(id string) Option { return func(c *CLI) { c.sessionID = id } }

  // WithStatusInterval sets how often the dashboard is refreshed while idle.
  func WithStatusInterval(d time.Duration) Option {
      return func(c *CLI) { c.statusInterval = d }
  }

  // New builds a CLI channel with sensible defaults.
  func New(opts ...Option) *CLI {
      c := &CLI{
          sessionID:      "cli",
          statusInterval: 5 * time.Second,
      }
      for _, o := range opts {
          o(c)
      }
      return c
  }

  // sender abstracts (*tea.Program).Send for testability.
  type sender func(msg tea.Msg)

  // tickMsg drives idle status refreshes.
  type tickMsg struct{}

  // Start runs the bubbletea program, bridging inbound lines to handle and
  // refreshing status via status. It blocks until ctx is cancelled or the user
  // quits.
  func (c *CLI) Start(ctx context.Context, handle channel.Handler, status channel.StatusFunc) error {
      pm := &programModel{
          model:  newModel(80, 24),
          cli:    c,
          ctx:    ctx,
          handle: handle,
          status: status,
      }
      p := tea.NewProgram(pm, tea.WithAltScreen())
      pm.send = p.Send

      // Cancel the program when the context ends.
      go func() {
          <-ctx.Done()
          p.Quit()
      }()

      // Prime an initial status snapshot.
      go func() {
          if st, err := status(ctx); err == nil {
              p.Send(statusMsg{status: st})
          }
      }()

      _, err := p.Run()
      if err != nil {
          return fmt.Errorf("running cli program: %w", err)
      }
      return nil
  }

  // runTurn executes one turn: it calls handle with an EventSink that forwards
  // stream events as tea messages, then sends turnDoneMsg and a fresh status.
  func (c *CLI) runTurn(ctx context.Context, send sender, handle channel.Handler, status channel.StatusFunc, line string) {
      emit := func(ev channel.StreamEvent) {
          switch ev.Type {
          case "text":
              send(streamDeltaMsg{text: ev.Text})
          case "tool_start", "tool_end":
              send(toolEventMsg{kind: ev.Type, name: ev.Text})
          case "subagent_start", "subagent_end":
              send(subagentEventMsg{kind: ev.Type, name: ev.Text})
          case "error":
              send(streamDeltaMsg{text: "\n[error] " + ev.Text})
          }
      }
      reply, err := handle(ctx, channel.Message{SessionID: c.sessionID, Text: line}, emit)
      send(turnDoneMsg{reply: reply.Text, err: err})
      if st, serr := status(ctx); serr == nil {
          send(statusMsg{status: st})
      }
  }
  ```
- [ ] **Add the program wrapper** — append `programModel` to `cli.go`:
  ```go
  // programModel embeds the pure model and intercepts the side-effecting messages
  // (submitMsg, statusRequestMsg, tickMsg) to launch goroutines that talk to the
  // handler/status funcs via the captured program sender. Pure UI messages fall
  // through to model.Update so view/update logic stays testable in isolation.
  type programModel struct {
      model  model
      cli    *CLI
      ctx    context.Context
      send   sender
      handle channel.Handler
      status channel.StatusFunc
  }

  func (pm *programModel) Init() tea.Cmd {
      return tea.Batch(pm.model.Init(), tea.Tick(pm.cli.statusInterval, func(time.Time) tea.Msg { return tickMsg{} }))
  }

  func (pm *programModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
      switch m := msg.(type) {
      case submitMsg:
          line := m.line
          go pm.cli.runTurn(pm.ctx, pm.send, pm.handle, pm.status, line)
          return pm, nil
      case statusRequestMsg:
          go func() {
              if st, err := pm.status(pm.ctx); err == nil {
                  pm.send(statusMsg{status: st})
              }
          }()
          return pm, nil
      case tickMsg:
          go func() {
              if st, err := pm.status(pm.ctx); err == nil {
                  pm.send(statusMsg{status: st})
              }
          }()
          return pm, tea.Tick(pm.cli.statusInterval, func(time.Time) tea.Msg { return tickMsg{} })
      }
      next, cmd := pm.model.Update(msg)
      pm.model = next.(model)
      return pm, cmd
  }

  func (pm *programModel) View() string { return pm.model.View() }
  ```
  > `Start` constructs `pm` first, then sets `pm.send = p.Send` after `NewProgram`, because `Send` needs the program. This is safe: `Send` is only invoked from goroutines launched inside `Update`, which cannot run before `p.Run()` starts.
- [ ] **Run + PASS:** `go test ./internal/channel/cli/ -run 'TestNewCLI|TestRunTurn'`.
- [ ] **Confirm `var _ channel.Channel = (*CLI)(nil)`** compiles — add this assertion near the top of `cli.go` and run `go build ./internal/channel/cli/`.
- [ ] **Run the whole package:** `go test ./internal/channel/cli/`.
- [ ] **Commit:** `feat(cli): add CLI channel Start with streaming bridge and status refresh`.

---

### Task 7: Wire `cmd/czcli/main.go` to launch the TUI channel

**Files:** `cmd/czcli/main.go`

Replace the plain stdin/stdout loop with construction of the CLI channel and a call to `Start` using the assistant's `Handle` and `Status`. This task depends on Plan 1 (`config.Load`, `agent.BuildModel`), Plan 2 (`memory.Open`, an `Embedder`), and Plan 3 (`agent.Build`, `assistant.Handle`, `assistant.Status`). Reference those by their contract signatures; if an earlier plan has not landed, this task's `go build ./...` will fail on the missing symbols, which is expected until the dependency exists. Keep the wiring minimal and correct.

> Because `main.go` integrates symbols owned by other plans, its only "test" is `go build ./...` plus the manual run below. Do not add a unit test for `main`.

- [ ] **Implement** — write `cmd/czcli/main.go`:
  ```go
  package main

  import (
      "context"
      "flag"
      "log/slog"
      "os"
      "os/signal"
      "syscall"

      "github.com/caxqueiroz/czcli/internal/agent"
      "github.com/caxqueiroz/czcli/internal/channel/cli"
      "github.com/caxqueiroz/czcli/internal/config"
      "github.com/caxqueiroz/czcli/internal/memory"
  )

  func main() {
      cfgPath := flag.String("config", "config.yaml", "path to config file")
      flag.Parse()

      logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
      slog.SetDefault(logger)

      if err := run(*cfgPath); err != nil {
          slog.Error("czcli exited with error", "err", err)
          os.Exit(1)
      }
  }

  func run(cfgPath string) error {
      ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
      defer cancel()

      cfg, err := config.Load(cfgPath)
      if err != nil {
          return err
      }

      embedder, err := memory.NewEmbedder(cfg.Embeddings) // Plan 2 constructor
      if err != nil {
          return err
      }

      store, err := memory.Open(cfg.Memory, embedder)
      if err != nil {
          return err
      }
      defer store.Close()

      model, err := agent.BuildModel(cfg) // Plan 1
      if err != nil {
          return err
      }

      assistant, err := agent.Build(ctx, cfg, store, model) // Plan 3
      if err != nil {
          return err
      }

      ch := cli.New(cli.WithSessionID("cli"))
      return ch.Start(ctx, assistant.Handle, assistant.Status)
  }
  ```
  > Assumption: Plan 2 exposes an embedder constructor `memory.NewEmbedder(config.EmbeddingsConfig) (memory.Embedder, error)`. The contracts list `memory.Embedder` and note implementations live in `embedder.go` but do not pin a constructor name; if Plan 2 names it differently (e.g. `memory.OpenAIEmbedder(...)`), adjust this one line. This is the only contract ambiguity touched by this plan.
- [ ] **Build:** `go build ./...` (passes once Plans 1–3 symbols exist; until then, expect undefined-symbol errors only from those packages, not from `internal/channel/cli`).
- [ ] **Run package tests once more:** `go test ./internal/channel/cli/`.
- [ ] **Commit:** `feat(cli): launch TUI channel from main`.

---

### Task 8: Manual verification (TUI behavior that cannot be unit-tested)

**Files:** none (verification only)

The bubbletea render loop, terminal sizing, scrolling, focus, and live streaming cannot be exercised by `Update`/`View` unit tests alone. After Tasks 1–7 and the dependent plans land, verify by hand.

- [ ] Run `go build ./...` and confirm a clean build.
- [ ] Run `go test ./...` and confirm all packages pass.
- [ ] Run `go run ./cmd/czcli` (with a valid `config.yaml` and provider creds). Confirm:
  - [ ] The alt-screen TUI shows the four regions: top bar, conversation, bottom bar, input — bordered/separated as in the mockup.
  - [ ] The top bar shows `provider:model` with `✓` (or `⚠ fallback #N` when a provider fails), and the context gauge `ctx X/Y ▓▓░ NN%`.
  - [ ] Type a message and press Enter: your line echoes as `you:`, then the reply streams in live under `bot:` character-by-character (not all at once).
  - [ ] After the turn, the bottom bar updates: `tok 1d… 1w… 1m…·mem…·🔧N 🤖M`.
  - [ ] As context fills toward the budget, the gauge turns amber at ~75% and red at ~90%, with a `⚠`.
  - [ ] If a sub-agent runs, it appears in `/agents` running list while active.
  - [ ] Slash-commands print system output in the pane: `/stats`, `/tools`, `/agents`, `/schedule`, `/model`.
  - [ ] Long conversations scroll; the viewport pins to the bottom on new output (PgUp/PgDn or arrows scroll history).
  - [ ] Resizing the terminal re-lays-out all regions without corruption.
  - [ ] Ctrl+C / Esc / `/quit` exits cleanly and the store closes (no panic, terminal restored).
- [ ] No code commit for this task; record results in the PR description.

---

## Done criteria

- `internal/channel/cli/` implements `channel.Channel` (`var _ channel.Channel = (*CLI)(nil)`).
- All `internal/channel/cli/` unit tests pass with no terminal/PTY dependency.
- `go build ./...` succeeds with `cmd/czcli/main.go` launching the TUI via `assistant.Handle` / `assistant.Status`.
- Manual checklist (Task 8) verified.
- `golangci-lint run ./internal/channel/cli/...` is clean.
