// Package tui implements looper's Bubble Tea fleet & focus client. The
// Model is pure: it never performs network I/O itself. Side-effecting
// actions (responding to a decision, attaching to a live session) are
// invoked via injected function fields (RespondFn, AttachFn) that return
// tea.Cmd values, so Update/View can be unit-tested with synthetic
// messages and fakes.
package tui

import (
	"fmt"
	"sort"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/jbofill10/looper/builder"
	"github.com/jbofill10/looper/config"
	"github.com/jbofill10/looper/style"
)

// StateUpdateMsg mirrors daemon.Update: it is the tea.Msg carrying one
// state-change event for a run/worker, as translated from the daemon's
// gRPC StateUpdate stream (see updateFromProto).
type StateUpdateMsg struct {
	RunID     string
	Kind      string
	LoopName  string
	Iteration int
	Step      string
	State     string
	Message   string
	RequestID string
	Options   []string
	WorkerID  string
	Task      string
}

// WorkerSnapshot is a point-in-time snapshot of one worker's status within
// a run, as returned by the daemon's ListRuns RPC.
type WorkerSnapshot struct {
	WorkerID    string
	Task        string
	Iteration   int
	CurrentStep string
	State       string
	Status      string
}

// RunSnapshot is a point-in-time snapshot of one run's status, as returned
// by the daemon's ListRuns RPC.
type RunSnapshot struct {
	RunID       string
	LoopName    string
	Status      string
	Iteration   int
	CurrentStep string
	State       string
	Err         string
	Workers     []WorkerSnapshot
}

// RunsSnapshotMsg primes the Model with the daemon's current runs, typically
// sent once at startup (from a ListRuns call) before the live update stream
// catches up.
type RunsSnapshotMsg []RunSnapshot

// ErrMsg reports an error encountered by the program wiring (e.g. a stream
// or RPC failure) so the Model can surface it.
type ErrMsg struct{ Err error }

// DecisionSentMsg confirms a decision outcome was successfully delivered to
// the daemon for (RunID, RequestID).
type DecisionSentMsg struct {
	RunID     string
	RequestID string
}

// workerRow is one row of the fleet view: a worker's latest known status,
// keyed by (RunID, WorkerID).
type workerRow struct {
	RunID          string
	LoopName       string
	WorkerID       string
	Task           string
	Step           string
	State          string
	Status         string
	PendingReqID   string
	PendingOptions []string
	Iteration      int
}

// needsHuman reports whether this row has a pending decision awaiting a
// human response.
func (w workerRow) needsHuman() bool {
	return w.PendingReqID != ""
}

// workerKey identifies a worker row within Model.workers.
type workerKey struct {
	RunID    string
	WorkerID string
}

// Options configures a new Model.
type Options struct {
	// RespondFn delivers a decision outcome ("advance", "retry", or
	// "abort") for (runID, requestID) to the daemon. It returns a tea.Cmd
	// so Update stays pure; the returned command performs the RPC and
	// yields a DecisionSentMsg or ErrMsg.
	RespondFn func(runID, requestID, outcome string) tea.Cmd
	// AttachFn attaches the local terminal to runID's live interactive
	// session. It returns a tea.Cmd that suspends the Bubble Tea program,
	// bridges the session, and resumes it on return.
	AttachFn func(runID string) tea.Cmd
	// SaveLoopFn persists a completed guided-builder loop and returns the
	// path written. Invoked when the embedded builder (viewBuilder) reaches
	// its done stage.
	SaveLoopFn func(loop *config.Loop) (string, error)
	// HarnessNames lists the configured harnesses, offered as the guided
	// builder's per-step harness select field.
	HarnessNames []string
	// DraftFn, if set, is passed through to each builder.Model the guided
	// builder constructs, letting it launch an interactive harness session
	// to draft a script step's contents (see builder.Options.DraftFn).
	DraftFn func(builder.DraftRequest) tea.Cmd
	// Quit, if set, makes Init immediately emit tea.Quit — used by tests
	// and tools that want a Model that never blocks on a real program run.
	Quit bool
}

// viewKind selects which of the TUI's three views is rendered.
type viewKind int

const (
	viewFleet viewKind = iota
	viewFocus
	viewBuilder
)

