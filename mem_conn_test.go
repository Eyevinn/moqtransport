package moqtransport

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"sync/atomic"
)

// An in-process Connection pair, so a whole session can be exercised without a
// QUIC stack. It is deliberately thin: streams are the blocking byte pipes
// already used elsewhere in these tests, and the only QUIC semantics modelled
// are the ones the protocol depends on -- FIN, RESET_STREAM and STOP_SENDING
// each ending a direction with a distinguishable error.
//
// One place it is not faithful: resetting a stream still lets the peer drain
// what was already buffered, where QUIC would discard it. Nothing here depends
// on the difference.

type memConn struct {
	perspective Perspective
	alpn        string

	peer *memConn

	incomingBidi chan Stream
	incomingUni  chan ReceiveStream
	datagrams    chan []byte

	nextStreamID atomic.Uint64

	mu     sync.Mutex
	pipes  []*streamPipe
	closed bool
	cause  error

	ctx    context.Context
	cancel context.CancelFunc
}

// newMemConnPair returns two connected endpoints, both reporting alpn as the
// negotiated protocol.
func newMemConnPair(alpn string) (client, server *memConn) {
	client = newMemConn(PerspectiveClient, alpn)
	server = newMemConn(PerspectiveServer, alpn)
	client.peer, server.peer = server, client
	return client, server
}

func newMemConn(p Perspective, alpn string) *memConn {
	ctx, cancel := context.WithCancel(context.Background())
	c := &memConn{
		perspective:  p,
		alpn:         alpn,
		incomingBidi: make(chan Stream, 64),
		incomingUni:  make(chan ReceiveStream, 64),
		datagrams:    make(chan []byte, 64),
		ctx:          ctx,
		cancel:       cancel,
	}
	// Client streams are even, server streams odd, as in QUIC.
	if p == PerspectiveServer {
		c.nextStreamID.Store(1)
	}
	return c
}

// track records a pipe this endpoint reads from, so closing the connection
// unblocks every reader rather than leaving goroutines parked.
func (c *memConn) track(p *streamPipe) *streamPipe {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		p.close(c.cause)
		return p
	}
	c.pipes = append(c.pipes, p)
	return p
}

func (c *memConn) streamID() uint64 { return c.nextStreamID.Add(2) - 2 }

func (c *memConn) AcceptStream(ctx context.Context) (Stream, error) {
	select {
	case s := <-c.incomingBidi:
		return s, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-c.ctx.Done():
		return nil, c.closedErr()
	}
}

func (c *memConn) AcceptUniStream(ctx context.Context) (ReceiveStream, error) {
	select {
	case s := <-c.incomingUni:
		return s, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-c.ctx.Done():
		return nil, c.closedErr()
	}
}

func (c *memConn) OpenStream() (Stream, error) {
	return c.OpenStreamSync(context.Background())
}

func (c *memConn) OpenStreamSync(ctx context.Context) (Stream, error) {
	if err := c.alive(); err != nil {
		return nil, err
	}
	id := c.streamID()
	toPeer := c.track(newStreamPipe())
	toUs := c.peer.track(newStreamPipe())

	mine := &memStream{id: id, send: toPeer, recv: toUs}
	theirs := &memStream{id: id, send: toUs, recv: toPeer}
	select {
	case c.peer.incomingBidi <- theirs:
		return mine, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (c *memConn) OpenUniStream() (SendStream, error) {
	return c.OpenUniStreamSync(context.Background())
}

func (c *memConn) OpenUniStreamSync(ctx context.Context) (SendStream, error) {
	if err := c.alive(); err != nil {
		return nil, err
	}
	id := c.streamID()
	pipe := c.peer.track(newStreamPipe())

	mine := &memStream{id: id, send: pipe}
	theirs := &memStream{id: id, recv: pipe}
	select {
	case c.peer.incomingUni <- theirs:
		return mine, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (c *memConn) SendDatagram(b []byte) error {
	if err := c.alive(); err != nil {
		return err
	}
	select {
	case c.peer.datagrams <- append([]byte(nil), b...):
		return nil
	default:
		// A datagram that does not fit is dropped without notice, which is
		// exactly what the draft says happens to an oversized one.
		return nil
	}
}

func (c *memConn) ReceiveDatagram(ctx context.Context) ([]byte, error) {
	select {
	case b := <-c.datagrams:
		return b, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-c.ctx.Done():
		return nil, c.closedErr()
	}
}

func (c *memConn) CloseWithError(code uint64, reason string) error {
	err := fmt.Errorf("connection closed: %d: %s", code, reason)

	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil
	}
	c.closed = true
	c.cause = err
	pipes := c.pipes
	c.mu.Unlock()

	for _, p := range pipes {
		p.close(err)
	}
	c.cancel()

	// The peer of a closed connection sees its own reads fail too.
	if c.peer != nil {
		c.peer.remoteClosed(err)
	}
	return nil
}

func (c *memConn) remoteClosed(err error) {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return
	}
	c.closed = true
	c.cause = err
	pipes := c.pipes
	c.mu.Unlock()

	for _, p := range pipes {
		p.close(err)
	}
	c.cancel()
}

func (c *memConn) alive() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return c.cause
	}
	return nil
}

func (c *memConn) closedErr() error {
	if err := c.alive(); err != nil {
		return err
	}
	return errors.New("connection closed")
}

func (c *memConn) Context() context.Context { return c.ctx }
func (c *memConn) Protocol() Protocol       { return ProtocolQUIC }
func (c *memConn) Perspective() Perspective { return c.perspective }
func (c *memConn) NegotiatedALPN() string   { return c.alpn }

// memStream is one end of an in-process stream. A unidirectional stream has
// only one of the two pipes.
type memStream struct {
	id   uint64
	send *streamPipe
	recv *streamPipe
}

func (s *memStream) Read(b []byte) (int, error) {
	if s.recv == nil {
		return 0, errors.New("stream is send-only")
	}
	return s.recv.Read(b)
}

func (s *memStream) Write(b []byte) (int, error) {
	if s.send == nil {
		return 0, errors.New("stream is receive-only")
	}
	return s.send.Write(b)
}

// Close is a FIN: the peer drains what is buffered and then sees a clean end.
func (s *memStream) Close() error {
	if s.send != nil {
		s.send.close(io.EOF)
	}
	return nil
}

// Reset ends our send direction abruptly, so the peer's read fails rather than
// ending cleanly.
func (s *memStream) Reset(code uint32) {
	if s.send != nil {
		s.send.close(&memStreamError{code: code, sending: true})
	}
}

// Stop is STOP_SENDING: our own reads fail, and so do the peer's writes, since
// the two share the pipe.
func (s *memStream) Stop(code uint32) {
	if s.recv != nil {
		s.recv.close(&memStreamError{code: code})
	}
}

func (s *memStream) StreamID() uint64 { return s.id }

type memStreamError struct {
	code    uint32
	sending bool
}

func (e *memStreamError) Error() string {
	if e.sending {
		return fmt.Sprintf("stream reset: %v", StreamErrorCode(e.code))
	}
	return fmt.Sprintf("stream stopped: %v", StreamErrorCode(e.code))
}
