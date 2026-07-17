package daemon

import (
	"context"
	"io"
	"time"

	"github.com/jbofill10/looper/rpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// StartLoop starts running a loop and returns its run id.
func (s *Server) StartLoop(ctx context.Context, req *rpc.StartLoopRequest) (*rpc.StartLoopResponse, error) {
	runID, err := s.manager.StartLoop(req.GetLoopName(), req.GetLoopFile(), req.GetBaseDir(), req.GetWorkdir(), int(req.GetConcurrency()))
	if err != nil {
		return nil, err
	}
	return &rpc.StartLoopResponse{RunId: runID}, nil
}

// StopLoop stops a running loop.
func (s *Server) StopLoop(ctx context.Context, req *rpc.StopLoopRequest) (*rpc.StopLoopResponse, error) {
	if err := s.manager.StopLoop(req.GetRunId()); err != nil {
		return nil, err
	}
	return &rpc.StopLoopResponse{}, nil
}

// ListRuns returns the current runs known to the daemon.
func (s *Server) ListRuns(ctx context.Context, req *rpc.ListRunsRequest) (*rpc.ListRunsResponse, error) {
	runs := s.manager.ListRuns()
	out := make([]*rpc.RunInfo, len(runs))
	for i, r := range runs {
		out[i] = &rpc.RunInfo{
			RunId:       r.RunID,
			LoopName:    r.LoopName,
			Status:      r.Status,
			Iteration:   int32(r.Iteration),
			CurrentStep: r.CurrentStep,
			State:       r.State,
			Error:       r.Err,
			Workers:     workersToProto(r.Workers),
		}
	}
	return &rpc.ListRunsResponse{Runs: out}, nil
}

// workersToProto maps a run's per-worker snapshot to its wire
// representation.
func workersToProto(workers []WorkerInfo) []*rpc.WorkerInfo {
	if len(workers) == 0 {
		return nil
	}
	out := make([]*rpc.WorkerInfo, len(workers))
	for i, w := range workers {
		out[i] = &rpc.WorkerInfo{
			WorkerId:    w.WorkerID,
			Task:        w.Task,
			Iteration:   int32(w.Iteration),
			CurrentStep: w.CurrentStep,
			State:       w.State,
			Status:      w.Status,
		}
	}
	return out
}

// StreamState streams state updates for a run (or all runs if RunId is
// empty) until the client disconnects or the subscription is closed.
func (s *Server) StreamState(req *rpc.StreamStateRequest, stream rpc.Looper_StreamStateServer) error {
	ch, unsub := s.manager.Subscribe(req.GetRunId())
	defer unsub()

	for {
		select {
		case u, ok := <-ch:
			if !ok {
				return nil
			}
			if err := stream.Send(updateToProto(u)); err != nil {
				return err
			}
		case <-stream.Context().Done():
			return stream.Context().Err()
		}
	}
}

// RespondDecision answers a pending decision request (manual step, failure).
func (s *Server) RespondDecision(ctx context.Context, req *rpc.RespondDecisionRequest) (*rpc.RespondDecisionResponse, error) {
	if err := s.manager.Respond(req.GetRunId(), req.GetRequestId(), req.GetOutcome()); err != nil {
		return nil, err
	}
	return &rpc.RespondDecisionResponse{}, nil
}

// ListLoops lists every loop configured under req.BaseDir.
func (s *Server) ListLoops(ctx context.Context, req *rpc.ListLoopsRequest) (*rpc.ListLoopsResponse, error) {
	summaries, err := s.manager.ListLoops(req.GetBaseDir())
	if err != nil {
		return nil, err
	}
	out := make([]*rpc.LoopInfo, len(summaries))
	for i, l := range summaries {
		var nextRun string
		if !l.NextRun.IsZero() {
			nextRun = l.NextRun.Format(time.RFC3339)
		}
		out[i] = &rpc.LoopInfo{
			Name: l.Name, Path: l.Path, Enabled: l.Enabled, Steps: l.Steps, RunId: l.RunID,
			ScheduleEnabled: l.ScheduleEnabled, NextRun: nextRun,
		}
	}
	return &rpc.ListLoopsResponse{Loops: out}, nil
}

// SetLoopEnabled persists a loop's enabled flag and starts/gracefully
// stops its run accordingly.
func (s *Server) SetLoopEnabled(ctx context.Context, req *rpc.SetLoopEnabledRequest) (*rpc.SetLoopEnabledResponse, error) {
	runID, err := s.manager.SetLoopEnabled(req.GetLoopName(), req.GetBaseDir(), req.GetWorkdir(), req.GetEnabled())
	if err != nil {
		return nil, err
	}
	return &rpc.SetLoopEnabledResponse{RunId: runID}, nil
}

// RunLoopOnce starts a loop as a one-off run.
func (s *Server) RunLoopOnce(ctx context.Context, req *rpc.RunLoopOnceRequest) (*rpc.RunLoopOnceResponse, error) {
	runID, err := s.manager.RunLoopOnce(req.GetLoopName(), req.GetLoopFile(), req.GetBaseDir(), req.GetWorkdir())
	if err != nil {
		return nil, err
	}
	return &rpc.RunLoopOnceResponse{RunId: runID}, nil
}

// StopLoopGraceful lets a run's in-flight iteration finish, then stops it.
func (s *Server) StopLoopGraceful(ctx context.Context, req *rpc.StopLoopGracefulRequest) (*rpc.StopLoopGracefulResponse, error) {
	if err := s.manager.StopLoopGraceful(req.GetRunId()); err != nil {
		return nil, err
	}
	return &rpc.StopLoopGracefulResponse{}, nil
}

// RenameLoop renames a loop's file and registry entry.
func (s *Server) RenameLoop(ctx context.Context, req *rpc.RenameLoopRequest) (*rpc.RenameLoopResponse, error) {
	if err := s.manager.RenameLoop(req.GetLoopName(), req.GetNewName(), req.GetBaseDir()); err != nil {
		return nil, err
	}
	return &rpc.RenameLoopResponse{}, nil
}

// DeleteLoop deletes a loop's file and registry entry.
func (s *Server) DeleteLoop(ctx context.Context, req *rpc.DeleteLoopRequest) (*rpc.DeleteLoopResponse, error) {
	if err := s.manager.DeleteLoop(req.GetLoopName(), req.GetBaseDir()); err != nil {
		return nil, err
	}
	return &rpc.DeleteLoopResponse{}, nil
}

// SetScheduleEnabled persists a loop's schedule-enabled flag. It never
// starts or stops a run itself.
func (s *Server) SetScheduleEnabled(ctx context.Context, req *rpc.SetScheduleEnabledRequest) (*rpc.SetScheduleEnabledResponse, error) {
	if err := s.manager.SetScheduleEnabled(req.GetLoopName(), req.GetBaseDir(), req.GetWorkdir(), req.GetEnabled()); err != nil {
		return nil, err
	}
	return &rpc.SetScheduleEnabledResponse{}, nil
}

// Attach bridges a client's bidi stream to a run's live interactive pty
// session: the session's output is tapped onto the stream (via streamWriter
// + Session.PipeTo), and client messages are forwarded to the session as
// input or a resize. The first message on the stream must be an
// AttachStart naming the run; Attach returns a NotFound error if the run
// has no live session. Attach returns nil once the client closes its send
// direction (io.EOF) or its context is done; the session itself is left
// running (detach, not stop).
func (s *Server) Attach(stream rpc.Looper_AttachServer) error {
	first, err := stream.Recv()
	if err != nil {
		return err
	}
	start := first.GetStart()
	if start == nil || start.GetRunId() == "" {
		return status.Error(codes.InvalidArgument, "attach: first message must be an AttachStart with a run id")
	}

	sess, ok := s.manager.Session(start.GetRunId())
	if !ok {
		return status.Errorf(codes.NotFound, "attach: no live session for run %q", start.GetRunId())
	}

	// PipeTo's writes (the initial scrollback replay, then every live tee)
	// are the only calls to stream.Send for this stream; this loop is the
	// only caller of stream.Recv. That satisfies gRPC's one-sender/
	// one-receiver-per-direction rule without any extra synchronization.
	stop := sess.PipeTo(streamWriter{stream: stream})
	defer stop()

	for {
		in, err := stream.Recv()
		if err != nil {
			if err == io.EOF || stream.Context().Err() != nil {
				return nil
			}
			return err
		}
		switch msg := in.GetMsg().(type) {
		case *rpc.AttachInput_Data:
			if _, err := sess.Write(msg.Data); err != nil {
				return err
			}
		case *rpc.AttachInput_Resize:
			if err := sess.Resize(uint16(msg.Resize.GetRows()), uint16(msg.Resize.GetCols())); err != nil {
				return err
			}
		}
	}
}

// streamWriter adapts rpc.Looper_AttachServer's Send to io.Writer so a
// pty.Session's PipeTo can tap output directly onto an Attach client
// stream.
type streamWriter struct {
	stream rpc.Looper_AttachServer
}

// Write sends p as one AttachOutput message.
func (w streamWriter) Write(p []byte) (int, error) {
	if err := w.stream.Send(&rpc.AttachOutput{Data: p}); err != nil {
		return 0, err
	}
	return len(p), nil
}

// updateToProto maps a daemon.Update to its wire representation.
func updateToProto(u Update) *rpc.StateUpdate {
	return &rpc.StateUpdate{
		RunId:     u.RunID,
		Kind:      u.Kind,
		LoopName:  u.LoopName,
		Iteration: int32(u.Iteration),
		Step:      u.Step,
		State:     u.State,
		Message:   u.Message,
		RequestId: u.RequestID,
		Options:   u.Options,
		WorkerId:  u.WorkerID,
		Task:      u.Task,
	}
}
