package pty

// ringBuffer is a fixed-capacity byte buffer that retains only the most
// recently written bytes once it exceeds its capacity. It is not safe for
// concurrent use; callers must serialize access (Session does so via its
// mutex).
type ringBuffer struct {
	cap int
	buf []byte
}

// newRingBuffer returns a ringBuffer retaining at most capBytes of the most
// recently written data.
func newRingBuffer(capBytes int) *ringBuffer {
	return &ringBuffer{cap: capBytes}
}

// write appends p, discarding the oldest bytes if the result would exceed
// the buffer's capacity.
func (r *ringBuffer) write(p []byte) {
	r.buf = append(r.buf, p...)
	if len(r.buf) > r.cap {
		drop := len(r.buf) - r.cap
		r.buf = append(r.buf[:0:0], r.buf[drop:]...)
	}
}

// snapshot returns a copy of the currently retained bytes.
func (r *ringBuffer) snapshot() []byte {
	out := make([]byte, len(r.buf))
	copy(out, r.buf)
	return out
}
