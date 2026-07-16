---
name: step-authoring
description: Use when creating or editing a step in a looper loop's YAML file (.looper/loops/<name>.yaml). Looper is a Go CLI that runs an ordered list of steps as concurrent workers, looping. Covers all four step types, the step schema, the environment a script step runs in, and how outputs/failure-handling work.
---

# looper step authoring

You are editing a loop file directly: `.looper/loops/<name>.yaml`. A loop
is an ordered list of **steps**; each worker drives one work unit through
every step, then loops back to the first step for the next unit. Ask the
user what the step should do (or what they want changed, if editing an
existing one), then edit the YAML file yourself to match.

## Step schema (YAML fields, and the equivalent Go struct in
`config.Step`)

```yaml
- name: build          # required, unique within the loop
  type: script          # script | headless | interactive | manual
  run: ./scripts/build.sh   # script steps only: a shell command/script,
                             # run via `sh -c`
  prompt: "..."          # headless/interactive steps only: the prompt
                          # handed to the harness (may reference {{VAR}}
                          # tokens from earlier outputs)
  harness: claude         # headless/interactive steps only; blank = the
                          # global default harness
  outputs: [TASK_ID]      # names of variables this step may set (see
                          # "Setting outputs" below)
  on_fail: ask            # script/headless only: ask | retry | abort
  signals_no_work: false  # script steps only: see "Signaling no work"
```

## The `manual` step type

A `manual` step needs only `name` and `type: manual` — it's a pause point
where a human confirms something by hand before the worker continues. It
has no `run`, `prompt`, `harness`, `on_fail`, or meaningful `outputs`.

## Writing a script step's `run`

A script step's `run` is executed as `sh -c "<run>"` with:

- **Working directory**: the loop's workspace (the project checkout the
  worker is operating in).
- **Environment**: every variable currently in the run context (set by
  earlier steps' `outputs`, see below), plus `LOOPER_OUTPUT` — the path
  to a file the script can append `KEY=VALUE` lines to.
- **Exit code semantics**:
  - `0` → the step succeeded, the worker advances to the next step.
  - Non-zero → failure is handled per `on_fail`:
    - `ask` (default): a human is prompted to advance/retry/abort.
    - `retry`: the step re-runs.
    - `abort`: the worker's whole iteration aborts.
  - If `signals_no_work: true`, a specific exit code signals "no work
    available right now" rather than a failure.

Write scripts that are safe to retry (idempotent) since `on_fail: retry`
or a human choosing "retry" re-runs the same command with the same
environment.

### Setting outputs

To make a later step (or a later iteration) see a value this step
produces, append a `KEY=VALUE` line to the file at `$LOOPER_OUTPUT` for
every key declared in this step's `outputs:` list. Example:

```sh
task_id=$(pick-next-task)
echo "TASK_ID=$task_id" >> "$LOOPER_OUTPUT"
```

Only keys declared in `outputs:` are captured; anything else written to
that file is ignored. Once captured, `TASK_ID` (or whatever the key is)
becomes an environment variable for every subsequent step in the same
iteration, and a `{{TASK_ID}}`-style token for headless/interactive
prompts.

The variable that identifies "the work unit" for a loop defaults to
`TASK_ID` (configurable via the loop's `task_var`); the step that
acquires or picks the next unit of work should set it via `outputs`.

## headless / interactive steps

These hand a prompt to an agentic coding harness (e.g. `claude`) instead
of running a plain shell command:

- `headless` runs the harness non-interactively (`claude -p "<prompt>"`)
  and expects it to run to completion unattended.
- `interactive` hands the terminal to a live harness session; a human can
  watch/steer it, and the session's state (needs input, done, no work) is
  derived from sentinel strings the harness is expected to print.

Prompts may reference `{{VAR}}` tokens for any output variable set by an
earlier step (e.g. `{{TASK_ID}}`).

## Fixing a step that fails validation

If you're told a step currently fails validation (e.g. "interactive step
requires 'prompt'"), fix that specific problem first, then ask the user
if there's anything else they want changed about it.
