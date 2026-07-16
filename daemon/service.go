package daemon

import (
	"context"

	"github.com/jbofill10/looper/rpc"
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
