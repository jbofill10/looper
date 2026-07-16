# Interactive step authoring — design

## Problem

The guided builder (`builder/builder.go`) is a single-page form: every step
field (name, type, run/prompt, harness, outputs, on_fail) is typed or
cycled by hand, one field at a time. The only harness assistance available
is `draft.Run`, wired to ctrl+d, which drafts a single script step's `run`
text via a live session and pastes it back into that one field — everything
else is still manual.

We want the harness to be able to author or edit a *whole* step directly,
conversationally, for any step type, superseding the form entirely.

## Goals

1. Replace the field-by-field form with two keybinds, `create-step` and
   `edit-step`, that each open a live, human-attended `claude` session
   which edits the loop's YAML file on disk directly, for any step type.
2. Give that session a skill that knows the full `Step` schema (all four
   types, their required fields, `outputs`, `on_fail`) so it can author a
   valid step without hand-holding.
3. Scope that skill so it's available *only* in looper-launched sessions —
   not visible in a user's own ordinary `claude` sessions run in the same
   project.
4. Make the builder a thin, file-backed viewer: the loop YAML on disk is
   the source of truth at all times, not something assembled in memory and
   saved once at the end.
5. Surface validation failures per-step (a step that fails
   `config.Step.Validate()` shows red in the list) without blocking, and
   let re-invoking `edit-step` on a red step feed the error back to Claude
   so it can self-correct.

## Design

### 1. Scoped skill via a plugin, not a project skill file

The existing `harness.EnsureLoopCreationSkill` writes
`<projectDir>/.claude/skills/loop-creation/SKILL.md` — a real project
skill, discoverable by *any* `claude` session run in that directory, not
just looper's. That's no longer good enough: we want the skill available
only when looper itself launches the session.

Claude Code has no settings field for pointing at an arbitrary skill
directory, but it does support enabling a *plugin* per-invocation:
`--settings <path>` (already used for scratch settings files elsewhere in
this codebase) can set `enabledPlugins`, and `--plugin-dir <path>` loads a
plugin from an arbitrary directory for that one session.

So the bundled skill becomes a small plugin instead of a bare skill file:

- `harness/plugin/.claude-plugin/plugin.json` +
  `harness/plugin/skills/step-authoring/SKILL.md`, embedded via
  `go:embed` (replacing today's `skill.go`/`skills/loop-creation/`).
- `harness.EnsureStepAuthoringPlugin() (dir string, err error)` extracts
  the embedded plugin tree to a looper-owned directory outside the user's
  project (e.g. `$XDG_CACHE_HOME/looper/plugin/` or `os.UserCacheDir()`
  equivalent), refreshing it every call the same way the current function
  always overwrites — it's looper-managed, not user-owned.
- Before starting a session, looper writes a scratch `settings.json`
  containing `{"enabledPlugins": {"step-authoring": true}}` (same scratch
  file lifecycle as today's scratch script file: created under
  `<projectDir>/.looper/tmp/`, removed after the session) and passes
  `--plugin-dir <dir> --settings <scratchSettingsPath>` on the harness
  argv, in addition to the existing prompt argument.
- The skill content itself documents: the four step types and which
  fields each requires, `outputs`/`on_fail`/`harness` semantics, the
  script step's `$LOOPER_OUTPUT` contract, and 2-3 worked examples drawn
  from `.looper/loops/headless-example.yaml`-style loops.

### 2. Builder becomes file-backed

`builder.Model` stops assembling `m.steps` in memory for a one-time save.
Instead:

- Opening the builder for a new loop writes a minimal skeleton (`name`,
  `steps: []`) to `<dir>/.looper/loops/<name>.yaml` immediately via
  `config.SaveLoop`, before the TUI even renders.
- Opening it for an existing loop just points at that file.
- The model's step list is reloaded from `config.Load(path)` after every
  interactive session (see §3) — never held as the thing being "finished."
- There is no more `finish()`/`Loop()`/`Done()` end-of-form assembly.
  Reorder and delete remain direct list operations the builder performs
  itself (via `config.SaveLoop`), since they don't need a harness.
- Quitting the builder just closes the TUI; whatever's on disk is the
  loop.

### 3. `create-step` / `edit-step` flow

Both keybinds replace `builder`'s entire step-editor portion of the form
(fStepName..fStepOnFail, fAddStep) and its ctrl+d draft hook. Each launches
the same kind of session `draft.Run` does today (`pty.Start` +
`Session.RunAttached`, blocking until the user detaches), but pointed at
editing the real loop file instead of writing a scratch script:

- **create-step**: prompt tells Claude a new step needs to be added to the
  loop at `<path>`, to ask the user what it should do, and to use the
  step-authoring skill to append it correctly.
- **edit-step**, valid step: prompt names the step and the file, asks
  Claude to edit it per the user's request using the skill.
- **edit-step**, step currently failing validation: same prompt, prefixed
  with the stored validation error text, asking Claude to fix that first.

On detach, the builder reloads the file (§2) and re-validates.

This supersedes `draft.Run`/`draft.Request`/`DraftedMsg` and the
`Options.DraftFn` hook entirely — there's no drafted-text-to-paste-back
round trip anymore, since Claude edits the file directly.

### 4. Per-step validation and red/invalid display

After every reload, the builder calls `Validate()` per step (today it's
loop-wide only in `config.Loop.Validate()` — this needs a step-level
entry point alongside it) and keeps `map[stepName]error`. The step list
renders a step name in `style.Error` when it has an entry; the error text
shows in a status line when that step is selected/focused. Nothing about
this blocks navigation, saving, or running the builder — it's purely
advisory until the user re-invokes `edit-step` on it.

## Non-goals

- No change to `runner.InteractiveExecutor` or how a saved loop's
  `interactive` step type executes at run time — this is entirely a
  builder-time authoring concern.
- No structured-output/hook-socket channel for handing the step back;
  the filesystem (the loop YAML itself) is the interface.
- No support for authoring loop-level fields (name, concurrency) via the
  harness — those stay simple text fields in the builder, since they're
  trivial and don't benefit from conversational authoring.

## Testing

- `harness` package: replace `skill_test.go` with a test covering
  `EnsureStepAuthoringPlugin`'s extraction/refresh of the embedded plugin
  tree to the cache directory.
- New step-authoring session package (replacing `draft`): a test exercising
  the session end-to-end against a real pty (skipped where one can't be
  allocated, matching existing guards), using a fake harness script that
  edits the target YAML file directly, plus the no-interactive-command and
  invalid-final-YAML error paths.
- `config` package: add a step-level `Validate` entry point if one doesn't
  already exist in a usable form, with tests per step type's required
  fields.
- `builder` package: rewritten tests drive the simplified model — file
  reload after a session, red/valid step rendering, reorder/delete without
  a session, and the create/edit-step keybind wiring (session invoked with
  expected prompt content, including the validation-error-prefixed case).
- `cli`/`tui` packages: updated for the new `builder.New`/`Options`
  signatures (no more `DraftFn`) and the file-path-based construction.
