package moqtransport

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPendingStreamsHoldsUntilRelease(t *testing.T) {
	b := newPendingStreams[int](4)

	for i := range 3 {
		held, err := b.hold(i)
		require.NoError(t, err)
		assert.True(t, held)
	}
	assert.Equal(t, 3, b.len())

	// Arrival order is preserved: it is the order the peer opened them in.
	assert.Equal(t, []int{0, 1, 2}, b.release())
	assert.Equal(t, 0, b.len())
}

// TestPendingStreamsAfterReleaseHandsBack pins the property that removes the
// race from the accept loop: once released, hold never buffers again, so the
// caller has a single answer for what to do with a stream.
func TestPendingStreamsAfterReleaseHandsBack(t *testing.T) {
	b := newPendingStreams[int](4)
	b.release()

	held, err := b.hold(1)
	require.NoError(t, err)
	assert.False(t, held)
	assert.Equal(t, 0, b.len())
}

func TestPendingStreamsReleaseIsIdempotent(t *testing.T) {
	b := newPendingStreams[int](4)
	_, err := b.hold(1)
	require.NoError(t, err)

	assert.Equal(t, []int{1}, b.release())
	assert.Empty(t, b.release())
}

// TestPendingStreamsBound is what makes "SHOULD buffer" safe against a peer
// that opens streams and never sends SETUP.
func TestPendingStreamsBound(t *testing.T) {
	b := newPendingStreams[int](2)
	for i := range 2 {
		held, err := b.hold(i)
		require.NoError(t, err)
		assert.True(t, held)
	}

	held, err := b.hold(99)
	assert.ErrorIs(t, err, errTooManyPendingStreams)
	assert.False(t, held)
	assert.Equal(t, 2, b.len())
}

func TestPendingStreamsDefaultLimit(t *testing.T) {
	b := newPendingStreams[int](0)
	assert.Equal(t, defaultMaxPendingStreams, b.limit)
}
