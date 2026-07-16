package pty

import (
	"os"

	"github.com/muesli/cancelreader"
	"golang.org/x/term"
)

// Attach bridges a real terminal (in for input, out for output) to the
// session in tmux-style raw mode: in is put into raw mode (if it is a
// terminal), the session's scrollback is replayed to out, and out then
// becomes the session's live writer for the duration of the attach. Input
// read from in is scanned for the Ctrl-b d detach escape; everything else is
// forwarded to the session. Attach returns nil when the human detaches or
// when the session exits.
//
// Attach always joins its internal stdin-reading goroutine before returning,
// unblocking a pending Read via cancelreader.Cancel rather than abandoning
// the goroutine. Without this, a caller that reclaims in for its own use
// immediately after Attach returns (e.g. a TUI reinitializing its own stdin
// reader after ReleaseTerminal/RestoreTerminal) can end up racing a leaked
// goroutine still blocked in a Read on the same fd, silently stealing
// keystrokes. A plain os.File.SetReadDeadline-based approach can't do this
// reliably here: term.MakeRaw/term.IsTerminal above call in.Fd(), which per
// the os package's documented behavior permanently disables that File's
// SetDeadline support — hence cancelreader, which cancels via epoll/kqueue
// on the raw fd instead and isn't affected by that.
func (s *Session) Attach(in, out *os.File) error {
	if term.IsTerminal(int(in.Fd())) {
		oldState, err := term.MakeRaw(int(in.Fd()))
		if err == nil {
			defer term.Restore(int(in.Fd()), oldState)
		}
	}

	if _, err := out.Write(s.Scrollback()); err != nil {
		return err
	}
	s.setLive(out)
	defer s.setLive(nil)

	if cols, rows, err := term.GetSize(int(out.Fd())); err == nil {
		_ = s.Resize(uint16(rows), uint16(cols))
	}

	cr, err := cancelreader.NewReader(in)
	if err != nil {
		// Some fd types (e.g. /dev/null, common as stdin in a headless test
		// or CI environment) can't be registered with epoll/kqueue, and
		// NewReader errors rather than returning a working reader. Fall back
		// to reading in directly: Attach can no longer guarantee it joins the
		// reader goroutine on return (the same limitation this function had
		// before cancelreader was introduced), but it must not treat this as
		// a fatal error — that would abort the whole session before the
		// child process gets a chance to run.
		cr = uncancelableReader{in}
	}
	defer cr.Close()

	readerDone := make(chan struct{})
	detachCh := make(chan struct{})
	go func() {
		defer close(readerDone)
		var scanner DetachScanner
		buf := make([]byte, 4096)
		for {
			n, err := cr.Read(buf)
			if n > 0 {
				passthrough, detached := scanner.Scan(buf[:n])
				if len(passthrough) > 0 {
					_, _ = s.Write(passthrough)
				}
				if detached {
					close(detachCh)
					return
				}
			}
			if err != nil {
				return
			}
		}
	}()
	defer func() {
		cr.Cancel()
		<-readerDone
	}()

	select {
	case <-detachCh:
		return nil
	case <-s.readerDone:
		return nil
	}
}

// uncancelableReader adapts an *os.File to cancelreader.CancelReader for the
// fallback path in Attach: Cancel is a permanent no-op (there is no way to
// interrupt an in-flight Read), and Close does not close the underlying
// file, matching cancelreader's own Close semantics (which never closes the
// wrapped file either).
type uncancelableReader struct {
	*os.File
}

func (uncancelableReader) Cancel() bool { return false }
func (uncancelableReader) Close() error { return nil }
