// Package pty runs a command in a looper-owned pseudoterminal, capturing
// scrollback and supporting resize, input, and a tmux-style raw-mode
// attach/detach bridge for the human operator.
package pty

// ctrlBackslash is the byte that begins the detach escape sequence
// (Ctrl-\). Ctrl-b was considered but rejected: it collides with tmux's
// default prefix, making looper impossible to detach from cleanly when
// attached inside a tmux pane.
const ctrlBackslash = 0x1c

// detachEscapeByte is the byte that, following ctrlBackslash, triggers a detach.
const detachEscapeByte = 'd'

// DetachScanner is a pure state machine recognizing the "Ctrl-\ d" detach
// escape sequence in a stream of input bytes. It is not safe for concurrent
// use; callers must serialize calls to Scan. The zero value is ready to use.
type DetachScanner struct {
	armed bool // a lone Ctrl-\ has been seen and awaits resolution
}

// Scan walks in byte by byte, recognizing the detach escape. Bytes that are
// not part of the escape (or that resolve a false alarm) are appended to
// passthrough. If the escape completes, detached is true, the triggering 'd'
// is dropped, and any remaining bytes in in are not consumed. Armed state
// persists across calls, so an escape split across two Scan calls (a
// dangling Ctrl-\ at the end of one call, resolved at the start of the
// next) is still recognized.
func (d *DetachScanner) Scan(in []byte) (passthrough []byte, detached bool) {
	for _, b := range in {
		if d.armed {
			d.armed = false
			if b == detachEscapeByte {
				return passthrough, true
			}
			passthrough = append(passthrough, ctrlBackslash, b)
			continue
		}
		if b == ctrlBackslash {
			d.armed = true
			continue
		}
		passthrough = append(passthrough, b)
	}
	return passthrough, false
}
