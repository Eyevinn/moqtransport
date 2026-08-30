package moqtransport

import "sync"

// requestIDGenerator hands out this endpoint's Request IDs.
//
// draft-ietf-moq-transport-18 Section 10.1: a client generates even IDs from
// 0, a server odd IDs from 1, and each endpoint steps by 2. So the perspective
// alone fixes the sequence.
//
// draft-17 deleted MAX_REQUEST_ID and REQUESTS_BLOCKED, so there is no ceiling
// to track any more: a peer that cannot accept another request refuses the
// bidirectional stream through QUIC's own flow control instead.
type requestIDGenerator struct {
	lock sync.Mutex
	next uint64
}

// newRequestIDGenerator returns the generator for p's side of the session.
func newRequestIDGenerator(p Perspective) *requestIDGenerator {
	first := uint64(0)
	if p == PerspectiveServer {
		first = 1
	}
	return &requestIDGenerator{next: first}
}

// nextID returns the next Request ID for a request this endpoint originates.
func (g *requestIDGenerator) nextID() uint64 {
	g.lock.Lock()
	defer g.lock.Unlock()
	id := g.next
	g.next += 2
	return id
}

// peerRequestIDs checks the Request IDs arriving from the peer.
//
// Section 10.1 makes two things a session-ending INVALID_REQUEST_ID: an ID
// whose least significant bit is wrong for its sender, and a duplicate. The
// first is a parity test; the second needs memory.
//
// The memory is bounded rather than a set of everything seen. The peer issues
// its IDs in order, stepping by 2, so the used set is always a contiguous run
// with at most a few holes where requests are still in flight and arriving out
// of order. Tracking the low water mark plus those holes is exact and stays
// small, where a set of every ID ever seen would grow without limit on a
// long-lived session.
type peerRequestIDs struct {
	lock sync.Mutex

	// lowest is the smallest ID the peer has not used yet. Everything below it
	// has been seen.
	lowest uint64

	// ahead holds IDs seen out of order, above lowest.
	ahead map[uint64]struct{}
}

// newPeerRequestIDs returns the tracker for the peer of an endpoint with
// perspective p.
func newPeerRequestIDs(p Perspective) *peerRequestIDs {
	// Our peer's first ID is the one we do not use: a server's peer is a
	// client, which starts at 0.
	first := uint64(1)
	if p == PerspectiveServer {
		first = 0
	}
	return &peerRequestIDs{lowest: first, ahead: map[uint64]struct{}{}}
}

// use records an incoming Request ID, reporting whether it is acceptable.
func (t *peerRequestIDs) use(id uint64) error {
	t.lock.Lock()
	defer t.lock.Unlock()

	if id%2 != t.lowest%2 {
		return errInvalidRequestIDParity
	}
	if id < t.lowest {
		return errDuplicateRequestID
	}
	if _, seen := t.ahead[id]; seen {
		return errDuplicateRequestID
	}

	t.ahead[id] = struct{}{}
	// Absorb the run that is now contiguous, so only genuine holes are kept.
	for {
		if _, ok := t.ahead[t.lowest]; !ok {
			return nil
		}
		delete(t.ahead, t.lowest)
		t.lowest += 2
	}
}

var (
	errInvalidRequestIDParity = ProtocolError{
		code:    SessionErrorInvalidRequestID,
		message: "request ID has the wrong parity for its sender",
	}

	errDuplicateRequestID = ProtocolError{
		code:    SessionErrorInvalidRequestID,
		message: "request ID has already been used",
	}
)
