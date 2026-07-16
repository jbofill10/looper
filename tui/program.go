package tui

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/jbofill10/looper/client"
	"github.com/jbofill10/looper/config"
	"github.com/jbofill10/looper/rpc"
)

// rpcTimeout bounds the ListRuns/RespondDecision RPCs issued by the program
// wiring (StreamState and Attach are long-lived and unbounded).
const rpcTimeout = 5 * time.Second

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

	var p *tea.Program
	model := NewModel(Options{
		RespondFn:  respondFn(ctx, cl),
		AttachFn:   attachFn(ctx, cl, &p),
		SaveLoopFn: saveLoopFn(wd),
	})
	p = tea.NewProgram(model)

	go sendRunsSnapshot(ctx, p, cl)
	go streamUpdates(ctx, p, cl)

	_, err = p.Run()
	return err
}

// saveLoopFn returns the Options.SaveLoopFn implementation used by the
// running fleet TUI: it saves loop to <dir>/.looper/loops/<name>.yaml via
// config.SaveLoop, mirroring cli/build.go's buildAndSave (duplicated here
// rather than shared, since cli already imports tui and importing the
// other way would cycle).
func saveLoopFn(dir string) func(loop *config.Loop) (string, error) {
	return func(loop *config.Loop) (string, error) {
		path := filepath.Join(dir, ".looper", "loops", loop.Name+".yaml")
		if err := config.SaveLoop(loop, path); err != nil {
			return "", err
		}
		return path, nil
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
				defer p.RestoreTerminal()
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
