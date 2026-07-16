// Package daemon implements looperd's gRPC service and serving lifecycle.
package daemon

import (
	"context"
	"fmt"
	"net"
	"os"
	"sync/atomic"

	"github.com/jbofill10/looper/config"
	"github.com/jbofill10/looper/rpc"
	"google.golang.org/grpc"
)

// Version is looperd's version string.
const Version = "0.1.0-dev"

// Server implements rpc.LooperServer and manages the daemon's gRPC serving
// lifecycle over a Unix socket.
type Server struct {
	rpc.UnimplementedLooperServer

	manager    *Manager
	grpcServer atomic.Pointer[grpc.Server]
}

// New returns a new, unstarted Server backed by a fresh Manager. The
// Manager's looperBin is os.Executable(), falling back to "looper" if that
// cannot be determined.
func New() *Server {
	looperBin, err := os.Executable()
	if err != nil {
		looperBin = "looper"
	}
	return &Server{manager: NewManager(nil, looperBin)}
}

// NewWithGlobal is like New but lets the caller supply the harness/global
// configuration explicitly instead of config.DefaultGlobal(). It exists
// primarily so tests outside package daemon (e.g. the cli package's attach
// smoke test) can stand up a real daemon with a non-`claude` interactive
// harness such as `sh -c cat`, without needing a real coding-agent binary.
func NewWithGlobal(global *config.Global, looperBin string) *Server {
	return &Server{manager: NewManager(global, looperBin)}
}

// Ping reports the daemon's version.
func (s *Server) Ping(context.Context, *rpc.PingRequest) (*rpc.PingResponse, error) {
	return &rpc.PingResponse{Version: Version}, nil
}

// Shutdown asks the daemon to stop gracefully. It triggers the graceful stop
// asynchronously so the RPC can return a response before Serve unblocks.
func (s *Server) Shutdown(context.Context, *rpc.ShutdownRequest) (*rpc.ShutdownResponse, error) {
	go s.Stop()
	return &rpc.ShutdownResponse{}, nil
}

// Serve removes any stale socket file at socketPath, listens on it, and
// serves the Looper gRPC service until Stop is called (or a Shutdown RPC is
// received). It removes the socket file before returning.
func (s *Server) Serve(socketPath string) error {
	if err := os.Remove(socketPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("removing stale socket %s: %w", socketPath, err)
	}

	// Create and publish the gRPC server BEFORE listening, so that once the
	// socket file exists (created by net.Listen) a concurrent Stop is
	// guaranteed to observe a non-nil server. Otherwise Stop could load a nil
	// pointer and no-op, leaving Serve running forever.
	grpcServer := grpc.NewServer()
	rpc.RegisterLooperServer(grpcServer, s)
	s.grpcServer.Store(grpcServer)

	lis, err := net.Listen("unix", socketPath)
	if err != nil {
		return fmt.Errorf("listening on %s: %w", socketPath, err)
	}
	defer os.Remove(socketPath)

	// ErrServerStopped is a normal outcome when Stop/GracefulStop is called
	// (including before or during the Serve call), not a serving failure.
	if err := grpcServer.Serve(lis); err != nil && err != grpc.ErrServerStopped {
		return fmt.Errorf("serving on %s: %w", socketPath, err)
	}
	return nil
}

// Stop gracefully stops the server. It is safe to call even if Serve has not
// been called yet or has already returned.
func (s *Server) Stop() {
	if gs := s.grpcServer.Load(); gs != nil {
		gs.GracefulStop()
	}
}

// AutoResume starts every registry entry marked enabled. Called once by
// the `looper daemon` command right before Serve, so a freshly (re)started
// daemon picks back up whatever loops were enabled before it last stopped.
func (s *Server) AutoResume() []error {
	return s.manager.AutoResume()
}