// Model is the Bubble Tea model for looper's fleet & focus TUI. It holds
// aggregated run/worker state and renders all views; it never touches the
// network directly (see Options.RespondFn / Options.AttachFn).
type Model struct {
	opts Options

	workers map[workerKey]workerRow
	// runOrder and workerOrder preserve first-seen insertion order, used as
	// a stable tiebreaker alongside RunID/WorkerID sorting.
	order []workerKey

	view                  viewKind
	cursor                int
	focusRun, focusWorker string
	builder               builder.Model
	builderMsg            string
}

// NewModel returns a Model configured with opts.
func NewModel(opts Options) Model {
	return Model{
		opts:    opts,
		workers: map[workerKey]workerRow{},
	}
}

// Init implements tea.Model. It emits tea.Quit immediately if
// Options.Quit was set; otherwise it has no initial command.
func (m Model) Init() tea.Cmd {
	if m.opts.Quit {
		return tea.Quit
	}
	return nil
}

// Update implements tea.Model.
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case StateUpdateMsg:
		m.applyStateUpdate(msg)
		return m, nil
	case RunsSnapshotMsg:
		m.applyRunsSnapshot(msg)
		return m, nil
	case tea.KeyMsg:
		if m.view == viewBuilder {
			return m.handleBuilderKey(msg)
		}
		return m.handleKey(msg)
	case builder.DraftedMsg:
		if m.view == viewBuilder {
			next, cmd := m.builder.Update(msg)
			m.builder = next.(builder.Model)
			return m, cmd
		}
		return m, nil
	}
	return m, nil
}

// handleKey implements the TUI's keyboard navigation and decision/attach
// actions:
//
//	up/k, down/j    move the cursor in the fleet view
//	enter           focus the selected worker
//	esc             return to the fleet view
//	q, ctrl+c       quit
//	a/r/x           in focus with a pending decision, respond
//	                advance/retry/abort; with no pending decision, 'a'
//	                attaches to the focused run's live session
func (m Model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "q", "ctrl+c":
		return m, tea.Quit
	case "up", "k":
		if m.view == viewFleet && m.cursor > 0 {
			m.cursor--
		}
	case "down", "j":
		if m.view == viewFleet {
			if last := len(m.Workers()) - 1; m.cursor < last {
				m.cursor++
			}
		}
	case "enter":
		if m.view == viewFleet {
			rows := m.Workers()
			if m.cursor < len(rows) {
				row := rows[m.cursor]
				m.focusRun, m.focusWorker = row.RunID, row.WorkerID
				m.view = viewFocus
			}
		}
	case "esc":
		m.view = viewFleet
	case "n":
		if m.view == viewFleet {
			m.builder = builder.New(nil, m.opts.HarnessNames, builder.Options{DraftFn: m.opts.DraftFn})
			m.builderMsg = ""
			m.view = viewBuilder
		}
	case "a", "r", "x":
		if m.view == viewFocus {
			return m.handleFocusKey(msg.String())
		}
	}
	return m, nil
}

// handleFocusKey implements the a/r/x keys while in the focus view: with a
// pending decision they respond advance/retry/abort (optimistically
// clearing the pending state and invoking RespondFn); with no pending
// decision, 'a' attaches to the focused run (invoking AttachFn).
func (m Model) handleFocusKey(k string) (tea.Model, tea.Cmd) {
	row, ok := m.focusedRow()
	if !ok {
		return m, nil
	}

	if row.PendingReqID != "" {
		outcome, ok := map[string]string{"a": "advance", "r": "retry", "x": "abort"}[k]
		if !ok {
			return m, nil
		}
		runID, reqID := row.RunID, row.PendingReqID

		key := workerKey{RunID: row.RunID, WorkerID: row.WorkerID}
		row.PendingReqID = ""
		row.PendingOptions = nil
		m.workers[key] = row

		if m.opts.RespondFn != nil {
			return m, m.opts.RespondFn(runID, reqID, outcome)
		}
		return m, nil
	}

	if k == "a" && m.opts.AttachFn != nil {
		return m, m.opts.AttachFn(row.RunID)
	}
	return m, nil
}

