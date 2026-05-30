# czcli TUI v2 Layout — Branded Boxes

**Date:** 2026-05-30
**Status:** Approved direction, ready for implementation planning

## Summary

Replace the current 4-region status-bar layout with a chat-app-feel design: a
branded header box, the conversation in a labeled rounded box, a clean
single-line status row, and the input in its own labeled rounded box with a
hint line below. Rename the cryptic `hist` label to `buffer` and render its
fullness with dot indicators instead of a progress bar.

## Goals

- Give czcli a visible identity (header with name + tagline + active theme).
- Make the conversation feel like a card, not loose lines.
- Make the input deliberate (its own labeled box), not jammed at the bottom edge.
- Replace "hist" — users keep reading it as the model's context window.
- Keep all current behavior (themes, streaming, markdown, spinner, slash commands, keybindings).

## Non-goals

- Functional changes (no new commands, no behavior shifts).
- Theming overhaul (existing 8 themes apply unchanged; borders use `theme.Active().Separator`).
- Sidebar / session tree (deferred).
- Animation tweaks beyond what we already have.

## Target look

```
  ╭──────────────────────────────────────────────────────────────╮
  │ ◆ czcli           personal AI assistant         dracula      │
  ╰──────────────────────────────────────────────────────────────╯

  ╭─ conversation ───────────────────────────────────────────────╮
  │                                                              │
  │  ❯ explain Go embedding                                      │
  │                                                              │
  │  Go embedding lets a struct include another struct as a      │
  │  field — exposing its fields and methods at the top level:   │
  │                                                              │
  │      type Reader struct{ src io.Reader }                     │
  │      type Logged struct{ Reader; tag string }                │
  │                                                              │
  ╰──────────────────────────────────────────────────────────────╯

   opus ✓   ●●●○ buffer 76%    1d 124k · mem 18MB · 🔧 8

  ╭─ message ────────────────────────────────────────────────────╮
  │ ❯ _                                                          │
  ╰──────────────────────────────────────────────────────────────╯
                       ctrl+/ help  ·  ctrl+t theme
```

## Components

### Header box

- Lipgloss style: `RoundedBorder()`, border color = `theme.Active().Separator`.
- Padding: `Padding(0, 1)`.
- Left: `◆ czcli` — diamond glyph in `theme.Accent`, name bold in `theme.Foreground`.
- Middle: tagline `personal AI assistant` in `theme.Dim`.
- Right: active theme name in `theme.Accent`.
- Three-column horizontal join via `lipgloss.JoinHorizontal` with explicit
  spacer widths so the three pieces sit at L / center / R regardless of
  terminal width.

### Conversation box

- Same rounded border + accent separator color.
- Border title `conversation` rendered on the top border using
  `lipgloss.Border.Top` overlay (lipgloss does this via setting the border
  manually with a label in the top run).
- Padding `Padding(1, 2)` for breathing room.
- Inner content = same `renderConversation()` we have today (history + streaming + lastErr,
  glamour-rendered assistant entries, blank lines between turns, spinner+"working…").
- Vertical sizing: takes terminal height MINUS the fixed regions (see Layout math).
- Internal viewport for scroll behaves as today.

### Status row (between conversation and input)

- One line, no chrome, 2-space indent.
- Format: `<provider:model fallback?>   <dots> buffer <pct>%   <1d k> · mem <bytes> · 🔧 <count>`
- Dots: `●●●●●●●●` total (8 cells), filled count = `int(pct * 8)`. Filled in
  `theme.Accent` when below 75%, `theme.Amber` ≥75%, `theme.Red` ≥90%; empty
  cells in `theme.Dim`.
- Extra counters (skills 📜, MCP 🔌, plugins 🧩, LSP 🧠, hooks ⚓) appended
  only when non-zero (current behavior).
- `buffer` is the new label (was `hist`). `/stats` updates to use `buffer` too,
  with the same explanatory text about summarization.

### Message box

- Rounded box + accent separator color.
- Border title `message` on the top border.
- Padding `Padding(0, 1)`.
- Inner content = textarea (Plan 11). Grows 1→6 rows as today.
- Width = terminal width − 4 (2 indent each side).

### Hint line

- One line below the message box, dim, centered or 2-space-indented.
- Text: `ctrl+/ help  ·  ctrl+t theme`.
- Only visible when there is enough vertical room (skip when terminal height < threshold).

## Layout math (per `WindowSizeMsg`)

```
header box         3 (top+content+bottom)
blank              1
conversation box   N (grows; min 6)
blank              1
status row         1
blank              1
message box        H+2 (textarea height H ∈ [1,6] + top/bottom border)
hint line          1
                   ─────
                   msg.Height
```

So `N = msg.Height - (3 + 1 + 1 + 1 + (H+2) + 1) = msg.Height - 9 - H`.

Recomputed on every `WindowSizeMsg` AND whenever textarea height changes
(textarea growth triggers a viewport re-size).

## Files affected

```
internal/channel/cli/view.go        REWRITE   renderHeader/renderConversationBox/renderStatusRow/renderMessageBox/renderHintLine + new View()
internal/channel/cli/model.go       MODIFY    layout math in WindowSizeMsg; resizeInput recompute downstream
internal/channel/cli/commands.go    MODIFY    /stats label rename (hist → buffer); ensure no other 'hist' literal
internal/channel/cli/view_test.go   MODIFY    substring assertions ("conversation", "message", "buffer", "czcli")
internal/channel/cli/commands_test.go MODIFY  /stats substring assertions ("buffer")
```

No new packages. No config changes. No public-API changes outside view internals.

## Rendering details (lipgloss specifics)

- Rounded border: `lipgloss.NewStyle().Border(lipgloss.RoundedBorder())`.
- Border title trick: build the top border string manually using
  `lipgloss.RoundedBorder()`'s `Top`, `TopLeft`, `TopRight` runes; splice the
  label string at column 3 of the top run; render the rest of the box with the
  standard border (top side omitted, then prepend the custom top line). Concrete
  helper `borderWithTitle(content, title, width, color)` lives in view.go.
- Header three-column join: compute middle-string width = `total - 2*pad -
  leftWidth - rightWidth`; render middle with explicit `lipgloss.NewStyle().Width(n).Align(lipgloss.Center)`.

## Error handling

- If terminal height < 14 (the minimum to display every region readably), drop
  the hint line first, then squeeze the conversation box to `min=4`. If
  height < 9, fall back to the old plain-line layout (no boxes).
- All existing error paths (turnDoneMsg with err, lastErr display, etc.)
  unchanged.

## Testing

- `view_test.go`: `View()` contains `"czcli"`, `"conversation"`, `"message"`,
  `"buffer"`, model name, `"❯"`. Min-height fallback returns a non-empty,
  bounded string. `borderWithTitle` helper unit-tested directly: given a width
  and a title, the rendered top border contains the title.
- `commands_test.go`: `/stats` output contains `"buffer"` instead of `"hist"`.
- Existing tests survive layout rewrite (we already dropped substring
  assertions Plan 10 broke).

## Out of scope (backlog)

- Header right-side widgets beyond the theme name (token count, session id).
- Sidebar / sessions tree.
- Animated theme transitions.
- A `compact` mode that hides borders for small terminals (the fallback covers it minimally).
