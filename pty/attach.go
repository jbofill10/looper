package pty

import (
	"os"

	"golang.org/x/term"
)

// Attach bridges a real terminal (in for input, out for output) to the
// session in tmux-style raw mode: in is put into raw mode (if it is a
// terminal), the session's scrollback is replayed to out, and out then
// becomes the session's live writer for the duration of the attach. Input
// read from in is scanned for the Ctrl-b d detach escape; everything else is
// forwarded to the session. Attach returns nil when the human detaches or
// when the session exits.
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

	detachCh := make(chan struct{})
	go func() {
		var scanner DetachScanner
		buf := make([]byte, 4096)
		for {
			n, err := in.Read(buf)
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

	select {
	case <-detachCh:
		return nil
	case <-s.readerDone:
		return nil
	}
}
