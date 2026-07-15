package events

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"sync"
)

// Listener accepts hook connections on a Unix socket, decodes each
// connection's payload as a Hook, and delivers them on Events().
type Listener struct {
	path     string
	ln       net.Listener
	events   chan Hook
	done     chan struct{} // closed once the accept loop has returned
	closeErr error
	closeOnc sync.Once
}

// Listen starts listening on socketPath and spawns the accept loop. The
// caller must call Close to release the listener and remove the socket file.
func Listen(socketPath string) (*Listener, error) {
	ln, err := net.Listen("unix", socketPath)
	if err != nil {
		return nil, fmt.Errorf("listen on %s: %w", socketPath, err)
	}
	l := &Listener{
		path:   socketPath,
		ln:     ln,
		events: make(chan Hook),
		done:   make(chan struct{}),
	}
	go l.acceptLoop()
	return l, nil
}

// acceptLoop accepts connections until the listener is closed, decoding each
// connection's body into a Hook and sending it on l.events. It owns l.events
// exclusively: it is the only goroutine that ever sends on or closes it.
func (l *Listener) acceptLoop() {
	defer close(l.done)
	for {
		conn, err := l.ln.Accept()
		if err != nil {
			// Accept fails once the listener is closed; exit cleanly.
			return
		}
		l.handleConn(conn)
	}
}

func (l *Listener) handleConn(conn net.Conn) {
	defer conn.Close()
	data, err := io.ReadAll(conn)
	if err != nil {
		return
	}
	var h Hook
	if err := json.Unmarshal(data, &h); err != nil {
		return
	}
	l.events <- h
	// Acknowledge only after the event has been handed to a receiver on
	// l.events, so a caller that waits for this ack (e.g. the hook
	// forwarder) is guaranteed the event was durably delivered before it
	// returns. This lets callers safely close the listener once every
	// forwarder has returned, without racing an unaccepted connection
	// still sitting in the kernel's backlog.
	_, _ = conn.Write([]byte{'\n'})
}

// Events returns the channel on which received hooks are delivered. It is
// closed once Close has unblocked the accept loop and the loop has returned.
func (l *Listener) Events() <-chan Hook { return l.events }

// Path returns the Unix socket path this listener is bound to.
func (l *Listener) Path() string { return l.path }

// Close closes the underlying net.Listener, waits for the accept loop to
// finish, closes the events channel, and removes the socket file. It is safe
// to call multiple times.
func (l *Listener) Close() error {
	l.closeOnc.Do(func() {
		err := l.ln.Close()
		<-l.done // wait for the accept loop to exit before closing events
		close(l.events)
		if rmErr := os.Remove(l.path); rmErr != nil && !os.IsNotExist(rmErr) {
			if err == nil {
				err = rmErr
			}
		}
		l.closeErr = err
	})
	return l.closeErr
}
