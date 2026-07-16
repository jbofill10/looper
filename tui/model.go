// Package tui implements looper's Bubble Tea fleet & focus client. The
// Model is pure: it never performs network I/O itself. Side-effecting
// actions (responding to a decision, attaching to a live session) are
// invoked via injected function fields (RespondFn, AttachFn) that return
// tea.Cmd values, so Update/View can be unit-tested with synthetic
// messages and fakes.
package tui

import (
	"sort"

	tea "github.com/charmbracelet/bubbletea"
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
	// Quit, if set, makes Init immediately emit tea.Quit — used by tests
	// and tools that want a Model that never blocks on a real program run.
	Quit bool
}

// Model is the Bubble Tea model for looper's fleet & focus TUI. It holds
// aggregated run/worker state and renders both views; it never touches the
// network directly (see Options.RespondFn / Options.AttachFn).
type Model struct {
	opts Options

	workers map[workerKey]workerRow
	// runOrder and workerOrder preserve first-seen insertion order, used as
	// a stable tiebreaker alongside RunID/WorkerID sorting.
	order []workerKey
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
	}
	return m, nil
}

// View implements tea.Model. Task 1 provides only state aggregation; fleet
// and focus rendering are added in Task 2.
func (m Model) View() string {
	return ""
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