// handleBuilderKey routes a key press while the guided loop builder
// (viewBuilder) is active: ctrl+c quits the whole program, esc discards
// the in-progress builder and returns to the fleet view without saving,
// and any other key is forwarded to the builder's own Update. If that
// forwarded key advances the builder to its done stage, the resulting
// loop is saved via Options.SaveLoopFn and the fleet view is shown with
// the outcome in builderMsg. The builder's own tea.Quit (it is designed
// to run standalone in the CLI's `looper new`) is swallowed here — it
// must not quit the embedding fleet program.
func (m Model) handleBuilderKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "ctrl+c":
		return m, tea.Quit
	case "esc":
		m.builder = builder.Model{}
		m.view = viewFleet
		return m, nil
	}

	next, cmd := m.builder.Update(msg)
	m.builder = next.(builder.Model)

	if m.builder.Done() {
		loop, _ := m.builder.Loop()
		m.builderMsg = m.saveLoop(loop)
		m.builder = builder.Model{}
		m.view = viewFleet
		return m, nil
	}

	return m, cmd
}

// saveLoop persists loop via Options.SaveLoopFn and formats the outcome
// for display in the fleet view's builderMsg line.
func (m Model) saveLoop(loop *config.Loop) string {
	if m.opts.SaveLoopFn == nil {
		return ""
	}
	path, err := m.opts.SaveLoopFn(loop)
	if err != nil {
		return fmt.Sprintf("error: %v", err)
	}
	return fmt.Sprintf("saved %s", path)
}

// focusedRow returns the worker row currently focused, and whether one is
// focused at all.
func (m Model) focusedRow() (workerRow, bool) {
	row, ok := m.workers[workerKey{RunID: m.focusRun, WorkerID: m.focusWorker}]
	return row, ok
}

// glyph returns the single-character status glyph for a worker row:
// working ⚙, needs-human ⏸, done ✔, or no-work ∅.
func glyph(row workerRow) string {
	switch {
	case row.PendingReqID != "":
		return style.GlyphNeedsYou.Render("⏸")
	case row.Status == "done" || row.Status == "stopped" || row.Status == "error":
		return style.GlyphDone.Render("✔")
	case row.Step == "" && row.State == "":
		return style.GlyphEmpty.Render("∅")
	default:
		return style.GlyphRunning.Render("⚙")
	}
}

// View implements tea.Model, rendering the fleet, focus, or builder view.
func (m Model) View() string {
	switch m.view {
	case viewFocus:
		return m.viewFocus()
	case viewBuilder:
		return m.viewBuilder()
	default:
		return m.viewFleet()
	}
}

// viewFleet renders the header badge and a table of worker rows, sorted
// needs-human-first (see Workers), with a ▸ cursor on the selected row.
func (m Model) viewFleet() string {
	rows := m.Workers()
	runs := map[string]struct{}{}
	for _, r := range rows {
		runs[r.RunID] = struct{}{}
	}

	var b strings.Builder
	header := fmt.Sprintf("looper · %d runs · %d NEED YOU", len(runs), m.NeedYouCount())
	if m.NeedYouCount() > 0 {
		header = style.TitleAlert.Render(header)
	} else {
		header = style.Title.Render(header)
	}
	fmt.Fprintf(&b, "%s\n\n", header)
	if m.builderMsg != "" {
		msgStyle := style.Success
		if strings.HasPrefix(m.builderMsg, "error:") {
			msgStyle = style.Error
		}
		fmt.Fprintf(&b, "%s\n\n", msgStyle.Render(m.builderMsg))
	}
	for i, r := range rows {
		cursor := "  "
		if i == m.cursor {
			cursor = style.Marker.Render("▸ ")
		}
		fmt.Fprintf(&b, "%s%-8s %-14s %-12s %s\n", cursor, r.WorkerID, r.Task, r.Step, glyph(r))
	}
	b.WriteString("\n" + style.KeyHint.Render("[up/down] move  [enter] focus  [n] new loop  [q] quit") + "\n")
	return b.String()
}

// viewFocus renders the focused worker's title, current step, and (if
// pending) a decision prompt.
func (m Model) viewFocus() string {
	row, ok := m.focusedRow()
	if !ok {
		return "no worker focused\n"
	}

	var b strings.Builder
	fmt.Fprintf(&b, "%s\n\n", style.Title.Render(fmt.Sprintf("%s · %s", row.WorkerID, row.Task)))
	fmt.Fprintf(&b, "step: %s (%s) %s\n", row.Step, row.State, glyph(row))

	if row.PendingReqID != "" {
		fmt.Fprintf(&b, "\n%s\n", style.TitleAlert.Render("decision needed: [a]dvance [r]etry [x]abort"))
	} else {
		b.WriteString("\n" + style.Action.Render("[a] attach") + "\n")
	}
	b.WriteString("\n" + style.KeyHint.Render("[esc] back  [q] quit") + "\n")
	return b.String()
}

