package daemon

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jbofill10/looper/config"
	"github.com/jbofill10/looper/rpc"
	"gopkg.in/yaml.v3"
)

// startTestServer starts a Server on a fresh unix socket in t.TempDir and
// returns a connected client. The server is stopped and its listener
// cleaned up via t.Cleanup.
func startTestServer(t *testing.T) rpc.LooperClient {
	t.Helper()
	socketPath := filepath.Join(t.TempDir(), "looper.sock")

	s := New()
	serveErrCh := make(chan error, 1)
	go func() {
		serveErrCh <- s.Serve(socketPath)
	}()
	waitForSocket(t, socketPath, 2*time.Second)

	client, conn := dial(t, socketPath)
	t.Cleanup(func() {
		conn.Close()
		s.Stop()
		select {
		case <-serveErrCh:
		case <-time.After(2 * time.Second):
			t.Errorf("timed out waiting for Serve to return during cleanup")
		}
	})
	return client
}

func writeLoopYAML(t *testing.T, dir, name string, body map[string]any) string {
	t.Helper()
	body["name"] = name
	data, err := yaml.Marshal(body)
	if err != nil {
		t.Fatalf("marshal loop: %v", err)
	}
	path := filepath.Join(dir, name+".yaml")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("write loop file: %v", err)
	}
	return path
}

func recvStateUpdate(t *testing.T, stream rpc.Looper_StreamStateClient) *rpc.StateUpdate {
	t.Helper()
	type result struct {
		u   *rpc.StateUpdate
		err error
	}
	resCh := make(chan result, 1)
	go func() {
		u, err := stream.Recv()
		resCh <- result{u, err}
	}()
	select {
	case r := <-resCh:
		if r.err != nil {
			t.Fatalf("stream.Recv: %v", r.err)
		}
		return r.u
	case <-time.After(5 * time.Second):
		t.Fatalf("timed out waiting for a StateUpdate")
	}
	panic("unreachable")
}

