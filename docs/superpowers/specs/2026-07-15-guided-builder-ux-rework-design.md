# Guided loop builder UX rework — design

## Problem

The guided loop builder (`builder/builder.go`) walked the user through one
free-text prompt per screen, including for fields that are really enums
(step type, on_fail) validated only by string-matching against known
values with a silent re-prompt on mismatch. There was no way to select
from a visible list of options, no way to see more than one field at a
time, and no way to get help writing a script step's actual shell command
short of leaving the builder to write it by hand.

## Goals

1. Replace typed enum fields (step type, harness, on_fail) with select
   fields cycled via left/right, so the user picks from a visible list
   instead of typing a value that might be silently rejected.
2. Show the whole form — loop fields, steps added so far, and the
   in-progress step editor — on one page, navigated with tab/shift-tab,
   instead of a sequence of full-screen prompts.
3. Let the user launch a live, human-attended harness session from inside
   a script step's editor to draft its `run` command, rather than leaving
   the builder to write it by hand.
4. Give that harness session useful context automatically: a Claude Skill
   specialized in authoring looper loops/steps, plus the loop's name, the
   step being drafted, and the steps already added.

## Design

### 1. Single-page, select-driven form (`builder/builder.go`)

The builder is rewritten from a `stage`-sequenced state machine to a
single-page form. `Model` holds every field's buffer directly (loop name,
concurrency, the steps added so far, and buffers for the step currently
being edited) plus a `focus fieldID` naming which field is active.

- **Text fields** (loop name, concurrency, step name, run command, prompt,
  outputs) are edited in place: printable runes append, backspace removes
  the last rune, exactly as before.
- **Select fields** (step type, harness, on_fail) cycle through a fixed
  option list with left/right (and enter, for discoverability); there is
  no way to enter an invalid value.
- **Tab/shift-tab** (also down/up) move focus among the fields relevant to
  the step type currently selected — e.g. a `manual` step hides
  run/prompt/harness/on_fail entirely; `visibleFields()` computes this
  list fresh on every render/keypress.
- **Action fields** (`Add step`, `Finish & save loop`) commit the
  in-progress step or finalize the whole loop when confirmed with enter;
  validation failures set an inline error message and move focus to the
  offending field rather than silently re-prompting.
- `View()` renders the entire form — loop fields, a "Steps so far" summary
  list, and the step editor — in one pass, with a `▸` marker on the
  focused field.
- `New` gains a `harnessNames []string` parameter (offered as the
  harness select field's options, with a synthesized `"(default)"` first
  entry) and an `Options` parameter (see below). `Loop()`/`Done()` keep
  their existing shapes so callers outside the package are largely
  unaffected.

### 2. Drafting a script step with a live harness session

`builder.Options.DraftFn func(DraftRequest) tea.Cmd` is the builder's one
side-effecting hook, injected the same way `tui.Model` already injects
`RespondFn`/`AttachFn` to stay pure itself. `DraftRequest` carries the
loop name, the step name, and the steps already added — the preliminary
context available at the moment the user asks for help.

Pressing **ctrl+d** while a script step is being edited (regardless of
which field has focus) calls `Options.DraftFn`, if set, and marks the
form `drafting`. The returned `tea.Cmd` performs the actual session and
resolves to `builder.DraftedMsg{Content}` or `{Err}`; `Update` applies it
by writing `Content` into the step's `Run` buffer (trimmed) or setting
`errMsg`, and clears `drafting`.

The real implementation of that hook — suspending the terminal, running
the session, reading back its output — lives outside `builder`, in a new
`draft` package:

- **`draft.Run(projectDir, harness, Request) (string, error)`**: installs
  the loop-creation skill (see below) into `projectDir`, creates a scratch
  file under `<projectDir>/.looper/tmp/`, builds a prompt naming the
  skill, the loop/step being drafted, the steps already defined, and the
  scratch file the harness must write its draft to, then starts the
  harness's `Interactive` command in a looper-owned pty (`pty.Start` +
  `Session.Attach`, the same primitives `runner.InteractiveExecutor` uses)
  and blocks until it exits. It then reads back and returns the scratch
  file's contents, erroring if the session exits without writing anything.
  Unlike `runner.InteractiveExecutor`, a draft session has no hook
  socket or sentinel-derived state machine — it's just "run the harness
  here with this prompt, let the human collaborate with it, then read
  back what it wrote."
- Both `cli/build.go` (the standalone `looper new`/`looper edit` builder
  program) and `tui/program.go` (the builder embedded in the fleet TUI)
  wire `builder.Options.DraftFn` to a closure that releases/restores the
  hosting `*tea.Program`'s terminal control around a `draft.Run` call —
  the same `**tea.Program`-capture pattern `tui/program.go`'s existing
  `attachFn` uses, applied identically in both entry points since neither
  needs anything host-specific beyond the program pointer, the resolved
  default harness, and the working directory.

### 3. A loop-creation Claude Skill, bundled and auto-installed

`harness/skills/loop-creation/SKILL.md` is a Claude Skill (frontmatter +
markdown) documenting looper's step schema, the environment a script step
runs in (`$LOOPER_OUTPUT`, declared `outputs`, exit-code/`on_fail`
semantics), and how to draft a script step's contents when given the
loop/step/prior-steps context a draft session provides.

It's embedded into the `looper` binary via `go:embed`
(`harness/skill.go`) and written out on demand by
`harness.EnsureLoopCreationSkill(projectDir)` to
`<projectDir>/.claude/skills/loop-creation/SKILL.md` — always overwriting,
since it's a looper-managed file kept in sync with the binary's version,
mirroring how `harness.WriteHookSettings` regenerates its settings file
per session rather than treating it as user-owned. `draft.Run` calls this
before starting a session, so a `claude` interactive session launched in
that directory (`Dir: projectDir` on the pty) auto-discovers it as a
project skill.

## Non-goals

- No changes to `runner.InteractiveExecutor`/the loop's own `interactive`
  step type — drafting is a builder-time-only convenience, unrelated to
  how a saved loop's steps execute.
- No support for drafting `headless`/`interactive` step prompts, or any
  step type besides `script` — those fields are short enough that select
  fields plus free text already cover them well.
- No editing of an already-added step from the "Steps so far" list
  (matches the prior builder's lack of back-navigation).

## Testing

- `builder` package: rewritten `builder_test.go` drives the form via
  synthetic key messages (tab/shift-tab navigation, left/right select
  cycling, ctrl+d drafting) instead of the old stage-by-stage script;
  covers the two-step happy path, select wrapping, harness selection,
  duplicate-step-name and missing-run/prompt validation, concurrency
  parsing, edit-mode pre-population, and the `DraftFn`/`DraftedMsg` round
  trip (including the non-script and error cases).
- `harness` package: `skill_test.go` covers
  `EnsureLoopCreationSkill` writing and refreshing the skill file.
- `draft` package: `draft_test.go` exercises `draft.Run` end-to-end
  against a real pty (skipped if the environment can't allocate one,
  matching `runner`'s existing pty-test guard), using a fake harness
  script that writes to the scratch file it locates via glob, plus the
  no-interactive-command and session-exits-without-writing error paths.
- `tui` and `cli` package tests updated for the new `builder.New` and
  `builder.Options` signatures and the tab/select-driven key sequence.
