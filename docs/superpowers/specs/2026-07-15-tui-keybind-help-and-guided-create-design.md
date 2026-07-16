# TUI keybind help & in-fleet guided loop creation — design

## Problem

The fleet TUI (`tui/model.go`) renders its keybind hints as hardcoded
footer strings, and has no way to create a new loop — the guided builder
(`builder/builder.go`) is only reachable via the separate `looper new` CLI
command, which requires quitting the fleet TUI first.

## Goals

1. Keep the fleet view's inline footer accurate and complete as keybinds
   are added (no new help-overlay dependency — plain footer strings, as
   today).
2. Let a user press `n` from the fleet view to launch the guided loop
   builder in-process, without leaving the running TUI.
3. On completion, save the new loop's YAML and return to the fleet view.
   Starting the new loop's workers is out of scope — that remains a
   separate `looper run` step.

## Non-goals

- No `bubbles/key` + `bubbles/help` overlay/registry — inline footer
  strings stay the pattern.
- No in-TUI editing of existing loops (`looper edit` stays CLI-only).
- No auto-start of workers for a freshly created loop.

## Design

### 1. Footer text

`viewFleet`'s footer gains the new keybind:

```
[up/down] move  [enter] focus  [n] new loop  [q] quit
```

`viewFocus`'s footer is already accurate and unchanged.

### 2. New `viewBuilder` state in `tui.Model`

- Add `viewBuilder` to the `viewKind` enum.
- Add fields to `Model`: `builder builder.Model` and `builderMsg string`
  (holds the most recent save result, shown once in the fleet view).
- In `handleKey`, pressing `n` while `view == viewFleet` sets
  `m.builder = builder.New(nil)` and `m.view = viewBuilder`.
- `Update` routes `tea.KeyMsg` to a new `handleBuilderKey` when
  `view == viewBuilder`, instead of `handleKey`:
  - `"ctrl+c"` → `tea.Quit` (global escape hatch, consistent with the
    rest of the TUI).
  - `"esc"` → discard the in-progress builder (`m.builder =
    builder.Model{}`) and return to `viewFleet` without saving.
  - Anything else is forwarded to `m.builder.Update(msg)` unchanged.
- After forwarding a key, if `m.builder.Done()` is now true:
  - Extract the loop via `m.builder.Loop()`.
  - Call `m.opts.SaveLoopFn(loop)` (see below) to persist it.
  - Set `m.builderMsg` to `"saved <path>"` on success or `"error:
    <err>"` on failure.
  - Reset `m.builder = builder.Model{}` and `m.view = viewFleet`.
- `viewFleet`'s renderer prints `m.builderMsg` (if non-empty) on its own
  line under the header, once.
- A new `viewBuilder` render method delegates to `m.builder.View()` and
  appends a footer line: `[esc] cancel  [ctrl+c] quit`. The `builder`
  package itself is unchanged — it has no concept of cancellation; that
  is purely a fleet-TUI-level concern layered on top.

### 3. `Options.SaveLoopFn` and wiring in `tui/program.go`

Per `tui.Model`'s existing purity contract (side-effecting actions are
injected function fields — see `RespondFn`, `AttachFn`), add:

```go
// SaveLoopFn persists a completed builder.Model's loop and returns the
// path written.
SaveLoopFn func(loop *config.Loop) (string, error)
```

`tui.Run` (in `program.go`) constructs this from `os.Getwd()`, mirroring
`cli/build.go`'s `buildAndSave`: join `<cwd>/.looper/loops/<name>.yaml`
and call `config.SaveLoop`. This is a small (~5 line) duplication of that
helper rather than a new shared package — `cli` already imports `tui`,
so sharing code the other direction risks an import cycle, and the
logic is too small to justify a third package.

## Testing

- `tui` package unit tests (following the existing synthetic-`tea.Msg`
  pattern): pressing `n` from fleet enters builder view; `esc` cancels
  back to fleet without calling `SaveLoopFn`; completing the builder
  flow calls the injected `SaveLoopFn` and shows its result in
  `builderMsg`; `ctrl+c` from builder view quits.
- No changes needed to `builder` package tests (it is not modified).
