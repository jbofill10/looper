package tui

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/jbofill10/looper/builder"
	"github.com/jbofill10/looper/client"
	"github.com/jbofill10/looper/config"
	"github.com/jbofill10/looper/history"
	"github.com/jbofill10/looper/rpc"
	"github.com/jbofill10/looper/stepauthor"
)

// rpcTimeout bounds the ListRuns/RespondDecision RPCs issued by the program
// wiring (StreamState and Attach are long-lived and unbounded).
const rpcTimeout = 5 * time.Second

// loopsTickInterval bounds how often the program wiring re-fetches the
// Loops-catalog snapshot: there is no push stream for it (unlike
// StreamState for runs), so it's polled.
const loopsTickInterval = 2 * time.Second

// Run builds a Model wired to cl's RPCs and runs it as a Bubble Tea
// program until the user quits or ctx is cancelled. It primes the Model
// with a ListRuns snapshot, forwards cl's StreamState updates to the
// program as StateUpdateMsgs, and closes conn on return.
func Run(ctx context.Context, cl rpc.LooperClient, conn io.Closer) error {
	defer conn.Close()

	wd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("getting working directory: %w", err)
	}
	global, err := config.LoadGlobal(globalConfigPath())
	if err != nil {
		return fmt.Errorf("loading global config: %w", err)
	}

	baseDir := filepath.Join(wd, ".looper")
	var p *tea.Program
	model := NewModel(Options{
		RespondFn:            respondFn(ctx, cl),
		AttachFn:             attachFn(ctx, cl, &p),
		ProjectDir:           wd,
		AuthorFn:             authorFn(&p, global, wd),
		SetLoopEnabledFn:     setLoopEnabledFn(ctx, cl, baseDir, wd),
		SetScheduleEnabledFn: setScheduleEnabledFn(ctx, cl, baseDir, wd),
		RunLoopOnceFn:        runLoopOnceFn(ctx, cl, baseDir, wd),
		StopLoopGracefulFn:   stopLoopGracefulFn(ctx, cl, baseDir),
		AbortLoopFn:          abortLoopFn(ctx, cl, baseDir),
		RenameLoopFn:         renameLoopFn(ctx, cl, baseDir),
		DeleteLoopFn:         deleteLoopFn(ctx, cl, baseDir),
		LoadHistoryFn:        loadHistoryFn(baseDir),
		LoadDigestFn:         loadDigestFn(),
	})
	p = tea.NewProgram(model)

	go sendRunsSnapshot(ctx, p, cl)
	go streamUpdates(ctx, p, cl)
	go pollLoopsSnapshot(ctx, p, cl, baseDir)

	_, err = p.Run()
	return err
}

// globalConfigPath returns the path to looper's global config file:
// $XDG_CONFIG_HOME/looper/config.yaml, or ~/.config/looper/config.yaml.
// Duplicated from cli/run.go's globalPath (cli already imports tui, so
// sharing the other way would cycle; the logic is too small to justify a
// third package).
func globalConfigPath() string {
	if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
		return filepath.Join(xdg, "looper", "config.yaml")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(".config", "looper", "config.yaml")
	}
	return filepath.Join(home, ".config", "looper", "config.yaml")
}

// authorFn returns the Options.AuthorFn implementation: it releases the
// Bubble Tea program's hold on the terminal (mirroring attachFn), runs a
// create/edit-step session via the stepauthor package against the
// "claude" harness, and restores the program's terminal control on
// return. pp captures the *tea.Program variable the same way attachFn
// does.
func authorFn(pp **tea.Program, global *config.Global, wd string) func(builder.AuthorRequest) tea.Cmd {
	return func(req builder.AuthorRequest) tea.Cmd {
		return func() tea.Msg {
			p := *pp
			if p != nil {
				if err := p.ReleaseTerminal(); err != nil {
					return builder.SessionDoneMsg{Err: err}
				}
				defer func() {
					p.RestoreTerminal()
					p.Send(tea.ClearScreen())
				}()
			}

			h, err := global.ResolveHarness("claude")
			if err != nil {
				return builder.SessionDoneMsg{Err: err}
			}

			if req.StepName == "" {
				err = stepauthor.CreateStep(req.ProjectDir, h, req.LoopPath)
			} else {
				err = stepauthor.EditStep(req.ProjectDir, h, req.LoopPath, req.StepName, req.ValidationErr)
			}
			return builder.SessionDoneMsg{Err: err}
		}
	}
}

