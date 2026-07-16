---
name: loop-creation
description: Use when drafting or editing a shell command/script for a looper "script" step, or otherwise helping a user author a looper loop's YAML. Looper is a Go CLI that runs an ordered list of steps as concurrent workers, looping. Covers the step schema, the environment a script step runs in, and how outputs/failure-handling work.
---

# looper loop & step authoring

looper loads a loop definition from `.looper/loops/<name>.yaml`. A loop is
an ordered list of **steps**; each worker drives one work unit through
every step, then loops back to the first step for the next unit.

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
  - If `signals_no_work: true`, a specific exit code (consult the
    codebase's `runner.NoWorkExitCode` if precision matters) instead
    signals "no work available right now" rather than a failure.

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
- `interactive` hands the terminal to a live harness session; a human
  can watch/steer it, and the session's state (needs input, done, no
  work) is derived from sentinel strings the harness is expected to
  print.

Prompts may reference `{{VAR}}` tokens for any output variable set by an
earlier step (e.g. `{{TASK_ID}}`).

## When asked to draft a script step

If you're asked to write the contents of a `run:` command for a script
step (as opposed to editing this YAML file directly), you'll usually be
told the loop's name, the step's name, and the steps already defined
before it (so you know what environment variables/outputs are already
available). Write a plain shell script that:

1. Assumes the environment and `$LOOPER_OUTPUT` conventions above.
2. Declares the outputs it produces (mention them back to the user so
   they can add them to the step's `outputs:` list).
3. Exits non-zero on real failure, `0` on success — don't swallow
   errors just to avoid `on_fail` handling.
