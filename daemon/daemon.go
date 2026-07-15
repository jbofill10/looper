// Package daemon implements looperd's gRPC service and serving lifecycle.
package daemon

import (
	"context"
	"fmt"
	"net"
	"os"
	"sync/atomic"

	"github.com/jbofill10/looper/rpc"
	"google.golang.org/grpc"
)

// Version is looperd's version string.
const Version = "0.1.0-dev"

// Server implements rpc.LooperServer and manages the daemon's gRPC serving
// lifecycle over a Unix socket.
type Server struct {
	rpc.UnimplementedLooperServer

	grpcServer atomic.Pointer[grpc.Server]
}

// New returns a new, unstarted Server.
func New() *Server {
	return &Server{}
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

	lis, err := net.Listen("unix", socketPath)
	if err != nil {
		return fmt.Errorf("listening on %s: %w", socketPath, err)
	}
	defer os.Remove(socketPath)

	grpcServer := grpc.NewServer()
	rpc.RegisterLooperServer(grpcServer, s)
	s.grpcServer.Store(grpcServer)

	if err := grpcServer.Serve(lis); err != nil {
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