func TestService_StartLoopAndStreamStateToRunDone(t *testing.T) {
	client := startTestServer(t)
	dir := t.TempDir()
	path := writeLoopYAML(t, dir, "l", map[string]any{
		"max_iterations": 1,
		"steps": []map[string]any{
			{"name": "s", "type": "script", "run": "true"},
		},
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	startResp, err := client.StartLoop(ctx, &rpc.StartLoopRequest{
		LoopFile: path,
		BaseDir:  filepath.Join(dir, ".looper"),
		Workdir:  dir,
	})
	if err != nil {
		t.Fatalf("StartLoop: %v", err)
	}
	if startResp.RunId == "" {
		t.Fatalf("expected non-empty run id")
	}

	stream, err := client.StreamState(ctx, &rpc.StreamStateRequest{RunId: startResp.RunId})
	if err != nil {
		t.Fatalf("StreamState: %v", err)
	}

	// The loop's single script step runs near-instantly, so depending on
	// scheduling the client's StreamState call may join after step_start
	// already fanned out to zero subscribers (only "state"/"log"-ish kinds
	// are best-effort; decision_request and the final run_done are the
	// kinds required to be reliable). What must always be observed is the
	// terminal run_done with the correct final status.
	for {
		u := recvStateUpdate(t, stream)
		if u.Kind == "run_done" {
			if u.State != "done" {
				t.Errorf("run_done State = %q, want done", u.State)
			}
			break
		}
	}

	listResp, err := client.ListRuns(ctx, &rpc.ListRunsRequest{})
	if err != nil {
		t.Fatalf("ListRuns: %v", err)
	}
	if len(listResp.Runs) != 1 || listResp.Runs[0].Status != "done" {
		t.Fatalf("ListRuns = %+v, want single done run", listResp.Runs)
	}
}

func TestService_ManualLoopDecisionRequestRoundTrip(t *testing.T) {
	client := startTestServer(t)
	dir := t.TempDir()
	path := writeLoopYAML(t, dir, "l", map[string]any{
		"max_iterations": 1,
		"steps": []map[string]any{
			{"name": "gate", "type": "manual"},
		},
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	startResp, err := client.StartLoop(ctx, &rpc.StartLoopRequest{
		LoopFile: path,
		BaseDir:  filepath.Join(dir, ".looper"),
		Workdir:  dir,
	})
	if err != nil {
		t.Fatalf("StartLoop: %v", err)
	}

	stream, err := client.StreamState(ctx, &rpc.StreamStateRequest{RunId: startResp.RunId})
	if err != nil {
		t.Fatalf("StreamState: %v", err)
	}

	var reqID string
	for {
		u := recvStateUpdate(t, stream)
		if u.Kind == "decision_request" {
			reqID = u.RequestId
			break
		}
	}
	if reqID == "" {
		t.Fatalf("expected a decision_request with a request id")
	}

	if _, err := client.RespondDecision(ctx, &rpc.RespondDecisionRequest{
		RunId:     startResp.RunId,
		RequestId: reqID,
		Outcome:   "advance",
	}); err != nil {
		t.Fatalf("RespondDecision: %v", err)
	}

	for {
		u := recvStateUpdate(t, stream)
		if u.Kind == "run_done" {
			if u.State != "done" {
				t.Errorf("run_done State = %q, want done", u.State)
			}
			break
		}
	}
}

func TestService_StopLoop(t *testing.T) {
	client := startTestServer(t)
	dir := t.TempDir()
	path := writeLoopYAML(t, dir, "l", map[string]any{
		"steps": []map[string]any{
			{"name": "sleep", "type": "script", "run": "sleep 0.2"},
		},
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	startResp, err := client.StartLoop(ctx, &rpc.StartLoopRequest{
		LoopFile: path,
		BaseDir:  filepath.Join(dir, ".looper"),
		Workdir:  dir,
	})
	if err != nil {
		t.Fatalf("StartLoop: %v", err)
	}

	stream, err := client.StreamState(ctx, &rpc.StreamStateRequest{RunId: startResp.RunId})
	if err != nil {
		t.Fatalf("StreamState: %v", err)
	}
	for {
		u := recvStateUpdate(t, stream)
		if u.Kind == "step_start" {
			break
		}
	}

	if _, err := client.StopLoop(ctx, &rpc.StopLoopRequest{RunId: startResp.RunId}); err != nil {
		t.Fatalf("StopLoop: %v", err)
	}

	for {
		u := recvStateUpdate(t, stream)
		if u.Kind == "run_done" {
			if u.State != "stopped" {
				t.Errorf("run_done State = %q, want stopped", u.State)
			}
			break
		}
	}
}

func TestService_StreamStateUnsubscribesOnClientDisconnect(t *testing.T) {
	// Regression guard: StreamState must not leak a subscriber forever once
	// the client goes away. We can't observe internal Manager state from
	// here, so this just exercises that the server keeps functioning after
	// a stream is abandoned mid-flight.
	client := startTestServer(t)
	dir := t.TempDir()
	path := writeLoopYAML(t, dir, "l", map[string]any{
		"max_iterations": 1,
		"steps": []map[string]any{
			{"name": "s", "type": "script", "run": "true"},
		},
	})

	shortCtx, shortCancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	startResp, err := client.StartLoop(shortCtx, &rpc.StartLoopRequest{
		LoopFile: path, BaseDir: filepath.Join(dir, ".looper"), Workdir: dir,
	})
	if err != nil {
		shortCancel()
		t.Fatalf("StartLoop: %v", err)
	}
	stream, err := client.StreamState(shortCtx, &rpc.StreamStateRequest{RunId: startResp.RunId})
	if err != nil {
		shortCancel()
		t.Fatalf("StreamState: %v", err)
	}
	_, _ = stream.Recv()
	shortCancel() // abandon the stream

	// A fresh call should still work fine.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := client.ListRuns(ctx, &rpc.ListRunsRequest{}); err != nil {
		t.Fatalf("ListRuns after abandoned stream: %v", err)
	}

	// Make sure the run's background goroutine finishes (and stops writing
	// under t.TempDir) before the test's cleanup removes the directory.
	deadline := time.Now().Add(5 * time.Second)
	for {
		resp, err := client.ListRuns(ctx, &rpc.ListRunsRequest{})
		if err != nil {
			t.Fatalf("ListRuns: %v", err)
		}
		done := false
		for _, r := range resp.Runs {
			if r.RunId == startResp.RunId && r.Status != "running" {
				done = true
			}
		}
		if done {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("run %s did not finish before deadline", startResp.RunId)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// TestService_ConcurrencyPropagatesToWorkerFields exercises the gRPC surface
// added for concurrency: StartLoopRequest.Concurrency reaches the Manager,
// StateUpdate.worker_id/task are populated on the stream, and
// ListRuns/RunInfo.workers reflects the per-worker table.
func TestService_ConcurrencyPropagatesToWorkerFields(t *testing.T) {
	client := startTestServer(t)
	dir := t.TempDir()
	path := writeLoopYAML(t, dir, "l", map[string]any{
		"concurrency":    2,
		"max_iterations": 1,
		"steps": []map[string]any{
			{"name": "gate", "type": "manual"},
		},
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	startResp, err := client.StartLoop(ctx, &rpc.StartLoopRequest{
		LoopFile:    path,
		BaseDir:     filepath.Join(dir, ".looper"),
		Workdir:     dir,
		Concurrency: 2,
	})
	if err != nil {
		t.Fatalf("StartLoop: %v", err)
	}

	stream, err := client.StreamState(ctx, &rpc.StreamStateRequest{RunId: startResp.RunId})
	if err != nil {
		t.Fatalf("StreamState: %v", err)
	}

	reqByWorker := map[string]string{}
	for len(reqByWorker) < 2 {
		u := recvStateUpdate(t, stream)
		if u.Kind == "decision_request" {
			if u.WorkerId == "" {
				t.Fatalf("decision_request missing worker_id: %+v", u)
			}
			reqByWorker[u.WorkerId] = u.RequestId
		}
	}

	for _, reqID := range reqByWorker {
		if _, err := client.RespondDecision(ctx, &rpc.RespondDecisionRequest{
			RunId: startResp.RunId, RequestId: reqID, Outcome: "advance",
		}); err != nil {
			t.Fatalf("RespondDecision: %v", err)
		}
	}

	for {
		u := recvStateUpdate(t, stream)
		if u.Kind == "run_done" {
			if u.State != "done" {
				t.Errorf("run_done State = %q, want done", u.State)
			}
			break
		}
	}

	listResp, err := client.ListRuns(ctx, &rpc.ListRunsRequest{})
	if err != nil {
		t.Fatalf("ListRuns: %v", err)
	}
	if len(listResp.Runs) != 1 {
		t.Fatalf("ListRuns = %+v, want 1 run", listResp.Runs)
	}
	if len(listResp.Runs[0].Workers) != 2 {
		t.Fatalf("Workers = %+v, want 2 entries", listResp.Runs[0].Workers)
	}
	ids := map[string]bool{}
	for _, w := range listResp.Runs[0].Workers {
		ids[w.WorkerId] = true
	}
	for _, want := range []string{"w1", "w2"} {
		if !ids[want] {
			t.Errorf("missing worker %q in %+v", want, listResp.Runs[0].Workers)
		}
	}
}

func TestService_ListLoopsAndSetLoopEnabled(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	dir := t.TempDir()
	loopsDir := filepath.Join(dir, ".looper", "loops")
	if err := os.MkdirAll(loopsDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	writeLoopYAML(t, loopsDir, "a", map[string]any{
		"steps": []map[string]any{{"name": "s", "type": "script", "run": "true"}},
	})

	c := startTestServer(t)
	ctx := context.Background()

	listResp, err := c.ListLoops(ctx, &rpc.ListLoopsRequest{BaseDir: filepath.Join(dir, ".looper")})
	if err != nil {
		t.Fatalf("ListLoops: %v", err)
	}
	if len(listResp.Loops) != 1 || listResp.Loops[0].Enabled {
		t.Fatalf("loops = %v, want one disabled loop", listResp.Loops)
	}

	setResp, err := c.SetLoopEnabled(ctx, &rpc.SetLoopEnabledRequest{
		LoopName: "a", BaseDir: filepath.Join(dir, ".looper"), Workdir: dir, Enabled: true,
	})
	if err != nil {
		t.Fatalf("SetLoopEnabled: %v", err)
	}
	if setResp.RunId == "" {
		t.Fatalf("SetLoopEnabled did not return a run id")
	}

	listResp, err = c.ListLoops(ctx, &rpc.ListLoopsRequest{BaseDir: filepath.Join(dir, ".looper")})
	if err != nil {
		t.Fatalf("ListLoops after enable: %v", err)
	}
	if !listResp.Loops[0].Enabled || listResp.Loops[0].RunId != setResp.RunId {
		t.Errorf("loops after enable = %v, want enabled with run id %q", listResp.Loops, setResp.RunId)
	}
}

func TestService_RunLoopOnce(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	dir := t.TempDir()
	loopsDir := filepath.Join(dir, ".looper", "loops")
	if err := os.MkdirAll(loopsDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	writeLoopYAML(t, loopsDir, "a", map[string]any{
		"steps": []map[string]any{{"name": "s", "type": "script", "run": "true"}},
	})

	c := startTestServer(t)
	ctx := context.Background()
	resp, err := c.RunLoopOnce(ctx, &rpc.RunLoopOnceRequest{LoopName: "a", BaseDir: filepath.Join(dir, ".looper"), Workdir: dir})
	if err != nil {
		t.Fatalf("RunLoopOnce: %v", err)
	}
	if resp.RunId == "" {
		t.Errorf("RunLoopOnce did not return a run id")
	}
}

func TestService_StopLoopGraceful(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	dir := t.TempDir()
	loopsDir := filepath.Join(dir, ".looper", "loops")
	if err := os.MkdirAll(loopsDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	writeLoopYAML(t, loopsDir, "a", map[string]any{
		"steps": []map[string]any{{"name": "s", "type": "script", "run": "true"}},
	})

	c := startTestServer(t)
	ctx := context.Background()
	startResp, err := c.SetLoopEnabled(ctx, &rpc.SetLoopEnabledRequest{
		LoopName: "a", BaseDir: filepath.Join(dir, ".looper"), Workdir: dir, Enabled: true,
	})
	if err != nil {
		t.Fatalf("SetLoopEnabled: %v", err)
	}
	if _, err := c.StopLoopGraceful(ctx, &rpc.StopLoopGracefulRequest{RunId: startResp.RunId}); err != nil {
		t.Fatalf("StopLoopGraceful: %v", err)
	}
}

func TestService_RenameAndDeleteLoop(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	dir := t.TempDir()
	loopsDir := filepath.Join(dir, ".looper", "loops")
	if err := os.MkdirAll(loopsDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	writeLoopYAML(t, loopsDir, "a", map[string]any{
		"steps": []map[string]any{{"name": "s", "type": "script", "run": "true"}},
	})

	c := startTestServer(t)
	ctx := context.Background()
	if _, err := c.RenameLoop(ctx, &rpc.RenameLoopRequest{LoopName: "a", NewName: "b", BaseDir: filepath.Join(dir, ".looper")}); err != nil {
		t.Fatalf("RenameLoop: %v", err)
	}
	if _, err := os.Stat(filepath.Join(loopsDir, "b.yaml")); err != nil {
		t.Fatalf("renamed file missing: %v", err)
	}

	if _, err := c.DeleteLoop(ctx, &rpc.DeleteLoopRequest{LoopName: "b", BaseDir: filepath.Join(dir, ".looper")}); err != nil {
		t.Fatalf("DeleteLoop: %v", err)
	}
	if _, err := os.Stat(filepath.Join(loopsDir, "b.yaml")); !os.IsNotExist(err) {
		t.Errorf("deleted file still exists")
	}
}

func TestServer_SetScheduleEnabled(t *testing.T) {
	dir := t.TempDir()
	loop := &config.Loop{
		Name:     "a",
		Schedule: &config.Schedule{Every: "1h"},
		Steps:    []config.Step{{Name: "s", Type: config.StepScript, Run: "true"}},
	}
	baseDir := writeLoopsDir(t, dir, loop)

	srv := NewWithGlobal(nil, "looper")
	ctx := context.Background()

	// Prime ListLoops to register baseDir
	if _, err := srv.ListLoops(ctx, &rpc.ListLoopsRequest{BaseDir: baseDir}); err != nil {
		t.Fatalf("ListLoops (prime): %v", err)
	}
	// Rescan schedules to populate cron entries
	srv.manager.rescanSchedules()

	// Enable schedule and verify
	if _, err := srv.SetScheduleEnabled(ctx, &rpc.SetScheduleEnabledRequest{
		LoopName: "a", BaseDir: baseDir, Workdir: dir, Enabled: true,
	}); err != nil {
		t.Fatalf("SetScheduleEnabled(true): %v", err)
	}

	resp, err := srv.ListLoops(ctx, &rpc.ListLoopsRequest{BaseDir: baseDir})
	if err != nil {
		t.Fatalf("ListLoops: %v", err)
	}
	if len(resp.GetLoops()) != 1 || !resp.GetLoops()[0].GetScheduleEnabled() {
		t.Errorf("loops after enable = %v, want one loop with ScheduleEnabled=true", resp.GetLoops())
	}

	// Disable schedule and verify round-trip
	if _, err := srv.SetScheduleEnabled(ctx, &rpc.SetScheduleEnabledRequest{
		LoopName: "a", BaseDir: baseDir, Workdir: dir, Enabled: false,
	}); err != nil {
		t.Fatalf("SetScheduleEnabled(false): %v", err)
	}

	resp, err = srv.ListLoops(ctx, &rpc.ListLoopsRequest{BaseDir: baseDir})
	if err != nil {
		t.Fatalf("ListLoops: %v", err)
	}
	if len(resp.GetLoops()) != 1 || resp.GetLoops()[0].GetScheduleEnabled() {
		t.Errorf("loops after disable = %v, want one loop with ScheduleEnabled=false", resp.GetLoops())
	}
}
