package moqtransport

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"

	"github.com/Eyevinn/moqtransport/internal/wire2"
)

// StreamErrorCode is the application error code an endpoint sends with
// RESET_STREAM or STOP_SENDING on a MOQT stream
// (draft-ietf-moq-transport-18, Section 3.3.3).
//
// These codes carry meaning that used to travel in messages. draft-17 deleted
// UNSUBSCRIBE, UNANNOUNCE, ANNOUNCE_CANCEL, UNSUBSCRIBE_ANNOUNCES and
// FETCH_CANCEL, and cancelling a request became resetting the stream that
// carries it, so the code is now the only thing that says why.
//
// QUIC application error codes are 62-bit, but the [SendStream] and
// [ReceiveStream] interfaces narrow them to 32 bits, which comfortably holds
// every code the draft defines.
type StreamErrorCode uint32

const (
	// StreamErrorInternal is an implementation specific error.
	StreamErrorInternal StreamErrorCode = 0x0
	// StreamErrorCancelled means the stream was cancelled by either endpoint.
	// For subscriptions PUBLISH_DONE may carry a more detailed status.
	StreamErrorCancelled StreamErrorCode = 0x1
	// StreamErrorDeliveryTimeout means a delivery timeout was exceeded for
	// this stream.
	StreamErrorDeliveryTimeout StreamErrorCode = 0x2
	// StreamErrorSessionClosed means the session is being closed.
	StreamErrorSessionClosed StreamErrorCode = 0x3
	// StreamErrorGoingAway means the request is rejected because the endpoint
	// has sent or received a GOAWAY.
	StreamErrorGoingAway StreamErrorCode = 0x4
	// StreamErrorTooFarBehind means the subscription exceeded the publisher's
	// resource limits and is being terminated.
	StreamErrorTooFarBehind StreamErrorCode = 0x5
	// StreamErrorUnknownObjectStatus means that, in response to a FETCH, the
	// publisher cannot determine the status of the next Object in the range.
	StreamErrorUnknownObjectStatus StreamErrorCode = 0x6
	// StreamErrorExpiredAuthToken means the request's authorization token has
	// expired.
	StreamErrorExpiredAuthToken StreamErrorCode = 0x7
	// StreamErrorExcessiveLoad means the endpoint is overloaded.
	StreamErrorExcessiveLoad StreamErrorCode = 0x9
	// StreamErrorMalformedTrack means a relay publisher detected that the
	// track was malformed.
	StreamErrorMalformedTrack StreamErrorCode = 0x12
)

func (c StreamErrorCode) String() string {
	switch c {
	case StreamErrorInternal:
		return "INTERNAL_ERROR"
	case StreamErrorCancelled:
		return "CANCELLED"
	case StreamErrorDeliveryTimeout:
		return "DELIVERY_TIMEOUT"
	case StreamErrorSessionClosed:
		return "SESSION_CLOSED"
	case StreamErrorGoingAway:
		return "GOING_AWAY"
	case StreamErrorTooFarBehind:
		return "TOO_FAR_BEHIND"
	case StreamErrorUnknownObjectStatus:
		return "UNKNOWN_OBJECT_STATUS"
	case StreamErrorExpiredAuthToken:
		return "EXPIRED_AUTH_TOKEN"
	case StreamErrorExcessiveLoad:
		return "EXCESSIVE_LOAD"
	case StreamErrorMalformedTrack:
		return "MALFORMED_TRACK"
	}
	return fmt.Sprintf("unknown stream error code: %#x", uint32(c))
}

// requestStream is the shared machinery of one request: the bidirectional
// stream that carries it, and the lifetime of that stream.
//
// draft-18 makes the stream the identity of a request (Section 10.1) --
// responses carry no Request ID, and the five messages that used to end a
// request are now a reset or a FIN of this stream. A request is therefore an
// object with a lifetime rather than a message plus a response writer, and
// this type is that lifetime. Every concrete request embeds it, incoming or
// outgoing.
//
// The zero value is not usable; see newRequestStream. Whoever creates one is
// responsible for ending it: run ends it on any read outcome, and cancel ends
// it directly, but a requestStream that is neither run nor cancelled leaks its
// context until the session's context is cancelled.
type requestStream struct {
	stream Stream
	parser *wire2.ControlMessageParser

	ctx       context.Context
	cancelCtx context.CancelCauseFunc

	mu sync.Mutex
	// sendDone records that our half is finished, by FIN or by reset. Writing
	// after that is a bug in this package rather than a transport error, so it
	// is caught here instead of being handed to the transport.
	sendDone bool
	// readStopped records that STOP_SENDING has been sent, so that cancelling
	// twice does not send it twice.
	readStopped bool
}

// newRequestStream wraps an already-open bidirectional stream. parent is the
// session's context, so a session that ends cancels every request it carries.
func newRequestStream(parent context.Context, stream Stream) *requestStream {
	ctx, cancel := context.WithCancelCause(parent)
	return &requestStream{
		stream:    stream,
		parser:    wire2.NewControlMessageParser(stream, wire2.ScopeRequest),
		ctx:       ctx,
		cancelCtx: cancel,
	}
}

