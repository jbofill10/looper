package daemon

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"
	"sync"

	"github.com/jbofill10/looper/config"
	"github.com/jbofill10/looper/runner"
)

// Update is one state-change event for a run, published to Manager
// subscribers. Kind is one of: iteration | step_start | outcome |
// decision_request | state | run_done.
type Update struct {
	RunID     string
	Kind      string
	LoopName  string
	Iteration int
	Step      string
	State     string
	Message   string
	RequestID string
	Options   []string
}

// RunInfo is a point-in-time snapshot of one run's status.
type RunInfo struct {
	RunID       string
	LoopName    string
	Status      string // running | done | stopped | error
	Iteration   int
	CurrentStep string
	State       string
	Err         string
}

// subscriber is one Subscribe call's channel. Sends and close are
// serialized on subMu so a send can never race a close (which would
// otherwise panic with "send on closed channel").
type subscriber struct {
	subMu  sync.Mutex
	closed bool
	ch     chan Update
	runID  string // empty = all runs
}

func (s *subscriber) send(u Update) {
	s.subMu.Lock()
	defer s.subMu.Unlock()
	if s.closed {
		return
	}
	s.ch <- u
}

func (s *subscriber) close() {
	s.subMu.Lock()
	defer s.subMu.Unlock()
	if s.closed {
		return
	}
	s.closed = true
	close(s.ch)
}

// pendingRequest is an in-flight decision request awaiting Respond. update
// is retained so a subscriber that joins after the request was published
// (and thus missed it) can still be shown it — decision_request delivery
// must be reliable, since the worker blocks on it indefinitely.
type pendingRequest struct {
	outcome chan runner.Outcome
	update  Update
}

// runEntry tracks one active or finished run.
type runEntry struct {
	info    RunInfo
	cancel  context.CancelFunc
	fanIn   chan Update // internal buffer; drained by a per-run fanout goroutine
	pending map[string]*pendingRequest
}

// Manager owns the daemon's active and finished runs. It is pure Go with no
// gRPC dependency: it starts a runner.Worker per run, fans out its progress
// to subscribers, and services decision requests (manual steps, on_fail=ask)
// via a client round-trip.
type Manager struct {
	global    *config.Global
	looperBin string

	// newID generates run ids; injectable for deterministic tests. Guarded
	// by its own synchronization if replaced (the default is).
	newID func() string
	// newReqID generates decision request ids; injectable for tests.
	newReqID func() string

	mu        sync.Mutex
	runs      map[string]*runEntry
	subs      map[int]*subscriber
	nextSubID int
}

// NewManager returns a Manager for orchestrating loops. A nil global uses
// config.DefaultGlobal().
func NewManager(global *config.Global, looperBin string) *Manager {
	if global == nil {
		global = config.DefaultGlobal()
	}
	return &Manager{
		global:    global,
		looperBin: looperBin,
		newID:     newCounter("run"),
		newReqID:  newCounter("req"),
		runs:      map[string]*runEntry{},
		subs:      map[int]*subscriber{},
	}
}

// newCounter returns a mutex-guarded id generator producing
// "<prefix>-001", "<prefix>-002", ...
func newCounter(prefix string) func() string {
	var mu sync.Mutex
	n := 0
	return func() string {
		mu.Lock()
		defer mu.Unlock()
		n++
		return fmt.Sprintf("%s-%03d", prefix, n)
	}
}

// StartLoop loads and validates the loop (from loopFile, or
// baseDir/loops/loopName.yaml if loopFile is empty), rejects it if it
// contains any interactive step, and launches it as a new run. It returns
// the new run's id.
func (m *Manager) StartLoop(loopName, loopFile, baseDir, workdir string) (string, error) {
	path := loopFile
	if path == "" {
		if loopName == "" {
			return "", fmt.Errorf("either a loop name or a loop file is required")
		}
		path = filepath.Join(baseDir, "loops", loopName+".yaml")
	}
	loop, err := config.LoadLoop(path)
	if err != nil {
		return "", err
	}
	for _, s := range loop.Steps {
		if s.Type == config.StepInteractive {
			return "", fmt.Errorf("loop %q: interactive step %q is not supported by the daemon (run it with `looper run` instead)", loop.Name, s.Name)
		}
	}

	ctx, cancel := context.WithCancel(context.Background())

	m.mu.Lock()
	runID := m.newID()
	re := &runEntry{
		info:    RunInfo{RunID: runID, LoopName: loop.Name, Status: "running"},
		cancel:  cancel,
		fanIn:   make(chan Update, 256),
		pending: map[string]*pendingRequest{},
	}
	m.runs[runID] = re
	m.mu.Unlock()

	go m.fanout(runID, re.fanIn)

	prompter := &remotePrompter{mgr: m, runID: runID, ctx: ctx}
	w := &runner.Worker{
		Loop:      loop,
		BaseDir:   baseDir,
		Workdir:   workdir,
		Prompter:  prompter,
		Global:    m.global,
		LooperBin: m.looperBin,
		Ctx:       ctx,
		OnReport: func(r runner.Report) {
			m.onReport(runID, loop.Name, r)
		},
	}

	go m.runWorker(runID, ctx, w)

	return runID, nil
}

