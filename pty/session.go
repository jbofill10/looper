package pty

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"

	creackpty "github.com/creack/pty"
)

// defaultScrollbackBytes is the ring buffer size used when Config.ScrollbackBytes is 0.
const defaultScrollbackBytes = 64 * 1024 // 64 KiB

// Config configures a Session.
type Config struct {
	// Argv is the command and its arguments; Argv[0] is resolved via exec.Command.
	Argv []string
	// Env is the child process's environment. If nil, the child inherits the
	// current process's environment (exec.Command's default).
	Env []string
	// Dir is the child process's working directory.
	Dir string
	// ScrollbackBytes caps the scrollback ring buffer size. 0 means
	// defaultScrollbackBytes.
	ScrollbackBytes int
}

// Session runs a command in a looper-owned pseudoterminal. A background
// reader goroutine is the sole writer to both the scrollback ring buffer and
// the swappable live writer; both are mutex-guarded for readers/swappers.
type Session struct {
	cmd  *exec.Cmd
	ptmx *os.File

	mu   sync.Mutex // guards scrollback and live
	ring *ringBuffer
	live io.Writer

	readerDone chan struct{} // closed once the reader goroutine returns

	closeOnce sync.Once
	closeErr  error
}

// Start starts argv in a new pseudoterminal per cfg and begins copying its
// output into the scrollback ring buffer (and, once attached, a live
// writer).
func Start(cfg Config) (*Session, error) {
	if len(cfg.Argv) == 0 {
		return nil, fmt.Errorf("pty: start: empty argv")
	}
	cmd := exec.Command(cfg.Argv[0], cfg.Argv[1:]...)
	cmd.Env = cfg.Env
	cmd.Dir = cfg.Dir

	ptmx, err := creackpty.Start(cmd)
	if err != nil {
		return nil, fmt.Errorf("pty: start %q: %w", cfg.Argv[0], err)
	}

	size := cfg.ScrollbackBytes
	if size == 0 {
		size = defaultScrollbackBytes
	}

	s := &Session{
		cmd:        cmd,
		ptmx:       ptmx,
		ring:       newRingBuffer(size),
		readerDone: make(chan struct{}),
	}
	go s.readLoop()
	return s, nil
}

// readLoop copies ptmx output into the ring buffer and (if set) the live
// writer. It is the only goroutine that ever writes to s.ring or reads
// s.live, so it takes s.mu only to keep those operations atomic with
// concurrent Scrollback/setLive callers. It returns when the ptmx read end
// hits EOF (the child exited and its pty slave closed) or another read
// error occurs.
func (s *Session) readLoop() {
	defer close(s.readerDone)
	buf := make([]byte, 4096)
	for {
		n, err := s.ptmx.Read(buf)
		if n > 0 {
			s.mu.Lock()
			s.ring.write(buf[:n])
			live := s.live
			s.mu.Unlock()
			if live != nil {
				_, _ = live.Write(buf[:n])
			}
		}
		if err != nil {
			return
		}
	}
}

// Write writes p to the session's pty (i.e. sends input to the child).
func (s *Session) Write(p []byte) (int, error) {
	return s.ptmx.Write(p)
}

// Scrollback returns a snapshot copy of the captured scrollback.
func (s *Session) Scrollback() []byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.ring.snapshot()
}

// setLive swaps the live writer that the reader goroutine tees output to.
// Pass nil to disable live streaming.
func (s *Session) setLive(w io.Writer) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.live = w
}

// PipeTo taps the session's output for a remote attach: it writes the
// current Scrollback() to w, then makes w the live writer that the reader
// goroutine tees subsequent output to. If a live writer is already set (e.g.
// a local Attach or an earlier PipeTo), PipeTo replaces it — only one live
// writer is supported at a time. The returned stop func clears the live
// writer; it is idempotent and safe to call multiple times, but note it
// unconditionally clears whatever live writer is set when called, even if a
// later PipeTo/Attach has since replaced it.
func (s *Session) PipeTo(w io.Writer) (stop func()) {
	// A write error here doesn't prevent tapping live output; a caller whose
	// w stays broken will simply see no output at all, which is its own
	// signal.
	_, _ = w.Write(s.Scrollback())
	s.setLive(w)
	return func() {
		s.setLive(nil)
	}
}

// Resize sets the pty's window size.
func (s *Session) Resize(rows, cols uint16) error {
	if err := creackpty.Setsize(s.ptmx, &creackpty.Winsize{Rows: rows, Cols: cols}); err != nil {
		return fmt.Errorf("pty: resize: %w", err)
	}
	return nil
}

// Wait waits for the child process to exit and for the reader goroutine to
// finish draining its output.
func (s *Session) Wait() error {
	err := s.cmd.Wait()
	<-s.readerDone
	if err != nil {
		return fmt.Errorf("pty: wait: %w", err)
	}
	return nil
}

// RunAttached bridges in/out to s via Attach and waits for s to finish,
// handling the one-shot local-attach case: the caller has no way to
// reattach later, so if the human detaches (Ctrl-\ d) while the child is
// still running, that is treated as abandoning the session rather than as a
// request to leave it running in the background — s is killed immediately
// instead of being waited on indefinitely. If the child exits on its own
// first, Attach's own s.readerDone branch returns promptly and RunAttached
// simply reports the exit error.
func (s *Session) RunAttached(in, out *os.File) error {
	waitCh := make(chan error, 1)
	go func() { waitCh <- s.Wait() }()

	attachDone := make(chan struct{})
	go func() {
		defer close(attachDone)
		_ = s.Attach(in, out)
	}()

	var runErr error
	select {
	case runErr = <-waitCh:
		<-attachDone
	case <-attachDone:
		_ = s.Close()
		runErr = <-waitCh
	}
	_ = s.Close()
	return runErr
}

// Close closes the pty and kills the child process if it is still running.
// It is idempotent and safe to call multiple times.
func (s *Session) Close() error {
	s.closeOnce.Do(func() {
		if s.cmd.Process != nil {
			_ = s.cmd.Process.Kill()
		}
		s.closeErr = s.ptmx.Close()
	})
	return s.closeErr
}