// Context is cancelled when the request ends for any reason: the peer reset
// the stream or closed its half, we cancelled or rejected the request, or the
// session went away. [context.Cause] says which.
//
// This is how a publisher learns that a subscriber has gone, which in draft-18
// is a stream reset rather than a message.
func (r *requestStream) Context() context.Context {
	return r.ctx
}

// write frames msg and sends it. Concurrent calls are serialized, so a message
// is never interleaved with another.
func (r *requestStream) write(msg wire2.MessageV18) error {
	// Encoding outside the lock keeps a large message from blocking a small
	// one; the write itself is what has to be atomic.
	buf, err := wire2.AppendControlMessage(nil, msg)
	if err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.sendDone {
		return errRequestStreamSendClosed
	}
	_, err = r.stream.Write(buf)
	return err
}

// closeSend finishes our half of the stream: the peer sees a FIN, we send
// nothing more, and the peer's half stays open.
//
// This is the graceful ending of a request -- REQUEST_ERROR then FIN
// (Section 3.3.2), TRACK_STATUS_OK then FIN, PUBLISH_DONE then FIN. It is
// idempotent, and does nothing at all once the stream has been reset.
func (r *requestStream) closeSend() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.sendDone {
		return nil
	}
	r.sendDone = true
	return r.stream.Close()
}

// finish sends msg and then FINs, which is the shape of every graceful ending:
// one final message with nothing after it.
func (r *requestStream) finish(msg wire2.MessageV18) error {
	if err := r.write(msg); err != nil {
		return err
	}
	return r.closeSend()
}

// cancel abandons the request, terminating whatever is still open in either
// direction as Section 3.3.2 requires, and cancels the request's context with
// cause.
//
// This is what draft-18 uses in place of UNSUBSCRIBE, UNANNOUNCE,
// ANNOUNCE_CANCEL, UNSUBSCRIBE_ANNOUNCES and FETCH_CANCEL. It is safe to call
// from any goroutine and more than once; only the first call reaches the
// transport.
func (r *requestStream) cancel(code StreamErrorCode, cause error) {
	r.mu.Lock()
	resetSend := !r.sendDone
	r.sendDone = true
	stopRead := !r.readStopped
	r.readStopped = true
	r.mu.Unlock()

	if resetSend {
		r.stream.Reset(uint32(code))
	}
	if stopRead {
		// STOP_SENDING both tells the peer to stop writing and unblocks our
		// own pending Read, which is what lets a running read loop exit.
		r.stream.Stop(uint32(code))
	}
	r.cancelCtx(cause)
}

// readMessage reads one control message from the peer's half of the stream.
//
// The accept loop needs this before there is a request object to own the
// stream: the message that opens a request stream is what says which kind of
// request it is.
func (r *requestStream) readMessage() (wire2.ControlMessage, error) {
	return r.parser.Parse()
}

// readRequest reads the message that opens the stream, rejecting anything that
// is not allowed to open one.
//
// Section 3.3 lists seven message types that may; a bidirectional stream
// beginning with any other type MUST close the session with a
// PROTOCOL_VIOLATION. That is a separate question from whether the type is
// known -- REQUEST_OK is a perfectly good message in the wrong place.
func (r *requestStream) readRequest() (wire2.ControlMessage, error) {
	msg, err := r.readMessage()
	if err != nil {
		return nil, err
	}
	if !msg.Type().OpensRequestStream() {
		return nil, errNotARequestMessage
	}
	return msg, nil
}

// run reads the peer's half of the stream, passing each message to handle,
// until the stream ends. It returns nil when the peer FINs cleanly and the
// failure otherwise; either way the request's context is cancelled before it
// returns, which is how the rest of the library learns that the peer has gone.
//
// run is the request's reader goroutine, so handle runs on it. A slow handle
// stalls this stream's reads, and with them the notice that the peer reset the
// stream, so anything that can block belongs on a channel rather than in the
// callback.
func (r *requestStream) run(handle func(wire2.ControlMessage) error) error {
	err := r.readAll(handle)
	cause := err
	if cause == nil {
		cause = errPeerFinishedRequest
	}
	r.cancelCtx(cause)
	return err
}

func (r *requestStream) readAll(handle func(wire2.ControlMessage) error) error {
	for {
		msg, err := r.readMessage()
		if err != nil {
			// io.EOF, and only io.EOF, is the peer's FIN landing between
			// messages; the parser reports a truncated message as
			// io.ErrUnexpectedEOF.
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
		if err := handle(msg); err != nil {
			return err
		}
	}
}

var (
	// errRequestStreamSendClosed means a message was written after our half of
	// the stream was finished or reset.
	errRequestStreamSendClosed = errors.New("request stream send side is closed")

	// errPeerFinishedRequest is the context cause when the peer FINs its half
	// of a request stream. Whether that ends the request or merely means "no
	// more messages from me" is per request type -- a SUBSCRIBE_NAMESPACE is
	// cancelled by a FIN (Section 6.1), while a subscription is not -- so the
	// distinction is left to the cause rather than baked in here.
	errPeerFinishedRequest = errors.New("peer finished its half of the request stream")

	// errNotARequestMessage is a bidirectional stream that began with a
	// message type not allowed to open one (Section 3.3).
	errNotARequestMessage = ProtocolError{
		code:    SessionErrorProtocolViolation,
		message: "bidirectional stream did not begin with a request message",
	}
)
