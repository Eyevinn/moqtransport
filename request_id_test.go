package moqtransport

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A client generates even Request IDs from 0, a server odd ones from 1
// (Section 10.1), so the perspective alone fixes the sequence.
func TestRequestIDGenerator(t *testing.T) {
	client := newRequestIDGenerator(PerspectiveClient)
	assert.Equal(t, uint64(0), client.nextID())
	assert.Equal(t, uint64(2), client.nextID())
	assert.Equal(t, uint64(4), client.nextID())

	server := newRequestIDGenerator(PerspectiveServer)
	assert.Equal(t, uint64(1), server.nextID())
	assert.Equal(t, uint64(3), server.nextID())
}

// An ID with the wrong parity for its sender ends the session: a server's peer
// is a client, and clients use even IDs.
func TestPeerRequestIDParity(t *testing.T) {
	fromClient := newPeerRequestIDs(PerspectiveServer)
	require.NoError(t, fromClient.use(0))
	require.NoError(t, fromClient.use(2))
	assert.ErrorIs(t, fromClient.use(3), errInvalidRequestIDParity)

	fromServer := newPeerRequestIDs(PerspectiveClient)
	require.NoError(t, fromServer.use(1))
	assert.ErrorIs(t, fromServer.use(2), errInvalidRequestIDParity)
}

func TestPeerRequestIDDuplicates(t *testing.T) {
	ids := newPeerRequestIDs(PerspectiveServer)
	require.NoError(t, ids.use(0))
	assert.ErrorIs(t, ids.use(0), errDuplicateRequestID)

	require.NoError(t, ids.use(2))
	assert.ErrorIs(t, ids.use(2), errDuplicateRequestID)
	assert.ErrorIs(t, ids.use(0), errDuplicateRequestID, "still remembered after the run advanced")
}

// Requests arrive on their own streams and are dispatched concurrently, so an
// ID can turn up before a smaller one. That is not a duplicate, and the smaller
// one must still be accepted when it lands.
func TestPeerRequestIDOutOfOrder(t *testing.T) {
	ids := newPeerRequestIDs(PerspectiveServer)

	require.NoError(t, ids.use(6))
	require.NoError(t, ids.use(2))
	assert.ErrorIs(t, ids.use(6), errDuplicateRequestID)

	require.NoError(t, ids.use(0))
	require.NoError(t, ids.use(4))
	assert.ErrorIs(t, ids.use(4), errDuplicateRequestID)

	// With the run now contiguous through 6, nothing is held back.
	assert.Empty(t, ids.ahead, "no holes left to remember")
	assert.Equal(t, uint64(8), ids.lowest)
}

// The memory stays bounded: a peer issuing IDs in order leaves nothing behind,
// however many it sends.
func TestPeerRequestIDMemoryIsBounded(t *testing.T) {
	ids := newPeerRequestIDs(PerspectiveServer)
	for id := uint64(0); id < 10_000; id += 2 {
		require.NoError(t, ids.use(id))
	}
	assert.Empty(t, ids.ahead)
	assert.Equal(t, uint64(10_000), ids.lowest)
}

// A hole that never fills -- a request the peer cancelled before its message
// arrived, or an ID it skipped -- must not make the tracker hold every later
// ID. The IDs held stay within the window however many arrive (#173 in
// Eyevinn/moqlivemock: ID 0 withheld, millions of later IDs sent).
func TestPeerRequestIDPermanentHoleIsBounded(t *testing.T) {
	ids := newPeerRequestIDs(PerspectiveServer)
	for id := uint64(2); id < 4*2*peerIDWindow; id += 2 {
		require.NoError(t, ids.use(id))
		require.LessOrEqual(t, len(ids.ahead), peerIDWindow)
	}
	// The run caught up once the hole at 0 fell out of the window.
	assert.Empty(t, ids.ahead)
	assert.Equal(t, uint64(4*2*peerIDWindow), ids.lowest)
	// The given-up ID counts as used.
	assert.ErrorIs(t, ids.use(0), errDuplicateRequestID)
}

// IDs within the window keep the exact behaviour: a late arrival is accepted
// and a repeat is a duplicate.
func TestPeerRequestIDLateWithinWindow(t *testing.T) {
	ids := newPeerRequestIDs(PerspectiveServer)
	last := uint64(2 * (peerIDWindow - 1))
	for id := uint64(2); id <= last; id += 2 {
		require.NoError(t, ids.use(id))
	}
	assert.Len(t, ids.ahead, peerIDWindow-1)
	require.NoError(t, ids.use(0), "the oldest hole is still within the window")
	assert.Empty(t, ids.ahead)
	assert.ErrorIs(t, ids.use(last), errDuplicateRequestID)
}

// One ID far ahead moves the window in one step, forgetting the IDs held
// below it, and the peer can go on from there.
func TestPeerRequestIDFarJump(t *testing.T) {
	ids := newPeerRequestIDs(PerspectiveServer)
	require.NoError(t, ids.use(4))
	require.NoError(t, ids.use(8))

	far := uint64(1) << 40
	require.NoError(t, ids.use(far))
	assert.Len(t, ids.ahead, 1)
	assert.Equal(t, far-2*(peerIDWindow-1), ids.lowest)

	assert.ErrorIs(t, ids.use(8), errDuplicateRequestID, "below the window counts as used")
	assert.ErrorIs(t, ids.use(far), errDuplicateRequestID)
	require.NoError(t, ids.use(far-2), "a hole inside the window is still open")
	require.NoError(t, ids.use(far+2))
}