// respondFn returns the Options.RespondFn implementation: it delivers a
// decision outcome to the daemon via RespondDecision and reports the
// result as a DecisionSentMsg or ErrMsg.
func respondFn(ctx context.Context, cl rpc.LooperClient) func(runID, reqID, outcome string) tea.Cmd {
	return func(runID, reqID, outcome string) tea.Cmd {
		return func() tea.Msg {
			rctx, cancel := context.WithTimeout(ctx, rpcTimeout)
			defer cancel()
			_, err := cl.RespondDecision(rctx, &rpc.RespondDecisionRequest{
				RunId: runID, RequestId: reqID, Outcome: outcome,
			})
			if err != nil {
				return ErrMsg{Err: err}
			}
			return DecisionSentMsg{RunID: runID, RequestID: reqID}
		}
	}
}

// attachFn returns the Options.AttachFn implementation: it releases the
// Bubble Tea program's hold on the terminal, bridges the local terminal to
// the run's live session via the shared client.AttachStream, and restores
// the program's terminal control on return. pp points at the *tea.Program
// variable that Run assigns after constructing it (attachFn is built
// before the Program exists, so it captures the variable, not its value).
func attachFn(ctx context.Context, cl rpc.LooperClient, pp **tea.Program) func(runID string) tea.Cmd {
	return func(runID string) tea.Cmd {
		return func() tea.Msg {
			p := *pp
			if p != nil {
				if err := p.ReleaseTerminal(); err != nil {
					return ErrMsg{Err: err}
				}
				defer func() {
					p.RestoreTerminal()
					p.Send(tea.ClearScreen())
				}()
			}
			if err := client.AttachStream(ctx, cl, runID, os.Stdin, os.Stdout); err != nil {
				return ErrMsg{Err: err}
			}
			return nil
		}
	}
}

// sendRunsSnapshot fetches the daemon's current runs via ListRuns and
// delivers them to p as a RunsSnapshotMsg, so the Model has an initial
// picture of the fleet before the live update stream catches up.
func sendRunsSnapshot(ctx context.Context, p *tea.Program, cl rpc.LooperClient) {
	rctx, cancel := context.WithTimeout(ctx, rpcTimeout)
	defer cancel()
	resp, err := cl.ListRuns(rctx, &rpc.ListRunsRequest{})
	if err != nil {
		p.Send(ErrMsg{Err: err})
		return
	}
	p.Send(RunsSnapshotMsg(runsSnapshotFromProto(resp.GetRuns())))
}

// pollLoopsSnapshot periodically fetches the Loops-catalog snapshot and
// delivers it to p as a LoopsSnapshotMsg. There is no push stream for
// catalog state (unlike StreamState for runs), so it's polled on
// loopsTickInterval until ctx is cancelled.
func pollLoopsSnapshot(ctx context.Context, p *tea.Program, cl rpc.LooperClient, baseDir string) {
	fetch := listLoopsFn(ctx, cl, baseDir)
	ticker := time.NewTicker(loopsTickInterval)
	defer ticker.Stop()
	p.Send(fetch()())
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			p.Send(fetch()())
		}
	}
}

// streamUpdates opens StreamState for all runs and forwards each update to
// p as a StateUpdateMsg until the stream ends or ctx is cancelled.
func streamUpdates(ctx context.Context, p *tea.Program, cl rpc.LooperClient) {
	stream, err := cl.StreamState(ctx, &rpc.StreamStateRequest{})
	if err != nil {
		p.Send(ErrMsg{Err: err})
		return
	}
	for {
		u, err := stream.Recv()
		if err != nil {
			if err != io.EOF {
				p.Send(ErrMsg{Err: err})
			}
			return
		}
		p.Send(updateFromProto(u))
	}
}

// updateFromProto translates one *rpc.StateUpdate from the daemon's
// StreamState RPC into the pure StateUpdateMsg the Model understands. It
// has no side effects, so it is unit-tested directly without a running
// program or a real gRPC connection.
func updateFromProto(u *rpc.StateUpdate) StateUpdateMsg {
	return StateUpdateMsg{
		RunID:     u.GetRunId(),
		Kind:      u.GetKind(),
		LoopName:  u.GetLoopName(),
		Iteration: int(u.GetIteration()),
		Step:      u.GetStep(),
		State:     u.GetState(),
		Message:   u.GetMessage(),
		RequestID: u.GetRequestId(),
		Options:   u.GetOptions(),
		WorkerID:  u.GetWorkerId(),
		Task:      u.GetTask(),
	}
}

