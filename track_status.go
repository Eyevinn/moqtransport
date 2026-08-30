package moqtransport

import (
	"context"
	"fmt"
	"sync"

	"github.com/Eyevinn/moqtransport/internal/wire2"
)

// TrackStatusHandler answers TRACK_STATUS requests, by which a potential
// subscriber asks about a track without subscribing to it
// (draft-ietf-moq-transport-18, Section 10.14).
//
// A TRACK_STATUS is treated exactly like a SUBSCRIBE except that it creates no
// subscription state and delivers no Objects, so a handler that has the
// answers for SUBSCRIBE has them for this too.
type TrackStatusHandler interface {
	HandleTrackStatus(*TrackStatusRequest)
}

// TrackStatusHandlerFunc adapts a function to [TrackStatusHandler].
type TrackStatusHandlerFunc func(*TrackStatusRequest)

func (f TrackStatusHandlerFunc) HandleTrackStatus(r *TrackStatusRequest) { f(r) }

// TrackStatus is what a publisher reports about a track: the same parameters
// and Track Properties it would have put in a SUBSCRIBE_OK, minus the Track
// Alias, which a status request does not need.
type TrackStatus struct {
	Parameters Parameters
	Properties KVPList
}

// LargestObject returns the largest Location the publisher has for the track,
// and whether it reported one. Its absence means nothing has been published.
func (s TrackStatus) LargestObject() (Location, bool) {
	return s.Parameters.LargestObject()
}

// TrackStatusRequest is an incoming TRACK_STATUS.
type TrackStatusRequest struct {
	*requestStream

	requestID uint64
	namespace []string
	track     string
	params    wire2.Parameters

	mu       sync.Mutex
	answered bool
}

func newTrackStatusRequest(rs *requestStream, msg *wire2.TrackStatus) *TrackStatusRequest {
	return &TrackStatusRequest{
		requestStream: rs,
		requestID:     msg.RequestID,
		namespace:     namespaceStrings(msg.TrackNamespace),
		track:         string(msg.TrackName),
		params:        msg.Parameters,
	}
}

// RequestID is the ID the peer assigned to this TRACK_STATUS.
func (r *TrackStatusRequest) RequestID() uint64 { return r.requestID }

// Namespace is the Track Namespace being asked about.
func (r *TrackStatusRequest) Namespace() []string { return r.namespace }

// Track is the Track Name being asked about.
func (r *TrackStatusRequest) Track() string { return r.track }

// Parameters returns the Message Parameters the peer sent. A TRACK_STATUS
// carries no delivery parameters -- there is nothing to deliver -- so
// SUBSCRIBER_PRIORITY and its like are absent by definition.
func (r *TrackStatusRequest) Parameters() Parameters { return r.params }

// Accept answers with TRACK_STATUS_OK and finishes the stream. A status
// request is one message and one answer; there is nothing to follow it.
func (r *TrackStatusRequest) Accept(status TrackStatus) error {
	if err := r.claim(); err != nil {
		return err
	}
	params := status.Parameters
	if params == nil {
		params = wire2.Parameters{}
	}
	err := r.finish(&wire2.RequestOk{
		Parameters:      params,
		TrackProperties: status.Properties,
	})
	r.cancelCtx(errTrackStatusAnswered)
	return err
}

// Reject answers with REQUEST_ERROR and finishes the stream.
func (r *TrackStatusRequest) Reject(code RequestErrorCode, reason string) error {
	if err := r.claim(); err != nil {
		return err
	}
	err := r.finish(&wire2.RequestError{ErrorCode: uint64(code), ErrorReason: reason})
	r.cancelCtx(fmt.Errorf("rejected the track status request: %v", code))
	return err
}

func (r *TrackStatusRequest) claim() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.answered {
		return errRequestAlreadyAnswered
	}
	r.answered = true
	return nil
}

func (r *TrackStatusRequest) serve(h TrackStatusHandler) error {
	if h == nil {
		return r.Reject(RequestErrorNotSupported, "track status is not supported")
	}
	go h.HandleTrackStatus(r)
	return r.run(r.handleMessage)
}

// handleMessage rejects everything: Section 10.14 says the subscriber cannot
// send REQUEST_UPDATE, and TRACK_STATUS is the first and only message on the
// stream.
func (r *TrackStatusRequest) handleMessage(wire2.ControlMessage) error {
	return errUnexpectedMessageOnRequestStream
}

// TrackStatus asks the peer about a track without subscribing to it.
func (s *Session) TrackStatus(ctx context.Context, namespace []string, track string) (TrackStatus, error) {
	msg := &wire2.TrackStatus{
		RequestID:      s.requestIDs.nextID(),
		TrackNamespace: namespaceFields(namespace),
		TrackName:      []byte(track),
		Parameters:     wire2.Parameters{},
	}

	stream, err := s.conn.OpenStreamSync(ctx)
	if err != nil {
		return TrackStatus{}, err
	}
	rs := newRequestStream(s.ctx, stream, s.qlog)
	pending := &trackStatusRequest{answered: make(chan struct{})}

	go func() {
		if err := rs.run(pending.handleMessage); err != nil {
			s.failIfProtocolError(err)
		}
		pending.streamEnded(rs)
	}()

	if err := rs.write(msg); err != nil {
		rs.cancel(StreamErrorInternal, err)
		return TrackStatus{}, err
	}
	// TRACK_STATUS is the first and only message this side sends, so our half
	// is done as soon as it is out.
	if err := rs.closeSend(); err != nil {
		rs.cancel(StreamErrorInternal, err)
		return TrackStatus{}, err
	}

	select {
	case <-pending.answered:
	case <-ctx.Done():
		rs.cancel(StreamErrorCancelled, ctx.Err())
		return TrackStatus{}, ctx.Err()
	}
	return pending.result()
}

// trackStatusRequest is the outgoing side's state: one answer, then the stream
// is over.
type trackStatusRequest struct {
	answered  chan struct{}
	closeOnce sync.Once

	mu     sync.Mutex
	status TrackStatus
	err    error
}

func (t *trackStatusRequest) handleMessage(msg wire2.ControlMessage) error {
	switch m := msg.(type) {
	case *wire2.RequestOk:
		t.mu.Lock()
		t.status = TrackStatus{Parameters: m.Parameters, Properties: m.TrackProperties}
		t.mu.Unlock()
		t.closeOnce.Do(func() { close(t.answered) })
		return nil

	case *wire2.RequestError:
		t.mu.Lock()
		t.err = &RequestError{Code: RequestErrorCode(m.ErrorCode), Reason: m.ErrorReason}
		t.mu.Unlock()
		t.closeOnce.Do(func() { close(t.answered) })
		return nil
	}
	return errUnexpectedMessageOnRequestStream
}

// streamEnded releases a caller still waiting for an answer that is not coming.
func (t *trackStatusRequest) streamEnded(rs *requestStream) {
	t.closeOnce.Do(func() {
		t.mu.Lock()
		if t.err == nil {
			t.err = context.Cause(rs.Context())
		}
		t.mu.Unlock()
		close(t.answered)
	})
}

func (t *trackStatusRequest) result() (TrackStatus, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.err != nil {
		return TrackStatus{}, t.err
	}
	return t.status, nil
}

var errTrackStatusAnswered = fmt.Errorf("track status request answered")
