package daemon

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jbofill10/looper/config"
	"github.com/jbofill10/looper/rpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// startTestServerWithGlobal is like startTestServer but lets the caller
// supply a custom Global (e.g. one whose default harness's interactive
// command is `sh -c cat`, for attach tests that don't need a real `claude`).
func startTestServerWithGlobal(t *testing.T, global *config.Global) rpc.LooperClient {
	t.Helper()
	socketPath := filepath.Join(t.TempDir(), "looper.sock")

	s := &Server{manager: NewManager(global, "looper")}
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

// recvAttachOutput calls stream.Recv with a bounded timeout so a hung
// handler fails the test promptly instead of blocking forever.
func recvAttachOutput(t *testing.T, stream rpc.Looper_AttachClient) (*rpc.AttachOutput, error) {
	t.Helper()
	type result struct {
		out *rpc.AttachOutput
		err error
	}
	resCh := make(chan result, 1)
	go func() {
		out, err := stream.Recv()
		resCh <- result{out, err}
	}()
	select {
	case r := <-resCh:
		return r.out, r.err
	case <-time.After(5 * time.Second):
		t.Fatalf("timed out waiting for AttachOutput")
	}
	panic("unreachable")
}

// waitForSessionLive blocks until a "state"/"session_live" Update arrives on
// stream, which Manager.runInteractiveSession publishes once the run's pty
// session is registered and ready to be attached to.
func waitForSessionLive(t *testing.T, stream rpc.Looper_StreamStateClient) {
	t.Helper()
	for {
		u := recvStateUpdate(t, stream)
		if u.Kind == "state" && u.State == "session_live" {
			return
		}
	}
}

func TestService_AttachStreamsInputAndOutput(t *testing.T) {
	client := startTestServerWithGlobal(t, catHarnessGlobal())
	dir := t.TempDir()
	path := writeLoopYAML(t, dir, "l", map[string]any{
		"steps": []map[string]any{
			{"name": "sess", "type": "interactive", "prompt": "hi"},
		},
	})

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	startResp, err := client.StartLoop(ctx, &rpc.StartLoopRequest{
		LoopFile: path, BaseDir: filepath.Join(dir, ".looper"), Workdir: dir,
	})
	if err != nil {
		t.Fatalf("StartLoop: %v", err)
	}

	stateStream, err := client.StreamState(ctx, &rpc.StreamStateRequest{RunId: startResp.RunId})
	if err != nil {
		t.Fatalf("StreamState: %v", err)
	}
	waitForSessionLive(t, stateStream)

	attachStream, err := client.Attach(ctx)
	if err != nil {
		t.Fatalf("Attach: %v", err)
	}
	if err := attachStream.Send(&rpc.AttachInput{Msg: &rpc.AttachInput_Start{
		Start: &rpc.AttachStart{RunId: startResp.RunId},
	}}); err != nil {
		t.Fatalf("send AttachStart: %v", err)
	}
	if err := attachStream.Send(&rpc.AttachInput{Msg: &rpc.AttachInput_Data{
		Data: []byte("hello\n"),
	}}); err != nil {
		t.Fatalf("send data: %v", err)
	}

	var got strings.Builder
	deadline := time.Now().Add(8 * time.Second)
	for !strings.Contains(got.String(), "hello") {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for echoed data; got %q", got.String())
		}
		out, err := recvAttachOutput(t, attachStream)
		if err != nil {
			t.Fatalf("Recv: %v", err)
		}
		got.Write(out.Data)
	}

	if err := attachStream.Send(&rpc.AttachInput{Msg: &rpc.AttachInput_Resize{
		Resize: &rpc.Resize{Rows: 40, Cols: 120},
	}}); err != nil {
		t.Fatalf("send resize: %v", err)
	}

	if err := attachStream.CloseSend(); err != nil {
		t.Fatalf("CloseSend: %v", err)
	}

	// Once the client closes its send direction, the handler's Recv loop
	// sees io.EOF and returns, which ends the server's side of the stream.
	// A real pty echoes input at the tty layer in addition to cat's own
	// stdout echo, so a little more already-in-flight data may still
	// arrive; drain it until the stream actually ends (any error, ideally
	// io.EOF), bounded so a truly hung stream still fails the test.
	endDeadline := time.Now().Add(8 * time.Second)
	for {
		if time.Now().After(endDeadline) {
			t.Fatalf("stream did not end after CloseSend")
		}
		if _, err := recvAttachOutput(t, attachStream); err != nil {
			break
		}
	}
}

func TestService_AttachToNonexistentRunErrors(t *testing.T) {
	client := startTestServerWithGlobal(t, catHarnessGlobal())

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	stream, err := client.Attach(ctx)
	if err != nil {
		t.Fatalf("Attach: %v", err)
	}
	if err := stream.Send(&rpc.AttachInput{Msg: &rpc.AttachInput_Start{
		Start: &rpc.AttachStart{RunId: "no-such-run"},
	}}); err != nil {
		t.Fatalf("send AttachStart: %v", err)
	}

	_, err = recvAttachOutput(t, stream)
	if err == nil {
		t.Fatalf("expected an error attaching to a nonexistent run")
	}
	if st, ok := status.FromError(err); !ok || st.Code() != codes.NotFound {
		t.Errorf("error = %v, want codes.NotFound", err)
	}
}