// runWorker runs w to completion, records the run's final status, publishes
// a run_done Update, and closes the run's internal fan-in channel (which
// lets its fanout goroutine exit).
func (m *Manager) runWorker(runID string, ctx context.Context, w *runner.Worker) {
	runErr := w.Run()

	m.mu.Lock()
	re, ok := m.runs[runID]
	if !ok {
		m.mu.Unlock()
		return
	}
	status := "done"
	errStr := ""
	switch {
	case runErr == nil:
		status = "done"
	case ctx.Err() != nil:
		status = "stopped"
	default:
		status = "error"
		errStr = runErr.Error()
	}
	re.info.Status = status
	re.info.Err = errStr
	loopName := re.info.LoopName
	fanIn := re.fanIn
	m.mu.Unlock()

	m.publish(Update{RunID: runID, Kind: "run_done", LoopName: loopName, State: status, Message: errStr})
	close(fanIn)
}

// StopLoop cancels the run's context, which causes its Worker to stop at
// the next cancellation checkpoint. The run's final status becomes
// "stopped" once the worker returns.
func (m *Manager) StopLoop(runID string) error {
	m.mu.Lock()
	re, ok := m.runs[runID]
	m.mu.Unlock()
	if !ok {
		return fmt.Errorf("no such run %q", runID)
	}
	re.cancel()
	return nil
}

// ListRuns returns a snapshot of all known runs, stable-sorted by run id.
func (m *Manager) ListRuns() []RunInfo {
	m.mu.Lock()
	defer m.mu.Unlock()
	infos := make([]RunInfo, 0, len(m.runs))
	for _, re := range m.runs {
		infos = append(infos, re.info)
	}
	sort.Slice(infos, func(i, j int) bool { return infos[i].RunID < infos[j].RunID })
	return infos
}

// Subscribe registers a new subscriber and returns its update channel (cap
// 64) and an unsubscribe function that removes and closes it. An empty
// runID subscribes to updates from all runs. On subscribe, a synthetic
// "state" Update is sent for each currently-known matching run so a late
// subscriber immediately sees current state.
func (m *Manager) Subscribe(runID string) (<-chan Update, func()) {
	sub := &subscriber{ch: make(chan Update, 64), runID: runID}

	m.mu.Lock()
	id := m.nextSubID
	m.nextSubID++
	m.subs[id] = sub

	var snapshot []RunInfo
	var pendingUpdates []Update
	for rid, re := range m.runs {
		if runID == "" || rid == runID {
			snapshot = append(snapshot, re.info)
			for _, p := range re.pending {
				pendingUpdates = append(pendingUpdates, p.update)
			}
		}
	}
	m.mu.Unlock()

	for _, info := range snapshot {
		sub.send(Update{
			RunID: info.RunID, Kind: "state", LoopName: info.LoopName,
			Iteration: info.Iteration, Step: info.CurrentStep,
			State: info.State, Message: info.Err,
		})
	}
	// Replay any still-unanswered decision requests: a subscriber that
	// joins after one was published (and thus missed it on the fan-out)
	// must still see it, since the worker is blocked indefinitely on it.
	for _, u := range pendingUpdates {
		sub.send(u)
	}

	unsub := func() {
		m.mu.Lock()
		delete(m.subs, id)
		m.mu.Unlock()
		sub.close()
	}
	return sub.ch, unsub
}

// Respond delivers a decision outcome ("advance", "retry", or "abort") to
// the pending request requestID on run runID, unblocking whichever
// remotePrompter call registered it. It errors if there is no such pending
// request.
func (m *Manager) Respond(runID, requestID, outcome string) error {
	oc, err := parseOutcome(outcome)
	if err != nil {
		return err
	}

	m.mu.Lock()
	re, ok := m.runs[runID]
	if !ok {
		m.mu.Unlock()
		return fmt.Errorf("no such run %q", runID)
	}
	pr, ok := re.pending[requestID]
	if ok {
		delete(re.pending, requestID)
	}
	m.mu.Unlock()

	if !ok {
		return fmt.Errorf("no pending request %q for run %q", requestID, runID)
	}
	pr.outcome <- oc // buffered cap 1; never blocks
	return nil
}

