# looper — repository guide

## Workflow rule (applies to ALL changes)

Every change — features, fixes, docs, chores — goes through a pull request and
is merged into `main`. **Never commit feature work directly to `main`.**

Process for every change:

1. Branch off `main`: `git checkout -b <type>/<short-desc>`
   (`feat/…`, `fix/…`, `chore/…`, `docs/…`).
2. Implement with tests; commit using Conventional Commits.
3. Ensure `go build ./...` and `go test ./...` pass.
4. Push and open a PR into `main`: `gh pr create --base main`.
5. Merge into `main` and clean up: `gh pr merge --merge --delete-branch`,
   then `git checkout main && git pull --ff-only`.

## Project

looper is a Go tool for loop-based workflow abstraction: a user defines a
**loop** (an ordered list of **steps**) and looper runs it as concurrent
**workers**, each driving a distinct work unit through every step, then looping.

- Design spec: `docs/superpowers/specs/2026-07-15-looper-design.md`
- Milestone 1 plan: `docs/superpowers/plans/2026-07-15-looper-milestone-1-loop-runner.md`

Module path: `github.com/jbofill10/looper`. Go 1.26.

## Build, test, run

- Build: `go build ./...`
- Test: `go test ./...`
- Run a loop: `go run . run <loop-name>` (loads `.looper/loops/<name>.yaml`)

## Harness config

`headless` steps run a configured harness's headless command (default:
`claude -p <prompt>`). The default harness set is built in; override it via
`~/.config/looper/config.yaml` (or `$XDG_CONFIG_HOME/looper/config.yaml`). See
`.looper/loops/headless-example.yaml` for a sample loop; its `work-on-task`
step needs the `claude` CLI installed to actually run.
