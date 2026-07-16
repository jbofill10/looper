package daemon

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/jbofill10/looper/config"
	"github.com/jbofill10/looper/pty"
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

	// WorkerID identifies which worker this update is about; empty for a
	// single-worker run (concurrency=1, the default) preserves prior
	// behavior.
	WorkerID string
	// Task is the worker's current work unit (its task var's value).
	Task string
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
	// Workers is a stable (by worker id), point-in-time snapshot of each
	// worker's status. Empty for a run whose workers haven't been recorded
	// yet.
	Workers []WorkerInfo
}

// WorkerInfo is a point-in-time snapshot of one worker's status within a
// run.
type WorkerInfo struct {
	WorkerID    string
	Task        string
	Iteration   int
	CurrentStep string
	State       string
	Status      string // running | done | stopped | error
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

// workerState tracks one worker's latest reported status within a run.
type workerState struct {
	task        string
	iteration   int
	currentStep string
	state       string
	status      string // running | done | stopped | error
}

// runEntry tracks one active or finished run.
type runEntry struct {
	info    RunInfo
	cancel  context.CancelFunc
	fanIn   chan Update // internal buffer; drained by a per-run fanout goroutine
	pending map[string]*pendingRequest

	// workers, workersRemaining, and firstErr are guarded by Manager.mu
	// (the same lock that guards info and pending), so a worker's
	// completion handler can atomically update per-worker state, decrement
	// the remaining count, and decide whether it is the last worker to
	// finish (and so must finalize the run) without a separate lock.
	workers          map[string]*workerState
	workersRemaining int
	firstErr         error // first non-cancellation error from any worker

	baseDir string // the .looper dir this run was started from
	workdir string // execution dir this run was started from

	graceful     chan struct{}
	gracefulOnce sync.Once

	sessMu  sync.Mutex
	session *pty.Session // the run's live interactive session, if any
}

// setSession registers sess as the run's live interactive session, making it
// visible to Manager.Session (and thus to an Attach RPC).
func (re *runEntry) setSession(sess *pty.Session) {
	re.sessMu.Lock()
	defer re.sessMu.Unlock()
	re.session = sess
}

// clearSession unregisters the run's live interactive session.
func (re *runEntry) clearSession() {
	re.sessMu.Lock()
	defer re.sessMu.Unlock()
	re.session = nil
}

// currentSession returns the run's live interactive session, or nil if none
// is registered.
func (re *runEntry) currentSession() *pty.Session {
	re.sessMu.Lock()
	defer re.sessMu.Unlock()
	return re.session
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

	registryPath string
}

// NewManager returns a Manager for orchestrating loops. A nil global uses
// config.DefaultGlobal().
func NewManager(global *config.Global, looperBin string) *Manager {
	if global == nil {
		global = config.DefaultGlobal()
	}
	return &Manager{
		global:       global,
		looperBin:    looperBin,
		newID:        newCounter("run"),
		newReqID:     newCounter("req"),
		runs:         map[string]*runEntry{},
		subs:         map[int]*subscriber{},
		registryPath: defaultRegistryPath(),
	}
}

// SetRegistryPath overrides the daemon-wide enabled-loops registry path
// (defaultRegistryPath() otherwise). Tests use this to avoid touching the
// real per-user registry file.
func (m *Manager) SetRegistryPath(path string) {
	m.registryPath = path
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
// baseDir/loops/loopName.yaml if loopFile is empty) and launches it as a new
// run of concurrency workers (0 uses the loop's configured concurrency,
// clamped to [1, loop.MaxConcurrency]). Interactive steps run inside the
// daemon: each session is registered on the run (see Manager.Session) for a
// client to attach to, rather than being auto-attached to the daemon's own
// stdio. It returns the new run's id.
func (m *Manager) StartLoop(loopName, loopFile, baseDir, workdir string, concurrency int) (string, error) {
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

	n := concurrency
	if n <= 0 {
		n = loop.Concurrency
	}
	if n < 1 {
		n = 1
	}
	if loop.MaxConcurrency > 0 && n > loop.MaxConcurrency {
		n = loop.MaxConcurrency
	}

	ctx, cancel := context.WithCancel(context.Background())

	m.mu.Lock()
	runID := m.newID()
	re := &runEntry{
		info:             RunInfo{RunID: runID, LoopName: loop.Name, Status: "running"},
		cancel:           cancel,
		fanIn:            make(chan Update, 256),
		pending:          map[string]*pendingRequest{},
		workers:          map[string]*workerState{},
		workersRemaining: n,
		baseDir:          baseDir,
		workdir:          workdir,
		graceful:         make(chan struct{}),
	}
	m.runs[runID] = re
	m.mu.Unlock()

	go m.fanout(runID, re.fanIn)

	// acquireLock is shared by every worker of this run so that steps with
	// signals_no_work (task acquisition) are serialized across workers:
	// two workers never pull concurrently.
	acquireLock := &sync.Mutex{}

	for i := 1; i <= n; i++ {
		workerID := fmt.Sprintf("w%d", i)

		m.mu.Lock()
		re.workers[workerID] = &workerState{status: "running"}
		m.mu.Unlock()

		prompter := &remotePrompter{mgr: m, runID: runID, ctx: ctx, workerID: workerID}
		w := &runner.Worker{
			Loop:         loop,
			BaseDir:      baseDir,
			Workdir:      workdir,
			Prompter:     prompter,
			Global:       m.global,
			LooperBin:    m.looperBin,
			Ctx:          ctx,
			ID:           workerID,
			TaskVar:      loop.TaskVar,
			AcquireLock:  acquireLock,
			GracefulStop: re.graceful,
			InteractiveRun: func(argv, env []string, socketPath string) error {
				return m.runInteractiveSession(ctx, re, runID, argv, env)
			},
			OnReport: func(r runner.Report) {
				m.onReport(runID, loop.Name, r)
			},
		}

		go m.runWorker(runID, workerID, ctx, w)
	}

	return runID, nil
}

// runWorker runs w to completion and records workerID's final status. Once
// every worker of the run has finished, it computes the run's aggregate
// status ("done" only if all workers succeeded; "stopped" if the run's
// context was cancelled; "error" if any worker returned a non-cancellation
// error), publishes a run_done Update, and closes the run's internal fan-in
// channel (which lets its fanout goroutine exit).
func (m *Manager) runWorker(runID, workerID string, ctx context.Context, w *runner.Worker) {
	runErr := w.Run()

	m.mu.Lock()
	re, ok := m.runs[runID]
	if !ok {
		m.mu.Unlock()
		return
	}

	if ws, ok := re.workers[workerID]; ok {
		switch {
		case runErr == nil:
			ws.status = "done"
		case ctx.Err() != nil:
			ws.status = "stopped"
		default:
			ws.status = "error"
		}
	}
	if runErr != nil && ctx.Err() == nil && re.firstErr == nil {
		re.firstErr = runErr
	}

	re.workersRemaining--
	var (
		finalize       bool
		status, errStr string
		loopName       string
		fanIn          chan Update
	)
	if re.workersRemaining == 0 {
		finalize = true
		switch {
		case ctx.Err() != nil:
			status = "stopped"
		case re.firstErr != nil:
			status = "error"
			errStr = re.firstErr.Error()
		default:
			status = "done"
		}
		re.info.Status = status
		re.info.Err = errStr
		loopName = re.info.LoopName
		fanIn = re.fanIn
	}
	m.mu.Unlock()

	if finalize {
		m.publish(Update{RunID: runID, Kind: "run_done", LoopName: loopName, State: status, Message: errStr})
		close(fanIn)
	}
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

// StopLoopGraceful signals the run's workers to finish their current
// iteration and then stop, without cancelling the run's context — unlike
// StopLoop, an in-flight step is not interrupted. The run's final status
// becomes "done" once every worker returns (a graceful stop is a normal
// end of run, not an error or cancellation). Calling it more than once for
// the same run is a no-op.
func (m *Manager) StopLoopGraceful(runID string) error {
	m.mu.Lock()
	re, ok := m.runs[runID]
	m.mu.Unlock()
	if !ok {
		return fmt.Errorf("no such run %q", runID)
	}
	re.gracefulOnce.Do(func() { close(re.graceful) })
	return nil
}

// Session returns the live *pty.Session for runID, and whether one is
// currently registered. It is used by the Attach RPC handler to bridge a
// client stream to the run's interactive session.
func (m *Manager) Session(runID string) (*pty.Session, bool) {
	m.mu.Lock()
	re, ok := m.runs[runID]
	m.mu.Unlock()
	if !ok {
		return nil, false
	}
	sess := re.currentSession()
	return sess, sess != nil
}

// runInteractiveSession is the daemon's runner.Worker.InteractiveRun
// implementation: it starts argv in a looper-owned pty, registers the
// session on re for remote attach (Manager.Session), and waits for it to
// exit. Unlike the local `looper run` default (runPTY), it does not
// auto-attach to the daemon's own stdio; a client attaches remotely via the
// Attach RPC. If ctx is cancelled (StopLoop) before the session exits on its
// own, the session is killed so the run can end promptly.
func (m *Manager) runInteractiveSession(ctx context.Context, re *runEntry, runID string, argv, env []string) error {
	sess, err := pty.Start(pty.Config{Argv: argv, Env: env, Dir: workdirFromEnv(env)})
	if err != nil {
		return err
	}
	re.setSession(sess)
	defer re.clearSession()

	m.publish(Update{RunID: runID, Kind: "state", State: "session_live"})

	waitErr := make(chan error, 1)
	go func() { waitErr <- sess.Wait() }()

	select {
	case err := <-waitErr:
		_ = sess.Close()
		return err
	case <-ctx.Done():
		_ = sess.Close()
		return <-waitErr
	}
}

// workdirFromEnv extracts the WORKDIR entry from a KEY=VALUE env slice, as
// set by runctx for interactive/headless steps.
func workdirFromEnv(env []string) string {
	for _, kv := range env {
		if k, v, ok := strings.Cut(kv, "="); ok && k == "WORKDIR" {
			return v
		}
	}
	return ""
}

// ListRuns returns a snapshot of all known runs, stable-sorted by run id.
// Each run's Workers field is populated from its worker table, stable-sorted
// by worker id.
func (m *Manager) ListRuns() []RunInfo {
	m.mu.Lock()
	defer m.mu.Unlock()
	infos := make([]RunInfo, 0, len(m.runs))
	for _, re := range m.runs {
		info := re.info
		info.Workers = workersSnapshot(re.workers)
		infos = append(infos, info)
	}
	sort.Slice(infos, func(i, j int) bool { return infos[i].RunID < infos[j].RunID })
	return infos
}

// workersSnapshot converts a run's internal worker table into a
// stable-sorted (by worker id) []WorkerInfo. Returns nil for an empty table.
func workersSnapshot(workers map[string]*workerState) []WorkerInfo {
	if len(workers) == 0 {
		return nil
	}
	out := make([]WorkerInfo, 0, len(workers))
	for id, ws := range workers {
		out = append(out, WorkerInfo{
			WorkerID:    id,
			Task:        ws.task,
			Iteration:   ws.iteration,
			CurrentStep: ws.currentStep,
			State:       ws.state,
			Status:      ws.status,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].WorkerID < out[j].WorkerID })
	return out
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
// tracked status fields (both the run's aggregate view and, if the report
// carries a WorkerID, that worker's entry), and publishes it.
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
		if ws, ok := re.workers[r.WorkerID]; ok {
			switch r.Kind {
			case runner.ReportIteration:
				ws.iteration = r.Iteration
			case runner.ReportStepStart:
				ws.currentStep = r.Step
			case runner.ReportOutcome:
				ws.state = r.State
			}
			if r.Task != "" {
				ws.task = r.Task
			}
		}
	}
	m.mu.Unlock()

	m.publish(Update{
		RunID: runID, Kind: r.Kind, LoopName: loopName,
		Iteration: r.Iteration, Step: r.Step, State: r.State, Message: r.Message,
		WorkerID: r.WorkerID, Task: r.Task,
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
// outcome or the run's context is cancelled. Each worker of a run gets its
// own remotePrompter (so its decision_request carries that worker's id),
// but all of a run's remotePrompters share the run's pending map, keyed by
// the globally-unique request id.
type remotePrompter struct {
	mgr      *Manager
	runID    string
	ctx      context.Context
	workerID string
}

func (p *remotePrompter) AskFailure(step config.Step, exitCode int) (runner.Outcome, error) {
	return p.ask(step)
}

func (p *remotePrompter) Manual(step config.Step) (runner.Outcome, error) {
	return p.ask(step)
}

// Interactive round-trips the decision the same way Manual/AskFailure do:
// the client watching the run's state stream decides advance/retry/abort
// once the session's hook-derived final state is known. finalState is
// already visible to the client via the "state" Updates published while
// the session ran, so it isn't repeated here.
func (p *remotePrompter) Interactive(step config.Step, finalState string) (runner.Outcome, error) {
	return p.ask(step)
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
		WorkerID: p.workerID,
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
