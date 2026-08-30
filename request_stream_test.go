package moqtransport

import (
	"bytes"
	"context"
	"errors"
	"io"
	"slices"
	"sync"
	"testing"

	"github.com/Eyevinn/moqtransport/internal/wire2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

// streamPipe is the peer's send side: an unbounded buffer whose Read blocks
// until there is something to read or the stream ends.
//
// io.Pipe cannot serve here. Its Write blocks until a reader arrives, so
// "queue a message, then read it" -- which is exactly how a test drives a
// request stream -- would deadlock.
type streamPipe struct {
	mu   sync.Mutex
	cond *sync.Cond
	buf  bytes.Buffer
	err  error
}

func newStreamPipe() *streamPipe {
	p := &streamPipe{}
	p.cond = sync.NewCond(&p.mu)
	return p
}

func (p *streamPipe) Write(b []byte) (int, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.err != nil {
		return 0, p.err
	}
	n, err := p.buf.Write(b)
	p.cond.Broadcast()
	return n, err
}

func (p *streamPipe) Read(b []byte) (int, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for p.buf.Len() == 0 && p.err == nil {
		p.cond.Wait()
	}
	if p.buf.Len() > 0 {
		return p.buf.Read(b)
	}
	return 0, p.err
}

// close ends the stream. Readers drain whatever is still buffered and then
// see err, so a FIN sent right after a message still delivers that message.
func (p *streamPipe) close(err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.err == nil {
		p.err = err
	}
	p.cond.Broadcast()
}

// requestStreamPeer drives a requestStream from the other end of the wire:
// send puts a framed message where the request stream will read it, sent
// returns what the request stream wrote, and state reports the RESET_STREAM
// and STOP_SENDING codes it emitted and whether it FINed.
type requestStreamPeer struct {
	incoming *streamPipe

	mu     sync.Mutex
	buf    bytes.Buffer
	resets []StreamErrorCode
	stops  []StreamErrorCode
	closed int
}

func newRequestStreamPeer(t *testing.T, parent context.Context) (*requestStream, *requestStreamPeer) {
	t.Helper()
	ctrl := gomock.NewController(t)
	stream := NewMockStream(ctrl)
	p := &requestStreamPeer{incoming: newStreamPipe()}

	stream.EXPECT().Read(gomock.Any()).DoAndReturn(p.incoming.Read).AnyTimes()
	stream.EXPECT().Write(gomock.Any()).DoAndReturn(func(b []byte) (int, error) {
		p.mu.Lock()
		defer p.mu.Unlock()
		return p.buf.Write(b)
	}).AnyTimes()
	stream.EXPECT().Close().DoAndReturn(func() error {
		p.mu.Lock()
		defer p.mu.Unlock()
		p.closed++
		return nil
	}).AnyTimes()
	stream.EXPECT().Reset(gomock.Any()).Do(func(code uint32) {
		p.mu.Lock()
		defer p.mu.Unlock()
		p.resets = append(p.resets, StreamErrorCode(code))
	}).AnyTimes()
	stream.EXPECT().Stop(gomock.Any()).Do(func(code uint32) {
		p.mu.Lock()
		p.stops = append(p.stops, StreamErrorCode(code))
		p.mu.Unlock()
		// A real transport fails a pending Read once the receiving side is
		// shut down. Without that a read loop would never notice a local
		// cancel, which is the mechanism these tests are about.
		p.incoming.close(errStreamReaderStopped)
	}).AnyTimes()

	// A test that leaves a read loop blocked would hang rather than fail, and
	// goleak would have nothing to report because the goroutine never ends.
	t.Cleanup(func() { p.incoming.close(io.EOF) })

	return newRequestStream(parent, stream, qlogger{}), p
}

var errStreamReaderStopped = errors.New("stream reader stopped")

// send frames msg and hands it to the request stream's reader.
func (p *requestStreamPeer) send(t *testing.T, msg wire2.MessageV18) {
	t.Helper()
	buf, err := wire2.AppendControlMessage(nil, msg)
	require.NoError(t, err)
	_, err = p.incoming.Write(buf)
	require.NoError(t, err)
}

// fin ends the peer's half of the stream cleanly.
func (p *requestStreamPeer) fin() { p.incoming.close(io.EOF) }

// reset ends the peer's half abruptly.
func (p *requestStreamPeer) reset(err error) { p.incoming.close(err) }

// sent parses everything the request stream wrote, in order.
func (p *requestStreamPeer) sent(t *testing.T) []wire2.ControlMessage {
	t.Helper()
	p.mu.Lock()
	raw := bytes.Clone(p.buf.Bytes())
	p.mu.Unlock()

	parser := wire2.NewControlMessageParser(bytes.NewReader(raw), wire2.ScopeRequest)
	var msgs []wire2.ControlMessage
	for {
		msg, err := parser.Parse()
		if errors.Is(err, io.EOF) {
			return msgs
		}
		require.NoError(t, err)
		msgs = append(msgs, msg)
	}
}

func (p *requestStreamPeer) state() (resets, stops []StreamErrorCode, closes int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return slices.Clone(p.resets), slices.Clone(p.stops), p.closed
}

func TestRequestStreamWrite(t *testing.T) {
	rs, peer := newRequestStreamPeer(t, context.Background())
	defer rs.cancel(StreamErrorCancelled, errors.New("test over"))

	require.NoError(t, rs.write(&wire2.Subscribe{
		RequestID:      2,
		TrackNamespace: [][]byte{[]byte("example.com")},
		TrackName:      []byte("v0"),
	}))
	require.NoError(t, rs.write(&wire2.RequestUpdate{RequestID: 2}))

	sent := peer.sent(t)
	require.Len(t, sent, 2)
	assert.Equal(t, wire2.ControlMessageTypeSubscribe, sent[0].Type())
	assert.Equal(t, uint64(2), sent[0].(*wire2.Subscribe).RequestID)
	assert.Equal(t, wire2.ControlMessageTypeRequestUpdate, sent[1].Type())
}

// A graceful ending is one final message and a FIN, and nothing may follow it.
func TestRequestStreamFinish(t *testing.T) {
	rs, peer := newRequestStreamPeer(t, context.Background())

	require.NoError(t, rs.finish(&wire2.RequestError{
		ErrorCode:   0x10,
		ErrorReason: "no such track",
	}))

	sent := peer.sent(t)
	require.Len(t, sent, 1)
	assert.Equal(t, wire2.ControlMessageTypeRequestError, sent[0].Type())

	_, _, closes := peer.state()
	assert.Equal(t, 1, closes)

	assert.ErrorIs(t, rs.write(&wire2.RequestUpdate{}), errRequestStreamSendClosed)
	assert.NoError(t, rs.closeSend(), "closeSend is idempotent")
	_, _, closes = peer.state()
	assert.Equal(t, 1, closes, "a second closeSend must not FIN again")
}

// A FIN from the peer ends the read loop cleanly and cancels the request, so a
// publisher sees the subscriber go away without a message saying so.
func TestRequestStreamRunEndsOnPeerFIN(t *testing.T) {
	rs, peer := newRequestStreamPeer(t, context.Background())

	var got []wire2.ControlMessage
	done := make(chan error, 1)
	go func() {
		done <- rs.run(func(msg wire2.ControlMessage) error {
			got = append(got, msg)
			return nil
		})
	}()

	peer.send(t, &wire2.RequestUpdate{RequestID: 4})
	peer.send(t, &wire2.PublishBlocked{TrackNamespaceSuffix: [][]byte{[]byte("ns")}, TrackName: []byte("v0")})
	peer.fin()

	require.NoError(t, <-done)
	require.Len(t, got, 2)
	assert.Equal(t, wire2.ControlMessageTypeRequestUpdate, got[0].Type())
	assert.Equal(t, wire2.ControlMessageTypePublishBlocked, got[1].Type())

	<-rs.Context().Done()
	assert.ErrorIs(t, context.Cause(rs.Context()), errPeerFinishedRequest)
}

func TestRequestStreamRunEndsOnPeerReset(t *testing.T) {
	rs, peer := newRequestStreamPeer(t, context.Background())

	done := make(chan error, 1)
	go func() {
		done <- rs.run(func(wire2.ControlMessage) error { return nil })
	}()

	peerGone := errors.New("peer reset the stream")
	peer.reset(peerGone)

	require.ErrorIs(t, <-done, peerGone)
	<-rs.Context().Done()
	assert.ErrorIs(t, context.Cause(rs.Context()), peerGone)
}

// A handler that fails ends the request: there is nothing else to do with a
// message we cannot process on a stream that is the request.
func TestRequestStreamRunEndsOnHandlerError(t *testing.T) {
	rs, peer := newRequestStreamPeer(t, context.Background())
	defer rs.cancel(StreamErrorInternal, errors.New("test over"))

	handlerErr := errors.New("cannot apply update")
	done := make(chan error, 1)
	go func() {
		done <- rs.run(func(wire2.ControlMessage) error { return handlerErr })
	}()

	peer.send(t, &wire2.RequestUpdate{RequestID: 4})

	require.ErrorIs(t, <-done, handlerErr)
	assert.ErrorIs(t, context.Cause(rs.Context()), handlerErr)
}

// Cancelling is draft-18's UNSUBSCRIBE: both directions terminate abruptly,
// with a code that says why, and the read loop unblocks.
func TestRequestStreamCancelResetsBothDirections(t *testing.T) {
	rs, peer := newRequestStreamPeer(t, context.Background())

	done := make(chan error, 1)
	go func() {
		done <- rs.run(func(wire2.ControlMessage) error { return nil })
	}()

	gone := errors.New("subscriber no longer interested")
	rs.cancel(StreamErrorCancelled, gone)

	require.Error(t, <-done)
	resets, stops, closes := peer.state()
	assert.Equal(t, []StreamErrorCode{StreamErrorCancelled}, resets)
	assert.Equal(t, []StreamErrorCode{StreamErrorCancelled}, stops)
	assert.Zero(t, closes, "a cancelled stream must be reset, not FINed")
	assert.ErrorIs(t, context.Cause(rs.Context()), gone)
}

func TestRequestStreamCancelIsIdempotent(t *testing.T) {
	rs, peer := newRequestStreamPeer(t, context.Background())

	first := errors.New("first")
	rs.cancel(StreamErrorCancelled, first)
	rs.cancel(StreamErrorInternal, errors.New("second"))

	resets, stops, _ := peer.state()
	assert.Equal(t, []StreamErrorCode{StreamErrorCancelled}, resets)
	assert.Equal(t, []StreamErrorCode{StreamErrorCancelled}, stops)
	assert.ErrorIs(t, context.Cause(rs.Context()), first, "the first cause is the one that ended it")
}

// After a graceful FIN there is nothing left to reset on our side, but the
// peer still has to be told to stop sending.
func TestRequestStreamCancelAfterFIN(t *testing.T) {
	rs, peer := newRequestStreamPeer(t, context.Background())

	require.NoError(t, rs.closeSend())
	rs.cancel(StreamErrorGoingAway, errors.New("going away"))

	resets, stops, closes := peer.state()
	assert.Empty(t, resets)
	assert.Equal(t, []StreamErrorCode{StreamErrorGoingAway}, stops)
	assert.Equal(t, 1, closes)
}

func TestRequestStreamReadRequest(t *testing.T) {
	rs, peer := newRequestStreamPeer(t, context.Background())
	defer rs.cancel(StreamErrorCancelled, errors.New("test over"))

	peer.send(t, &wire2.Subscribe{
		RequestID:      1,
		TrackNamespace: [][]byte{[]byte("example.com")},
		TrackName:      []byte("v0"),
	})

	msg, err := rs.readRequest()
	require.NoError(t, err)
	assert.Equal(t, wire2.ControlMessageTypeSubscribe, msg.Type())
}

// Section 3.3 lists the seven types that may open a request stream. A message
// that is valid later on the same stream is still a violation as the first one.
func TestRequestStreamReadRequestRejectsResponseMessage(t *testing.T) {
	rs, peer := newRequestStreamPeer(t, context.Background())
	defer rs.cancel(StreamErrorInternal, errors.New("test over"))

	peer.send(t, &wire2.RequestOk{})

	_, err := rs.readRequest()
	assert.ErrorIs(t, err, errNotARequestMessage)
}

// A session that ends cancels every request it carries, without each request
// needing a goroutine to watch for it.
func TestRequestStreamFollowsSessionContext(t *testing.T) {
	session, closeSession := context.WithCancelCause(context.Background())
	rs, _ := newRequestStreamPeer(t, session)
	defer rs.cancel(StreamErrorSessionClosed, errors.New("test over"))

	sessionGone := errors.New("session closed")
	closeSession(sessionGone)

	<-rs.Context().Done()
	assert.ErrorIs(t, context.Cause(rs.Context()), sessionGone)
}
