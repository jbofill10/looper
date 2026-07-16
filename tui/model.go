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

// LoopSnapshot is a point-in-time view of one configured loop, as returned
// by the daemon's ListLoops RPC.
type LoopSnapshot struct {
	Name    string
	Path    string
	Enabled bool
	Steps   []string
	RunID   string
}

// LoopsSnapshotMsg carries the current Loops-catalog snapshot, sent
// periodically by the program wiring (see tui.Run) so the Loops section
// stays in sync with daemon-side enable/run-once/rename/delete actions
// taken from other clients.
type LoopsSnapshotMsg []LoopSnapshot

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
	// ProjectDir is the project directory passed to builder.New when
	// entering the builder view (the 'n' key).
	ProjectDir string
	// NewLoopPathFn, if set, returns a fresh loop path (e.g.
	// .looper/loops/new-<n>.yaml) each time the 'n' key constructs a new
	// builder.Model.
	NewLoopPathFn func() string
	// AuthorFn, if set, is passed through to each builder.Model the
	// embedded builder constructs, letting it launch an interactive
	// claude session to create or edit a step (see
	// builder.Options.AuthorFn).
	AuthorFn func(builder.AuthorRequest) tea.Cmd
	// SetLoopEnabledFn toggles a loop's enabled state.
	SetLoopEnabledFn func(loopName string, enabled bool) tea.Cmd
	// RunLoopOnceFn starts a loop as a one-off run.
	RunLoopOnceFn func(loopName string) tea.Cmd
	// StopLoopGracefulFn lets a run finish its current iteration, then stops it.
	StopLoopGracefulFn func(runID string) tea.Cmd
	// AbortLoopFn hard-stops a run immediately (may interrupt an in-flight
	// step), reusing the pre-existing StopLoop RPC.
	AbortLoopFn func(runID string) tea.Cmd
	// RenameLoopFn renames a loop.
	RenameLoopFn func(loopName, newName string) tea.Cmd
	// DeleteLoopFn deletes a loop.
	DeleteLoopFn func(loopName string) tea.Cmd
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

	loops        []LoopSnapshot
	expandedLoop string // "" = no loop expanded
	treeCursor   int    // cursor position within treeRows()
	loopsFocused bool   // false (zero value) = up/down/enter drive the Workers table, exactly as before this task; true = the Loops tree

	renamingLoop string // "" = not renaming; else the loop name being renamed
	renameInput  string
	deletingLoop string // "" = no delete confirmation pending; else the loop name

	loopBuilders map[string]builder.Model // lazily populated, one per loop ever expanded
}

// treeRow is one row of the Loops section's tree: either a loop header row
// or (when that loop is expanded) one of its step rows.
type treeRow struct {
	Kind      string // "loop" | "step"
	LoopName  string
	StepIndex int // valid only when Kind == "step"
}

// NewModel returns a Model configured with opts.
func NewModel(opts Options) Model {
	return Model{
		opts:         opts,
		workers:      map[workerKey]workerRow{},
		loopBuilders: map[string]builder.Model{},
	}
}

// loopBuilder returns the builder.Model backing loopName's inline step
// list, constructing it (via builder.New, against that loop's own file —
// see LoopSnapshot.Path) the first time it's needed, and caching it so
// cursor/authoring state survives collapsing and re-expanding the loop.
func (m *Model) loopBuilder(loopName string) (builder.Model, bool) {
	if b, ok := m.loopBuilders[loopName]; ok {
		return b, true
	}
	loop, ok := m.loopByName(loopName)
	if !ok {
		return builder.Model{}, false
	}
	b, err := builder.New(m.opts.ProjectDir, loop.Path, builder.Options{AuthorFn: m.opts.AuthorFn})
	if err != nil {
		return builder.Model{}, false
	}
	m.loopBuilders[loopName] = b
	return b, true
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
	case LoopsSnapshotMsg:
		m.loops = []LoopSnapshot(msg)
		if rows := m.treeRows(); m.treeCursor >= len(rows) {
			m.treeCursor = len(rows) - 1
		}
		if m.treeCursor < 0 {
			m.treeCursor = 0
		}
		return m, nil
	case ErrMsg:
		m.builderMsg = fmt.Sprintf("error: %v", msg.Err)
		return m, nil
	case tea.KeyMsg:
		if m.renamingLoop != "" {
			return m.handleRenameKey(msg)
		}
		if m.deletingLoop != "" {
			return m.handleDeleteConfirmKey(msg)
		}
		if m.view == viewBuilder {
			return m.handleBuilderKey(msg)
		}
		return m.handleKey(msg)
	case builder.SessionDoneMsg:
		if m.view == viewBuilder {
			next, cmd := m.builder.Update(msg)
			m.builder = next.(builder.Model)
			return m, cmd
		}
		if m.expandedLoop != "" {
			if b, ok := m.loopBuilders[m.expandedLoop]; ok {
				next, cmd := b.Update(msg)
				m.loopBuilders[m.expandedLoop] = next.(builder.Model)
				return m, cmd
			}
		}
		return m, nil
	}
	return m, nil
}

