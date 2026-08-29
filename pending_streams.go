package moqtransport

import (
	"errors"
	"sync"
)

// defaultMaxPendingStreams bounds how many streams are held while the control
// streams are still being established.
//
// draft-ietf-moq-transport-18 Section 3.3 says data streams and request
// streams that arrive before setup completes SHOULD be buffered, and MAY be
// reset instead if the implementation does not want to buffer. A bound is what
// makes "SHOULD buffer" safe: without one, a peer that opens streams and never
// sends SETUP holds memory in proportion to its patience.
const defaultMaxPendingStreams = 64

// pendingStreams holds streams that arrived before the session was ready.
//
// Once released it holds nothing further: hold reports that the caller should
// handle the stream itself, so the accept loop needs no separate "are we ready
// yet" check that could race with the release.
type pendingStreams[T any] struct {
	mu       sync.Mutex
	limit    int
	released bool
	streams  []T
}

func newPendingStreams[T any](limit int) *pendingStreams[T] {
	if limit <= 0 {
		limit = defaultMaxPendingStreams
	}
	return &pendingStreams[T]{limit: limit}
}

// hold buffers s until release is called. It reports whether s was buffered; a
// false result with no error means setup has completed and the caller should
// handle s now. An error means the buffer is full, and the caller may reset the
// stream, which the draft explicitly permits.
func (b *pendingStreams[T]) hold(s T) (bool, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.released {
		return false, nil
	}
	if len(b.streams) >= b.limit {
		return false, errTooManyPendingStreams
	}
	b.streams = append(b.streams, s)
	return true, nil
}

// release returns everything held, in arrival order, and stops buffering.
// Calling it again returns nothing.
func (b *pendingStreams[T]) release() []T {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.released = true
	streams := b.streams
	b.streams = nil
	return streams
}

// len reports how many streams are currently held.
func (b *pendingStreams[T]) len() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.streams)
}

var errTooManyPendingStreams = errors.New("too many streams buffered before setup completed")