// runsSnapshotFromProto translates a ListRuns response's []*rpc.RunInfo
// into the pure []RunSnapshot the Model understands.
func runsSnapshotFromProto(runs []*rpc.RunInfo) []RunSnapshot {
	out := make([]RunSnapshot, 0, len(runs))
	for _, r := range runs {
		workers := make([]WorkerSnapshot, 0, len(r.GetWorkers()))
		for _, w := range r.GetWorkers() {
			workers = append(workers, WorkerSnapshot{
				WorkerID:    w.GetWorkerId(),
				Task:        w.GetTask(),
				Iteration:   int(w.GetIteration()),
				CurrentStep: w.GetCurrentStep(),
				State:       w.GetState(),
				Status:      w.GetStatus(),
			})
		}
		out = append(out, RunSnapshot{
			RunID:       r.GetRunId(),
			LoopName:    r.GetLoopName(),
			Status:      r.GetStatus(),
			Iteration:   int(r.GetIteration()),
			CurrentStep: r.GetCurrentStep(),
			State:       r.GetState(),
			Err:         r.GetError(),
			Workers:     workers,
		})
	}
	return out
}

// loopsSnapshotFromProto translates a ListLoops response's []*rpc.LoopInfo
// into the pure LoopsSnapshotMsg the Model understands.
func loopsSnapshotFromProto(loops []*rpc.LoopInfo) LoopsSnapshotMsg {
	out := make(LoopsSnapshotMsg, 0, len(loops))
	for _, l := range loops {
		out = append(out, LoopSnapshot{
			Name: l.GetName(), Path: l.GetPath(), Enabled: l.GetEnabled(),
			Steps: l.GetSteps(), RunID: l.GetRunId(),
			ScheduleEnabled: l.GetScheduleEnabled(), NextRun: l.GetNextRun(),
		})
	}
	return out
}

// listLoopsFn returns a command factory that fetches the current
// Loops-catalog snapshot; used by pollLoopsSnapshot and by each mutating
// action helper to refresh the view immediately after its RPC succeeds.
func listLoopsFn(ctx context.Context, cl rpc.LooperClient, baseDir string) func() tea.Cmd {
	return func() tea.Cmd {
		return func() tea.Msg {
			rctx, cancel := context.WithTimeout(ctx, rpcTimeout)
			defer cancel()
			resp, err := cl.ListLoops(rctx, &rpc.ListLoopsRequest{BaseDir: baseDir})
			if err != nil {
				return ErrMsg{Err: err}
			}
			return loopsSnapshotFromProto(resp.GetLoops())
		}
	}
}

// setLoopEnabledFn returns the Options.SetLoopEnabledFn implementation.
func setLoopEnabledFn(ctx context.Context, cl rpc.LooperClient, baseDir, workdir string) func(string, bool) tea.Cmd {
	return func(loopName string, enabled bool) tea.Cmd {
		return func() tea.Msg {
			rctx, cancel := context.WithTimeout(ctx, rpcTimeout)
			defer cancel()
			_, err := cl.SetLoopEnabled(rctx, &rpc.SetLoopEnabledRequest{
				LoopName: loopName, BaseDir: baseDir, Workdir: workdir, Enabled: enabled,
			})
			if err != nil {
				return ErrMsg{Err: err}
			}
			return listLoopsFn(ctx, cl, baseDir)()()
		}
	}
}

// setScheduleEnabledFn returns the Options.SetScheduleEnabledFn implementation.
func setScheduleEnabledFn(ctx context.Context, cl rpc.LooperClient, baseDir, workdir string) func(string, bool) tea.Cmd {
	return func(loopName string, enabled bool) tea.Cmd {
		return func() tea.Msg {
			rctx, cancel := context.WithTimeout(ctx, rpcTimeout)
			defer cancel()
			_, err := cl.SetScheduleEnabled(rctx, &rpc.SetScheduleEnabledRequest{
				LoopName: loopName, BaseDir: baseDir, Workdir: workdir, Enabled: enabled,
			})
			if err != nil {
				return ErrMsg{Err: err}
			}
			return listLoopsFn(ctx, cl, baseDir)()()
		}
	}
}

// runLoopOnceFn returns the Options.RunLoopOnceFn implementation.
func runLoopOnceFn(ctx context.Context, cl rpc.LooperClient, baseDir, workdir string) func(string) tea.Cmd {
	return func(loopName string) tea.Cmd {
		return func() tea.Msg {
			rctx, cancel := context.WithTimeout(ctx, rpcTimeout)
			defer cancel()
			_, err := cl.RunLoopOnce(rctx, &rpc.RunLoopOnceRequest{LoopName: loopName, BaseDir: baseDir, Workdir: workdir})
			if err != nil {
				return ErrMsg{Err: err}
			}
			return listLoopsFn(ctx, cl, baseDir)()()
		}
	}
}