func parseOutcome(outcome string) (runner.Outcome, error) {
	switch outcome {
	case "advance":
		return runner.OutcomeAdvance, nil
	case "retry":
		return runner.OutcomeRetry, nil
	case "abort":
		return runner.OutcomeAbort, nil
	default:
		return 0, fmt.Errorf("unknown decision outcome %q", outcome)
	}
}

// onReport translates a runner.Report into an Update, updates the run's
// tracked status fields, and publishes it.
func (m *Manager) onReport(runID, loopName string, r runner.Report) {
	if r.Kind == runner.ReportRunDone {
		// runWorker publishes its own run_done Update once it knows the
		// final status (done/stopped/error); avoid a redundant/premature one.
		return
	}

	m.mu.Lock()
	if re, ok := m.runs[runID]; ok {
		switch r.Kind {
		case runner.ReportIteration:
			re.info.Iteration = r.Iteration
		case runner.ReportStepStart:
			re.info.CurrentStep = r.Step
		case runner.ReportOutcome:
			re.info.State = r.State
		}
	}
	m.mu.Unlock()

	m.publish(Update{
		RunID: runID, Kind: r.Kind, LoopName: loopName,
		Iteration: r.Iteration, Step: r.Step, State: r.State, Message: r.Message,
	})
}

// publish enqueues u onto its run's internal fan-in channel, which the run's
// fanout goroutine drains and delivers to subscribers. This never blocks on
// a slow subscriber: only on the run's own cap-256 internal buffer.
func (m *Manager) publish(u Update) {
	m.mu.Lock()
	re, ok := m.runs[u.RunID]
	m.mu.Unlock()
	if !ok {
		return
	}
	re.fanIn <- u
}

// fanout drains a run's internal fan-in channel and delivers each Update to
// every subscriber interested in that run (subscribed to it specifically,
// or to all runs). It exits once fanIn is closed (after the run's run_done
// Update has been published).
func (m *Manager) fanout(runID string, fanIn chan Update) {
	for u := range fanIn {
		m.mu.Lock()
		var targets []*subscriber
		for _, s := range m.subs {
			if s.runID == "" || s.runID == runID {
				targets = append(targets, s)
			}
		}
		m.mu.Unlock()

		for _, s := range targets {
			s.send(u)
		}
	}
}

// remotePrompter implements runner.Prompter by round-tripping decisions
// through the Manager: it registers a pending request, publishes a
// decision_request Update, and blocks until Manager.Respond delivers an
// outcome or the run's context is cancelled.
type remotePrompter struct {
	mgr   *Manager
	runID string
	ctx   context.Context
}

func (p *remotePrompter) AskFailure(step config.Step, exitCode int) (runner.Outcome, error) {
	return p.ask(step)
}

func (p *remotePrompter) Manual(step config.Step) (runner.Outcome, error) {
	return p.ask(step)
}

// Interactive is not reachable: StartLoop rejects loops with interactive
// steps. Implemented defensively.
func (p *remotePrompter) Interactive(step config.Step, finalState string) (runner.Outcome, error) {
	return runner.OutcomeAbort, fmt.Errorf("interactive steps are not supported by the daemon")
}

func (p *remotePrompter) ask(step config.Step) (runner.Outcome, error) {
	m := p.mgr
	reqID := m.newReqID()
	outcomeCh := make(chan runner.Outcome, 1)

	m.mu.Lock()
	re, ok := m.runs[p.runID]
	if !ok {
		m.mu.Unlock()
		return runner.OutcomeAbort, fmt.Errorf("no such run %q", p.runID)
	}
	update := Update{
		RunID: p.runID, Kind: "decision_request", LoopName: re.info.LoopName,
		Step: step.Name, RequestID: reqID, Options: []string{"advance", "retry", "abort"},
	}
	re.pending[reqID] = &pendingRequest{outcome: outcomeCh, update: update}
	m.mu.Unlock()

	m.publish(update)

	select {
	case oc := <-outcomeCh:
		return oc, nil
	case <-p.ctx.Done():
		m.mu.Lock()
		delete(re.pending, reqID)
		m.mu.Unlock()
		return runner.OutcomeAbort, fmt.Errorf("run cancelled while awaiting decision: %w", p.ctx.Err())
	}
}
