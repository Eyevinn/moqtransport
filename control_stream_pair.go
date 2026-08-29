package moqtransport

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/Eyevinn/moqtransport/internal/wire2"
)

// controlStreamPair owns the two unidirectional streams that carry control
// messages from draft-18 onwards: one this endpoint opens and writes its SETUP
// on, and one the peer opens carrying theirs.
//
// draft-ietf-moq-transport-18 Section 3.3 makes the handshake symmetric. There
// is no client/server ordering and no request/response: each side opens its
// own stream and sends SETUP as soon as it can, so with 0-RTT either side may
// speak first. Nothing about the exchange is a negotiation -- the version came
// from the ALPN before a byte of MOQT was written -- so a peer's SETUP only
// tells us the options it chose.
//
// The leading varint of the control stream is 0x2F00, which is simultaneously
// the unidirectional stream type from Table 3 and SETUP's own message type.
// Opening the stream and sending SETUP are therefore the same act.
type controlStreamPair struct {
	// local is our half. The draft forbids closing a control stream for the
	// life of the session: doing so is a PROTOCOL_VIOLATION.
	local SendStream

	remote ReceiveStream
	parser *wire2.ControlMessageParser

	// ready is closed once the peer's SETUP has been parsed. Everything that
	// depends on knowing the peer's options waits on it, and closing a channel
	// rather than setting a flag means the wait is race-free without a lock on
	// the read path.
	ready     chan struct{}
	readyOnce sync.Once

	mu sync.Mutex
	// remoteAdopted guards against a second control stream. It is separate
	// from remote because the stream itself may legitimately be nil when the
	// caller only has the parser.
	remoteAdopted bool
	peerSetup     *wire2.Setup
	remoteErr     error
}

func newControlStreamPair() *controlStreamPair {
	return &controlStreamPair{ready: make(chan struct{})}
}

// open opens our control stream and sends setup on it.
//
// It is deliberately not called from a constructor: the caller decides when the
// handshake starts, so a session can be fully assembled -- accept loops running
// and able to buffer whatever arrives first -- before anything is on the wire.
func (p *controlStreamPair) open(ctx context.Context, conn Connection, setup *wire2.Setup) error {
	stream, err := conn.OpenUniStreamSync(ctx)
	if err != nil {
		return fmt.Errorf("opening control stream: %w", err)
	}

	buf, err := wire2.AppendControlMessage(nil, setup)
	if err != nil {
		return fmt.Errorf("encoding SETUP: %w", err)
	}
	if _, err := stream.Write(buf); err != nil {
		return fmt.Errorf("sending SETUP: %w", err)
	}

	p.local = stream
	return nil
}

// write sends a control message on our half of the pair. Only SETUP and GOAWAY
// belong here; everything else travels on its own request stream.
func (p *controlStreamPair) write(msg wire2.MessageV18) error {
	if p.local == nil {
		return errControlStreamNotOpen
	}
	buf, err := wire2.AppendControlMessage(nil, msg)
	if err != nil {
		return err
	}
	_, err = p.local.Write(buf)
	return err
}

// adoptRemote takes a unidirectional stream the peer opened whose type varint
// has already been read as the control stream type, reads the SETUP that
// follows, and marks the pair ready.
//
// A second control stream is a protocol violation: the pair is a pair.
func (p *controlStreamPair) adoptRemote(stream ReceiveStream, parser *wire2.ControlMessageParser) error {
	p.mu.Lock()
	if p.remoteAdopted {
		p.mu.Unlock()
		return errDuplicateControlStream
	}
	p.remoteAdopted = true
	p.remote = stream
	p.parser = parser
	p.mu.Unlock()

	msg, err := parser.ParseBody(wire2.ControlMessageType(wire2.StreamTypeSetup))
	if err != nil {
		p.failRemote(fmt.Errorf("reading peer SETUP: %w", err))
		return err
	}
	setup, ok := msg.(*wire2.Setup)
	if !ok {
		err := fmt.Errorf("control stream began with %v, not SETUP", msg.Type())
		p.failRemote(err)
		return err
	}

	p.mu.Lock()
	p.peerSetup = setup
	p.mu.Unlock()
	p.readyOnce.Do(func() { close(p.ready) })
	return nil
}

// read yields the control messages that follow SETUP. Only GOAWAY is expected;
// the parser rejects anything else that is not valid on a control stream.
func (p *controlStreamPair) read() (wire2.ControlMessage, error) {
	p.mu.Lock()
	parser := p.parser
	p.mu.Unlock()
	if parser == nil {
		return nil, errControlStreamNotOpen
	}
	return parser.Parse()
}

// failRemote records why the peer's control stream ended and releases anything
// waiting on the handshake, so a peer that opens a control stream and then
// sends garbage fails the wait instead of hanging until the setup deadline.
func (p *controlStreamPair) failRemote(err error) {
	p.mu.Lock()
	if p.remoteErr == nil {
		p.remoteErr = err
	}
	p.mu.Unlock()
	p.readyOnce.Do(func() { close(p.ready) })
}

// awaitPeerSetup blocks until the peer's SETUP has been read, the pair has
// failed, or ctx is done.
func (p *controlStreamPair) awaitPeerSetup(ctx context.Context) (*wire2.Setup, error) {
	select {
	case <-ctx.Done():
		return nil, context.Cause(ctx)
	case <-p.ready:
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.remoteErr != nil {
		return nil, p.remoteErr
	}
	return p.peerSetup, nil
}

var (
	errControlStreamNotOpen   = errors.New("control stream is not open")
	errDuplicateControlStream = errors.New("peer opened a second control stream")
)