// handleKey implements the TUI's keyboard navigation and decision/attach
// actions:
//
//	up/k, down/j    move the cursor: the Workers table by default, or the
//	                Loops tree when tab has given it focus
//	tab             toggle up/down/enter focus between the Workers table
//	                and the Loops tree (fleet view only)
//	space           expand/collapse the Loops tree's selected loop row
//	enter           focus the selected worker (only when the Workers table
//	                has focus, i.e. loopsFocused is false)
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
		if m.view != viewFleet {
			break
		}
		if m.loopsFocused {
			if m.treeCursor > 0 {
				m.treeCursor--
			}
		} else if m.cursor > 0 {
			m.cursor--
		}
	case "down", "j":
		if m.view != viewFleet {
			break
		}
		if m.loopsFocused {
			if last := len(m.treeRows()) - 1; m.treeCursor < last {
				m.treeCursor++
			}
		} else if last := len(m.Workers()) - 1; m.cursor < last {
			m.cursor++
		}
	case "tab":
		if m.view == viewFleet {
			m.loopsFocused = !m.loopsFocused
		}
	case " ":
		if m.view == viewFleet && m.loopsFocused {
			rows := m.treeRows()
			if m.treeCursor < len(rows) && rows[m.treeCursor].Kind == "loop" {
				name := rows[m.treeCursor].LoopName
				if m.expandedLoop == name {
					m.expandedLoop = ""
				} else {
					m.expandedLoop = name
				}
			}
		}
	case "enter":
		if m.view != viewFleet || m.loopsFocused {
			break
		}
		rows := m.Workers()
		if m.cursor < len(rows) {
			row := rows[m.cursor]
			m.focusRun, m.focusWorker = row.RunID, row.WorkerID
			m.view = viewFocus
		}
	case "esc":
		m.view = viewFleet
	case "n":
		if m.view == viewFleet && m.opts.NewLoopPathFn != nil {
			loopPath := m.opts.NewLoopPathFn()
			b, err := builder.New(m.opts.ProjectDir, loopPath, builder.Options{AuthorFn: m.opts.AuthorFn})
			if err != nil {
				m.builderMsg = fmt.Sprintf("error: %v", err)
				return m, nil
			}
			m.builder = b
			m.builderMsg = ""
			m.view = viewBuilder
		}
	case "a", "r":
		if m.view == viewFocus {
			return m.handleFocusKey(msg.String())
		}
	case "x":
		// "x" is shared: focus-view decision-abort (a/r/x) and the Loops
		// tree's hard-abort key. The two conditions are mutually exclusive
		// (view can't be both viewFocus and viewFleet), so they're merged
		// into a single case — Go forbids a duplicate case value in the
		// same switch, which the brief's literal `case "a", "r", "x":` /
		// `case "t", "o", "g", "x", "R", "D":` split would have produced.
		if m.view == viewFocus {
			return m.handleFocusKey(msg.String())
		}
		if m.view == viewFleet && m.loopsFocused {
			return m.handleLoopRowKey(msg.String())
		}
	case "t", "o", "g", "R", "D":
		if m.view == viewFleet && m.loopsFocused {
			return m.handleLoopRowKey(msg.String())
		}
	case "c", "e", "d", "shift+up", "shift+down":
		if m.view == viewFleet && m.loopsFocused && m.expandedLoop != "" {
			return m.handleExpandedLoopStepKey(msg)
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

// handleLoopRowKey implements the Loops-section loop-row action keys: t
// toggles enabled, o runs once, g gracefully stops an active run, x hard-
// aborts one, R begins a rename, D begins a delete confirmation. All are
// no-ops when the cursor isn't on a loop row, and g/x are additionally
// no-ops when that loop has no active run.
func (m Model) handleLoopRowKey(k string) (tea.Model, tea.Cmd) {
	rows := m.treeRows()
	if m.treeCursor >= len(rows) || rows[m.treeCursor].Kind != "loop" {
		return m, nil
	}
	loop, ok := m.loopByName(rows[m.treeCursor].LoopName)
	if !ok {
		return m, nil
	}

	switch k {
	case "t":
		if m.opts.SetLoopEnabledFn != nil {
			return m, m.opts.SetLoopEnabledFn(loop.Name, !loop.Enabled)
		}
	case "o":
		if m.opts.RunLoopOnceFn != nil {
			return m, m.opts.RunLoopOnceFn(loop.Name)
		}
	case "g":
		if loop.RunID != "" && m.opts.StopLoopGracefulFn != nil {
			return m, m.opts.StopLoopGracefulFn(loop.RunID)
		}
	case "x":
		if loop.RunID != "" && m.opts.AbortLoopFn != nil {
			return m, m.opts.AbortLoopFn(loop.RunID)
		}
	case "R":
		m.renamingLoop = loop.Name
		m.renameInput = loop.Name
	case "D":
		m.deletingLoop = loop.Name
	}
	return m, nil
}

// handleExpandedLoopStepKey forwards a step-list key (c/e/d/shift+up/
// shift+down) to the expanded loop's embedded builder.Model, positioning
// that builder's own cursor at the tree's currently-selected step row
// first (if the cursor is on a step row of this loop) so the forwarded
// key acts on the right step.
func (m Model) handleExpandedLoopStepKey(key tea.KeyMsg) (tea.Model, tea.Cmd) {
	b, ok := m.loopBuilder(m.expandedLoop)
	if !ok {
		return m, nil
	}

	rows := m.treeRows()
	if m.treeCursor < len(rows) {
		row := rows[m.treeCursor]
		if row.Kind == "step" && row.LoopName == m.expandedLoop {
			b = b.WithCursor(row.StepIndex)
		}
	}

	next, cmd := b.Update(key)
	m.loopBuilders[m.expandedLoop] = next.(builder.Model)
	return m, cmd
}

// handleRenameKey handles the rename-loop input stage entered via R:
// printable runes append to renameInput, backspace removes the last rune,
// esc cancels, enter confirms and invokes RenameLoopFn.
func (m Model) handleRenameKey(key tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch key.String() {
	case "esc":
		m.renamingLoop = ""
		m.renameInput = ""
		return m, nil
	case "backspace":
		if r := []rune(m.renameInput); len(r) > 0 {
			m.renameInput = string(r[:len(r)-1])
		}
		return m, nil
	case "enter":
		newName := strings.TrimSpace(m.renameInput)
		oldName := m.renamingLoop
		m.renamingLoop = ""
		m.renameInput = ""
		if newName == "" || m.opts.RenameLoopFn == nil {
			return m, nil
		}
		return m, m.opts.RenameLoopFn(oldName, newName)
	}
	if key.Type == tea.KeyRunes {
		m.renameInput += string(key.Runes)
	}
	return m, nil
}

// handleDeleteConfirmKey handles the delete-loop confirmation stage
// entered via D: y confirms and invokes DeleteLoopFn, any other key
// cancels.
func (m Model) handleDeleteConfirmKey(key tea.KeyMsg) (tea.Model, tea.Cmd) {
	name := m.deletingLoop
	m.deletingLoop = ""
	if key.String() == "y" && m.opts.DeleteLoopFn != nil {
		return m, m.opts.DeleteLoopFn(name)
	}
	return m, nil
}

// handleBuilderKey routes a key press while the embedded file-backed loop
// builder (viewBuilder) is active: ctrl+c quits the whole program, esc
// discards the in-progress builder and returns to the fleet view (the
// loop file itself is already saved continuously by the builder, so esc
// only affects the fleet view's state, not the file), and any other key
// is forwarded to the builder's own Update. If that forwarded key sets
// the builder's Quit(), its Path() is shown in builderMsg and the fleet
// view is shown. The builder's own tea.Quit (it is designed to run
// standalone in the CLI's `looper new`) is swallowed here — it must not
// quit the embedding fleet program.
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

	if m.builder.Quit() {
		m.builderMsg = fmt.Sprintf("saved %s", m.builder.Path())
		m.builder = builder.Model{}
		m.view = viewFleet
		return m, nil
	}

	return m, cmd
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
	switch {
	case m.renamingLoop != "":
		return m.viewRenameLoop()
	case m.deletingLoop != "":
		return m.viewDeleteConfirm()
	}
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
	if len(m.loops) > 0 {
		b.WriteString(style.SubHeader.Render("Loops") + "\n")
		treeRows := m.treeRows()
		for i, r := range treeRows {
			cursor := "  "
			if m.loopsFocused && i == m.treeCursor {
				cursor = style.Marker.Render("▸ ")
			}
			if r.Kind == "loop" {
				loop, _ := m.loopByName(r.LoopName)
				status := "[off]"
				if loop.Enabled {
					status = "[on]"
				}
				running := ""
				if loop.RunID != "" {
					running = fmt.Sprintf("  running (%s)", loop.RunID)
				}
				fmt.Fprintf(&b, "%s%-20s %s%s\n", cursor, loop.Name, status, running)
			} else {
				stepName := ""
				if l, ok := m.loopByName(r.LoopName); ok && r.StepIndex < len(l.Steps) {
					stepName = l.Steps[r.StepIndex]
				}
				if bm, ok := m.loopBuilders[r.LoopName]; ok {
					if steps := bm.Steps(); r.StepIndex < len(steps) {
						stepName = fmt.Sprintf("%s (%s)", steps[r.StepIndex].Name, steps[r.StepIndex].Type)
					}
				}
				fmt.Fprintf(&b, "%s    %d. %s\n", cursor, r.StepIndex+1, stepName)
			}
		}
		b.WriteString("\n")
	}
	for i, r := range rows {
		cursor := "  "
		if !m.loopsFocused && i == m.cursor {
			cursor = style.Marker.Render("▸ ")
		}
		fmt.Fprintf(&b, "%s%-8s %-14s %-12s %s\n", cursor, r.WorkerID, r.Task, r.Step, glyph(r))
	}
	b.WriteString("\n" + style.KeyHint.Render("[up/down] move  [tab] switch focus  [space] expand/collapse  [enter] focus  [t] toggle  [o] run once  [g] graceful stop  [x] abort  [R] rename  [D] delete  [n] new loop  [q] quit") + "\n")
	return b.String()
}