// viewBuilder renders the embedded guided loop builder: its own
// prompt/input, plus a footer for the cancel/quit keys the builder
// package itself has no concept of (cancellation is a fleet-TUI-level
// concern layered on top of the unmodified builder).
func (m Model) viewBuilder() string {
	var b strings.Builder
	b.WriteString(m.builder.View())
	b.WriteString("\n" + style.KeyHint.Render("[esc] cancel  [ctrl+c] quit") + "\n")
	return b.String()
}

// applyStateUpdate upserts the worker row keyed by (RunID, WorkerID)
// according to msg.Kind:
//   - decision_request sets the row's pending decision (PendingReqID /
//     PendingOptions), making it needs-human.
//   - any other per-worker update (step_start, outcome, state, iteration)
//     clears a previously pending decision (the worker has moved on) and
//     updates the row's displayed fields.
//   - run_done marks every worker of the run as done and clears any
//     pending decision (the run no longer needs a response).
func (m *Model) applyStateUpdate(msg StateUpdateMsg) {
	if msg.Kind == "run_done" {
		for _, key := range m.order {
			if key.RunID != msg.RunID {
				continue
			}
			row := m.workers[key]
			row.Status = "done"
			if msg.State != "" {
				row.Status = msg.State
			}
			row.PendingReqID = ""
			row.PendingOptions = nil
			m.workers[key] = row
		}
		return
	}

	key := workerKey{RunID: msg.RunID, WorkerID: msg.WorkerID}
	row, ok := m.workers[key]
	if !ok {
		row = workerRow{RunID: msg.RunID, WorkerID: msg.WorkerID}
		m.order = append(m.order, key)
	}

	if msg.LoopName != "" {
		row.LoopName = msg.LoopName
	}
	if msg.Task != "" {
		row.Task = msg.Task
	}
	if msg.Step != "" {
		row.Step = msg.Step
	}
	if msg.State != "" {
		row.State = msg.State
	}
	if msg.Iteration != 0 {
		row.Iteration = msg.Iteration
	}

	switch msg.Kind {
	case "decision_request":
		row.PendingReqID = msg.RequestID
		row.PendingOptions = msg.Options
	default:
		row.PendingReqID = ""
		row.PendingOptions = nil
	}

	m.workers[key] = row
}

// applyRunsSnapshot upserts a worker row for every worker in snap, without
// disturbing any pending decision already recorded for that worker (a
// snapshot never carries decision-request information).
func (m *Model) applyRunsSnapshot(snap RunsSnapshotMsg) {
	for _, run := range snap {
		for _, w := range run.Workers {
			key := workerKey{RunID: run.RunID, WorkerID: w.WorkerID}
			row, ok := m.workers[key]
			if !ok {
				row = workerRow{RunID: run.RunID, WorkerID: w.WorkerID}
				m.order = append(m.order, key)
			}
			row.LoopName = run.LoopName
			row.Task = w.Task
			row.Step = w.CurrentStep
			row.State = w.State
			row.Status = w.Status
			row.Iteration = w.Iteration
			m.workers[key] = row
		}
	}
}

// Workers returns the current worker rows, sorted with needs-human workers
// first, then by RunID, then by WorkerID.
func (m Model) Workers() []workerRow {
	rows := make([]workerRow, 0, len(m.workers))
	for _, row := range m.workers {
		rows = append(rows, row)
	}
	sort.Slice(rows, func(i, j int) bool {
		a, b := rows[i], rows[j]
		if a.needsHuman() != b.needsHuman() {
			return a.needsHuman()
		}
		if a.RunID != b.RunID {
			return a.RunID < b.RunID
		}
		return a.WorkerID < b.WorkerID
	})
	return rows
}

// NeedYouCount returns the number of workers with a pending decision
// awaiting a human response.
func (m Model) NeedYouCount() int {
	n := 0
	for _, row := range m.workers {
		if row.needsHuman() {
			n++
		}
	}
	return n
}
