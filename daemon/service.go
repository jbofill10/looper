package daemon

import (
	"context"
	"io"

	"github.com/jbofill10/looper/rpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// StartLoop starts running a loop and returns its run id.
func (s *Server) StartLoop(ctx context.Context, req *rpc.StartLoopRequest) (*rpc.StartLoopResponse, error) {
	runID, err := s.manager.StartLoop(req.GetLoopName(), req.GetLoopFile(), req.GetBaseDir(), req.GetWorkdir())
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
		}
	}
	return &rpc.ListRunsResponse{Runs: out}, nil
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
	}
}