// viewRenameLoop renders the rename-loop input prompt entered via R.
func (m Model) viewRenameLoop() string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s\n\n", style.Title.Render(fmt.Sprintf("Rename loop %q", m.renamingLoop)))
	fmt.Fprintf(&b, "%s %s\n", style.Label.Render("New name:"), m.renameInput)
	b.WriteString("\n" + style.KeyHint.Render("[enter] confirm  [esc] cancel") + "\n")
	return b.String()
}

// viewDeleteConfirm renders the delete-loop confirmation prompt entered via D.
func (m Model) viewDeleteConfirm() string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s\n\n", style.TitleAlert.Render(fmt.Sprintf("Delete loop %q?", m.deletingLoop)))
	b.WriteString("\n" + style.KeyHint.Render("[y] confirm  [any other key] cancel") + "\n")
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

// treeRows returns the Loops section's current flattened row list: one
// "loop" row per configured loop (sorted as delivered by ListLoops, i.e.
// by name), plus — for whichever loop is expanded — a "step" row per step
// in order, interleaved right after that loop's row.
func (m Model) treeRows() []treeRow {
	var rows []treeRow
	for _, l := range m.loops {
		rows = append(rows, treeRow{Kind: "loop", LoopName: l.Name})
		if l.Name == m.expandedLoop {
			n := len(l.Steps)
			if b, ok := m.loopBuilders[l.Name]; ok {
				n = len(b.Steps())
			}
			for i := 0; i < n; i++ {
				rows = append(rows, treeRow{Kind: "step", LoopName: l.Name, StepIndex: i})
			}
		}
	}
	return rows
}

// loopByName returns the LoopSnapshot named name, and whether one exists.
func (m Model) loopByName(name string) (LoopSnapshot, bool) {
	for _, l := range m.loops {
		if l.Name == name {
			return l, true
		}
	}
	return LoopSnapshot{}, false
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