// stopLoopGracefulFn returns the Options.StopLoopGracefulFn implementation.
func stopLoopGracefulFn(ctx context.Context, cl rpc.LooperClient, baseDir string) func(string) tea.Cmd {
	return func(runID string) tea.Cmd {
		return func() tea.Msg {
			rctx, cancel := context.WithTimeout(ctx, rpcTimeout)
			defer cancel()
			_, err := cl.StopLoopGraceful(rctx, &rpc.StopLoopGracefulRequest{RunId: runID})
			if err != nil {
				return ErrMsg{Err: err}
			}
			return listLoopsFn(ctx, cl, baseDir)()()
		}
	}
}

// abortLoopFn returns the Options.AbortLoopFn implementation: an
// immediate hard stop via the pre-existing StopLoop RPC (may interrupt an
// in-flight step), as opposed to StopLoopGracefulFn's finish-then-stop.
func abortLoopFn(ctx context.Context, cl rpc.LooperClient, baseDir string) func(string) tea.Cmd {
	return func(runID string) tea.Cmd {
		return func() tea.Msg {
			rctx, cancel := context.WithTimeout(ctx, rpcTimeout)
			defer cancel()
			_, err := cl.StopLoop(rctx, &rpc.StopLoopRequest{RunId: runID})
			if err != nil {
				return ErrMsg{Err: err}
			}
			return listLoopsFn(ctx, cl, baseDir)()()
		}
	}
}

// renameLoopFn returns the Options.RenameLoopFn implementation.
func renameLoopFn(ctx context.Context, cl rpc.LooperClient, baseDir string) func(string, string) tea.Cmd {
	return func(loopName, newName string) tea.Cmd {
		return func() tea.Msg {
			rctx, cancel := context.WithTimeout(ctx, rpcTimeout)
			defer cancel()
			_, err := cl.RenameLoop(rctx, &rpc.RenameLoopRequest{LoopName: loopName, NewName: newName, BaseDir: baseDir})
			if err != nil {
				return ErrMsg{Err: err}
			}
			return listLoopsFn(ctx, cl, baseDir)()()
		}
	}
}

// deleteLoopFn returns the Options.DeleteLoopFn implementation.
func deleteLoopFn(ctx context.Context, cl rpc.LooperClient, baseDir string) func(string) tea.Cmd {
	return func(loopName string) tea.Cmd {
		return func() tea.Msg {
			rctx, cancel := context.WithTimeout(ctx, rpcTimeout)
			defer cancel()
			_, err := cl.DeleteLoop(rctx, &rpc.DeleteLoopRequest{LoopName: loopName, BaseDir: baseDir})
			if err != nil {
				return ErrMsg{Err: err}
			}
			return listLoopsFn(ctx, cl, baseDir)()()
		}
	}
}

// loadHistoryFn returns the Options.LoadHistoryFn implementation: it loads
// loopName's step names from its loop file (to preserve config step order
// and label steps with no digest), scans its run directory on disk via
// history.Scan, and reports the result as a HistorySnapshotMsg.
func loadHistoryFn(baseDir string) func(loopName string) tea.Cmd {
	return func(loopName string) tea.Cmd {
		return func() tea.Msg {
			loop, err := config.LoadLoopLenient(filepath.Join(baseDir, "loops", loopName+".yaml"))
			if err != nil {
				return ErrMsg{Err: err}
			}
			stepNames := make([]string, len(loop.Steps))
			for i, s := range loop.Steps {
				stepNames[i] = s.Name
			}
			entries, err := history.Scan(baseDir, loopName, stepNames)
			if err != nil {
				return ErrMsg{Err: err}
			}
			return HistorySnapshotMsg{LoopName: loopName, Entries: entries}
		}
	}
}

// loadDigestFn returns the Options.LoadDigestFn implementation: it reads one
// step's captured digest content for a specific run-history entry via
// history.Digest and reports it as a DigestContentMsg.
func loadDigestFn() func(loopName string, entry history.Entry, step string) tea.Cmd {
	return func(loopName string, entry history.Entry, step string) tea.Cmd {
		return func() tea.Msg {
			content, err := history.Digest(entry.Dir, step)
			if err != nil {
				return ErrMsg{Err: err}
			}
			return DigestContentMsg{Step: step, Content: content}
		}
	}
}
