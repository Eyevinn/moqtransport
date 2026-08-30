package moqtransport

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"sync"

	"github.com/Eyevinn/moqtransport/internal/wire2"
	"github.com/mengelbart/qlog/moqt"
)

// FetchHandler answers FETCH requests, by which a subscriber asks for a range
// of Objects that already exist (draft-ietf-moq-transport-18, Section 10.12).
//
// A FETCH is answered on its request stream but delivered on a unidirectional
// stream of its own, so Accept gives back a writer rather than the request
// itself.
type FetchHandler interface {
	HandleFetch(*FetchRequest)
}

// FetchHandlerFunc adapts a function to [FetchHandler].
type FetchHandlerFunc func(*FetchRequest)

func (f FetchHandlerFunc) HandleFetch(r *FetchRequest) { f(r) }

// EndOfRange marks a run of Objects in a FETCH response that were not
// serialized, because they do not exist or their status is unknown.
type EndOfRange = wire2.EndOfRange

const (
	// EndOfRangeNonExistent says the covered Objects are known not to exist.
	EndOfRangeNonExistent = wire2.EndOfRangeNonExistent
	// EndOfRangeUnknown says the publisher cannot determine their status.
	EndOfRangeUnknown = wire2.EndOfRangeUnknown
)

// FetchObject is one record of a FETCH response.
//
// A FETCH carries no Object Status: absence is expressed by an End of Range
// marker covering a run of Locations rather than per Object, so the embedded
// Object's Status is unused here.
type FetchObject struct {
	Object

	// EndOfRange is zero for a real Object. Otherwise this record is a marker,
	// and every Location from the previous record up to and including this
	// one either does not exist or has unknown status.
	EndOfRange EndOfRange
}

// fetchSession is what a FETCH needs from the session that carries it.
type fetchSession interface {
	openUniStream(ctx context.Context) (SendStream, error)

	// subscriptionByRequestID finds a peer-initiated subscription, which is
	// what a Joining FETCH names.
	subscriptionByRequestID(requestID uint64) (*Subscription, bool)

	registerFetch(requestID uint64, f *FetchStream)
	unregisterFetch(requestID uint64)
}

// FetchRequest is an incoming FETCH.
//
// A Joining FETCH arrives naming a subscription rather than a range; by the
// time a handler sees it the range has been resolved against that
// subscription, so Namespace, Track and Range answer the same way for both
// kinds.
type FetchRequest struct {
	*requestStream

	session          fetchSession
	requestID        uint64
	fetchType        wire2.FetchType
	joiningRequestID uint64
	namespace        []string
	track            string
	start            Location
	end              Location
	order            GroupOrder
	params           wire2.Parameters

	mu       sync.Mutex
	answered bool
}

// RequestID is the ID the subscriber assigned to this FETCH.
func (r *FetchRequest) RequestID() uint64 { return r.requestID }

// Namespace is the Track Namespace being fetched.
func (r *FetchRequest) Namespace() []string { return r.namespace }

// Track is the Track Name being fetched.
func (r *FetchRequest) Track() string { return r.track }

// Range is the requested range: the first Location wanted, and the Location
// one past the last. An End Location whose Object is 0 means the whole of that
// Group.
func (r *FetchRequest) Range() (start, end Location) { return r.start, r.end }

// GroupOrder is the order the response must be delivered in. A FETCH that did
// not ask is Ascending (Section 10.2.8).
func (r *FetchRequest) GroupOrder() GroupOrder { return r.order }

// Joining reports whether this is a Joining FETCH, and the Request ID of the
// subscription it joins. The range has already been resolved against that
// subscription, so this is for logging rather than for working anything out.
func (r *FetchRequest) Joining() (uint64, bool) {
	if r.fetchType == wire2.FetchTypeStandalone {
		return 0, false
	}
	return r.joiningRequestID, true
}

// Parameters returns the Message Parameters the subscriber sent.
func (r *FetchRequest) Parameters() Parameters { return r.params }

// Accept answers with FETCH_OK and opens the unidirectional stream the Objects
// travel on.
//
// The response stream is separate from the request stream, so Objects cannot
// be written before the FETCH has been answered.
func (r *FetchRequest) Accept(opts ...FetchOkOption) (*FetchResponse, error) {
	r.mu.Lock()
	if r.answered {
		r.mu.Unlock()
		return nil, errRequestAlreadyAnswered
	}
	r.answered = true
	r.mu.Unlock()

	ok := &wire2.FetchOk{
		EndLocation: r.end,
		Parameters:  wire2.Parameters{},
	}
	for _, opt := range opts {
		opt(ok)
	}

	stream, err := r.session.openUniStream(r.ctx)
	if err != nil {
		return nil, err
	}
	header := wire2.AppendFetchHeader(nil, r.requestID)
	if _, err := stream.Write(header); err != nil {
		stream.Reset(uint32(StreamErrorInternal))
		return nil, err
	}

	if err := r.write(ok); err != nil {
		stream.Reset(uint32(StreamErrorInternal))
		return nil, err
	}
	r.qlog.logStreamType(moqt.OwnerLocal, stream, moqt.StreamTypeFetchHeader)
	return &FetchResponse{
		stream: stream,
		writer: wire2.NewFetchWriter(r.order),
		qlog:   r.qlog,
	}, nil
}

// Reject answers with REQUEST_ERROR and finishes the stream. No Objects are
// sent for a request that ends in an error.
func (r *FetchRequest) Reject(code RequestErrorCode, reason string) error {
	r.mu.Lock()
	if r.answered {
		r.mu.Unlock()
		return errRequestAlreadyAnswered
	}
	r.answered = true
	r.mu.Unlock()

	err := r.finish(&wire2.RequestError{ErrorCode: uint64(code), ErrorReason: reason})
	r.cancelCtx(fmt.Errorf("rejected the fetch: %v", code))
	return err
}

func (r *FetchRequest) serve(h FetchHandler) error {
	if h == nil {
		return r.Reject(RequestErrorNotSupported, "fetch is not supported")
	}
	go h.HandleFetch(r)
	return r.run(r.handleMessage)
}

func (r *FetchRequest) handleMessage(msg wire2.ControlMessage) error {
	if _, ok := msg.(*wire2.RequestUpdate); !ok {
		return errUnexpectedMessageOnRequestStream
	}
	// Section 10.9.1: a REQUEST_UPDATE that fails for a FETCH also resets the
	// response stream. Nothing in a FETCH is updatable here, so the honest
	// answer is that it failed.
	return r.write(&wire2.RequestError{
		ErrorCode:   uint64(RequestErrorNotSupported),
		ErrorReason: "updating a fetch is not supported",
	})
}

// FetchResponse is the unidirectional stream a FETCH's Objects are written to.
//
// Records must be written in the order the request asked for: Group IDs
// ascending or descending as the Group Order says, and Object IDs increasing
// within a Group. The wire format encodes each against the one before it, so
// there is no way to go back.
type FetchResponse struct {
	mu     sync.Mutex
	stream SendStream
	writer *wire2.FetchWriter
	qlog   qlogger
	done   bool
}

// StreamID returns the ID of the underlying stream.
func (f *FetchResponse) StreamID() uint64 { return f.stream.StreamID() }

// WriteObject writes one Object of the response.
func (f *FetchResponse) WriteObject(o Object) error {
	return f.write(&wire2.FetchObject{
		GroupID:    o.GroupID,
		SubgroupID: o.SubgroupID,
		ObjectID:   o.ObjectID,
		Priority:   o.Priority,
		Properties: o.Properties,
		Payload:    o.Payload,
		Datagram:   o.ForwardingPreference == ObjectForwardingPreferenceDatagram,
	})
}

// WriteEndOfRange marks every Location from the previous record up to and
// including this one as absent or of unknown status.
//
// A publisher should reach for this only to split a run that will not be
// serialized into the part known not to exist and the part whose status it
// cannot determine; an ordinary gap in Object IDs already says the Objects do
// not exist.
func (f *FetchResponse) WriteEndOfRange(kind EndOfRange, location Location) error {
	return f.write(&wire2.FetchObject{
		GroupID:    location.Group,
		ObjectID:   location.Object,
		EndOfRange: kind,
	})
}

func (f *FetchResponse) write(obj *wire2.FetchObject) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.done {
		return errFetchResponseClosed
	}
	// The writer holds the prior-record state that every delta is against, so
	// encoding belongs under the same lock as the write.
	buf, err := f.writer.AppendObject(nil, obj)
	if err != nil {
		return err
	}
	if _, err := f.stream.Write(buf); err != nil {
		return err
	}
	f.qlog.logFetchObject(moqt.FetchObjectEventCreated, f.stream, obj)
	return nil
}

// Close finishes the response with a FIN, which is what tells the subscriber
// that the range is complete: any gap between the last Object and the End
// Location means those Objects do not exist.
//
// A response with no Objects at all is a header and a FIN, which is a
// legitimate and meaningful answer.
func (f *FetchResponse) Close() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.done {
		return nil
	}
	f.done = true
	return f.stream.Close()
}

// Reset abandons the response, saying the range was not delivered in full.
func (f *FetchResponse) Reset(code StreamErrorCode) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.done {
		return
	}
	f.done = true
	f.stream.Reset(uint32(code))
}

// FetchOkOption customises the FETCH_OK sent by Accept.
type FetchOkOption func(*wire2.FetchOk)

// WithEndOfTrack says the End Location is the end of the track, so no Object
// at or beyond it will ever exist.
func WithEndOfTrack() FetchOkOption {
	return func(ok *wire2.FetchOk) { ok.EndOfTrack = true }
}

// WithFetchEndLocation overrides the End Location reported in FETCH_OK, which
// otherwise echoes what was asked for. It is the largest Location the response
// covers, and a gap between the last Object and it means those Objects do not
// exist.
func WithFetchEndLocation(l Location) FetchOkOption {
	return func(ok *wire2.FetchOk) { ok.EndLocation = l }
}

// WithFetchTrackProperties attaches Track Properties to the response.
func WithFetchTrackProperties(properties KVPList) FetchOkOption {
	return func(ok *wire2.FetchOk) { ok.TrackProperties = append(ok.TrackProperties, properties...) }
}

// FetchStream is a FETCH this endpoint made: the request, its answer, and the
// Objects that come back on a stream of their own.
type FetchStream struct {
	*requestStream

	session   fetchSession
	requestID uint64
	order     GroupOrder

	established   chan struct{}
	establishOnce sync.Once

	// objects is fed by the data stream's reader goroutine and never closed;
	// responseDone is what says no more will come.
	objects chan *FetchObject

	// responseDone is closed when the data stream ends, which is separate from
	// the request stream ending. Section 10.12.3 lets FETCH_OK arrive at any
	// time relative to object delivery, including after the last Object, so
	// the two cannot share a signal.
	responseDone chan struct{}
	responseOnce sync.Once

	mu          sync.Mutex
	answerErr   error
	endLocation Location
	endOfTrack  bool
	answered    bool
	dataDone    bool
}

// fetchObjectBuffer is how many records are held for a consumer that is not
// reading. Beyond it the data stream's reader blocks, which is the
// backpressure the publisher should feel.
const fetchObjectBuffer = 64

// RequestID is the ID this endpoint assigned to the FETCH. The response stream
// carries it in its FETCH_HEADER, which is how the two are matched up.
func (f *FetchStream) RequestID() uint64 { return f.requestID }

// GroupOrder is the order the response is delivered in.
func (f *FetchStream) GroupOrder() GroupOrder { return f.order }

// EndLocation is the largest Location the response covers, from FETCH_OK. A
// gap between the last Object received and it means those Objects do not
// exist, provided the stream ended with a FIN.
func (f *FetchStream) EndLocation() Location {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.endLocation
}

// EndOfTrack reports whether the End Location is the end of the track.
func (f *FetchStream) EndOfTrack() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.endOfTrack
}

// ReadObject returns the next record of the response, blocking until one
// arrives.
//
// It returns [ErrFetchComplete] once the response has been delivered in full
// and drained, and any other error if the fetch ended without completing.
func (f *FetchStream) ReadObject(ctx context.Context) (*FetchObject, error) {
	select {
	case o := <-f.objects:
		return o, nil
	default:
	}

	select {
	case o := <-f.objects:
		return o, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-f.responseDone:
		return f.drained()
	case <-f.ctx.Done():
		return f.drained()
	}
}

// drained returns whatever raced in before the ending, and otherwise the
// reason there is nothing more.
func (f *FetchStream) drained() (*FetchObject, error) {
	select {
	case o := <-f.objects:
		return o, nil
	default:
	}
	f.mu.Lock()
	complete := f.dataDone
	f.mu.Unlock()
	if complete {
		return nil, ErrFetchComplete
	}
	return nil, context.Cause(f.ctx)
}

// responseComplete records that the data stream has ended, cleanly or not.
//
// A completed fetch has nothing left to exchange on its request stream either,
// but it can only be torn down once the answer has arrived: FETCH_OK may come
// after the last Object.
func (f *FetchStream) responseComplete(complete bool) {
	f.mu.Lock()
	f.dataDone = complete
	answered := f.answered || f.answerErr != nil
	f.mu.Unlock()

	f.responseOnce.Do(func() { close(f.responseDone) })
	if answered {
		f.cancel(StreamErrorCancelled, ErrFetchComplete)
	}
}

// finishIfComplete tears the request stream down once both halves are done.
func (f *FetchStream) finishIfComplete() {
	select {
	case <-f.responseDone:
	default:
		return
	}
	f.cancel(StreamErrorCancelled, ErrFetchComplete)
}

// Close cancels the fetch. Section 5.2 requires STOP_SENDING on the request
// stream, which is what ends the publisher's state; the data stream follows
// when the publisher resets it.
func (f *FetchStream) Close() error {
	f.cancel(StreamErrorCancelled, errFetchClosedLocally)
	return nil
}

func (f *FetchStream) deliver(ctx context.Context, o *FetchObject) error {
	select {
	case f.objects <- o:
		return nil
	case <-ctx.Done():
		return context.Cause(ctx)
	case <-f.ctx.Done():
		return context.Cause(f.ctx)
	}
}

func (f *FetchStream) run() error {
	err := f.requestStream.run(f.handleMessage)
	f.establishOnce.Do(func() {
		f.mu.Lock()
		if f.answerErr == nil {
			f.answerErr = context.Cause(f.ctx)
		}
		f.mu.Unlock()
		close(f.established)
	})
	f.session.unregisterFetch(f.requestID)
	// A request stream that ends without the data stream having finished means
	// the response will never complete.
	f.responseOnce.Do(func() { close(f.responseDone) })
	return err
}

func (f *FetchStream) handleMessage(msg wire2.ControlMessage) error {
	switch m := msg.(type) {
	case *wire2.FetchOk:
		f.mu.Lock()
		if f.answered {
			f.mu.Unlock()
			return errDuplicateResponse
		}
		f.answered = true
		f.endLocation = m.EndLocation
		f.endOfTrack = m.EndOfTrack
		f.mu.Unlock()
		f.establishOnce.Do(func() { close(f.established) })
		f.finishIfComplete()
		return nil

	case *wire2.RequestError:
		f.mu.Lock()
		f.answerErr = &RequestError{Code: RequestErrorCode(m.ErrorCode), Reason: m.ErrorReason}
		f.mu.Unlock()
		f.establishOnce.Do(func() { close(f.established) })
		f.finishIfComplete()
		return nil
	}
	return errUnexpectedMessageOnRequestStream
}

func (f *FetchStream) awaitEstablished(ctx context.Context) error {
	select {
	case <-f.established:
	case <-ctx.Done():
		return ctx.Err()
	case <-f.ctx.Done():
		return context.Cause(f.ctx)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.answerErr
}

// FetchOption customises an outgoing FETCH.
type FetchOption func(*wire2.Fetch) error

// WithFetchGroupOrder asks for the Groups in a particular order. Omitted, a
// FETCH response is Ascending.
func WithFetchGroupOrder(order GroupOrder) FetchOption {
	return func(fetch *wire2.Fetch) error {
		if !order.Valid() {
			return fmt.Errorf("invalid group order: %v", order)
		}
		fetch.Parameters = append(fetch.Parameters, wire2.Uint8Parameter(wire2.ParamGroupOrder, uint8(order)))
		return nil
	}
}

// Fetch asks for the Objects in a range that already exist.
//
// end is the Location one past the last one wanted; an End Location whose
// Object is 0 asks for the whole of that Group.
func (s *Session) Fetch(ctx context.Context, namespace []string, track string, start, end Location, opts ...FetchOption) (*FetchStream, error) {
	return s.fetch(ctx, &wire2.Fetch{
		FetchType: wire2.FetchTypeStandalone,
		Standalone: &wire2.StandaloneFetch{
			TrackNamespace: namespaceFields(namespace),
			TrackName:      []byte(track),
			StartLocation:  start,
			EndLocation:    end,
		},
	}, opts...)
}

// FetchRelative asks for the Objects immediately before a subscription's
// starting point, going back groupsBack Groups.
//
// The publisher works the range out from the subscription, so what the fetch
// retrieves and what the subscription delivers are contiguous and do not
// overlap.
func (s *Session) FetchRelative(ctx context.Context, subscription *RemoteTrack, groupsBack uint64, opts ...FetchOption) (*FetchStream, error) {
	return s.fetch(ctx, &wire2.Fetch{
		FetchType: wire2.FetchTypeRelativeJoining,
		Joining: &wire2.JoiningFetch{
			JoiningRequestID: subscription.RequestID(),
			JoiningStart:     groupsBack,
		},
	}, opts...)
}

// FetchAbsolute asks for the Objects from an explicit Group up to a
// subscription's starting point.
func (s *Session) FetchAbsolute(ctx context.Context, subscription *RemoteTrack, startGroup uint64, opts ...FetchOption) (*FetchStream, error) {
	return s.fetch(ctx, &wire2.Fetch{
		FetchType: wire2.FetchTypeAbsoluteJoining,
		Joining: &wire2.JoiningFetch{
			JoiningRequestID: subscription.RequestID(),
			JoiningStart:     startGroup,
		},
	}, opts...)
}

func (s *Session) fetch(ctx context.Context, msg *wire2.Fetch, opts ...FetchOption) (*FetchStream, error) {
	msg.RequestID = s.requestIDs.nextID()
	msg.Parameters = wire2.Parameters{}
	for _, opt := range opts {
		if err := opt(msg); err != nil {
			return nil, err
		}
	}
	order, present, err := msg.Parameters.GroupOrder()
	if err != nil {
		return nil, err
	}
	if !present {
		order = GroupOrderAscending
	}

	stream, err := s.conn.OpenStreamSync(ctx)
	if err != nil {
		return nil, err
	}
	rs := newRequestStream(s.ctx, stream, s.qlog)
	fetch := &FetchStream{
		requestStream: rs,
		session:       s,
		requestID:     msg.RequestID,
		order:         order,
		established:   make(chan struct{}),
		responseDone:  make(chan struct{}),
		objects:       make(chan *FetchObject, fetchObjectBuffer),
	}

	// The response stream carries only a Request ID, and it may arrive before
	// FETCH_OK does, so the fetch has to be findable before the request goes
	// out at all.
	s.registerFetch(msg.RequestID, fetch)

	go func() {
		if err := fetch.run(); err != nil {
			s.failIfProtocolError(err)
		}
	}()

	if err := rs.write(msg); err != nil {
		rs.cancel(StreamErrorInternal, err)
		return nil, err
	}
	if err := fetch.awaitEstablished(ctx); err != nil {
		rs.cancel(StreamErrorCancelled, err)
		return nil, err
	}
	return fetch, nil
}

// newFetchRequest builds an incoming FETCH, resolving a Joining FETCH against
// the subscription it names. A range that cannot be resolved comes back as the
// REQUEST_ERROR code the draft prescribes.
func newFetchRequest(rs *requestStream, session fetchSession, msg *wire2.Fetch) (*FetchRequest, error) {
	order, present, err := msg.Parameters.GroupOrder()
	if err != nil {
		return nil, err
	}
	if !present {
		// Section 10.2.8: a FETCH that does not ask gets Ascending, unlike a
		// SUBSCRIBE, where the publisher's own preference applies.
		order = GroupOrderAscending
	}

	req := &FetchRequest{
		requestStream: rs,
		session:       session,
		requestID:     msg.RequestID,
		fetchType:     msg.FetchType,
		order:         order,
		params:        msg.Parameters,
	}
	if msg.Joining != nil {
		req.joiningRequestID = msg.Joining.JoiningRequestID
	}

	if msg.FetchType == wire2.FetchTypeStandalone {
		req.namespace = namespaceStrings(msg.Standalone.TrackNamespace)
		req.track = string(msg.Standalone.TrackName)
		req.start = msg.Standalone.StartLocation
		req.end = msg.Standalone.EndLocation
		return req, nil
	}
	return req, resolveJoiningFetch(req, session, msg)
}

// resolveJoiningFetch turns a Joining FETCH into a range, using the Joining
// Location of the subscription it names (Section 10.12.2.1).
func resolveJoiningFetch(req *FetchRequest, session fetchSession, msg *wire2.Fetch) error {
	sub, ok := session.subscriptionByRequestID(msg.Joining.JoiningRequestID)
	if !ok {
		return &fetchRangeError{
			code:   RequestErrorInvalidJoiningRequestID,
			reason: "no subscription with that request ID in this session",
		}
	}
	if !sub.Forward() {
		return &fetchRangeError{
			code:   RequestErrorInvalidRange,
			reason: "the joined subscription is not forwarding",
		}
	}
	joining, ok := sub.JoiningLocation()
	if !ok {
		return &fetchRangeError{
			code:   RequestErrorInvalidRange,
			reason: "nothing has been published on the joined track",
		}
	}

	req.namespace = sub.Namespace()
	req.track = sub.Track()
	// The last Object the fetch returns is the one at the Joining Location, so
	// the End Location is one past it -- which is what makes the fetch and the
	// subscription contiguous without overlapping.
	req.end = Location{Group: joining.Group, Object: joining.Object + 1}

	switch msg.FetchType {
	case wire2.FetchTypeRelativeJoining:
		if msg.Joining.JoiningStart > joining.Group {
			return &fetchRangeError{
				code:   RequestErrorInvalidRange,
				reason: "the joining start is before the beginning of the track",
			}
		}
		req.start = Location{Group: joining.Group - msg.Joining.JoiningStart}
	case wire2.FetchTypeAbsoluteJoining:
		if msg.Joining.JoiningStart > joining.Group {
			return &fetchRangeError{
				code:   RequestErrorInvalidRange,
				reason: "the joining start is after the joining location",
			}
		}
		req.start = Location{Group: msg.Joining.JoiningStart}
	}
	return nil
}

// fetchRangeError is a FETCH that cannot be served, carrying the REQUEST_ERROR
// code the draft names for it. It ends the request, not the session.
type fetchRangeError struct {
	code   RequestErrorCode
	reason string
}

func (e *fetchRangeError) Error() string { return fmt.Sprintf("%v: %v", e.code, e.reason) }

// handleFetchStream reads a FETCH response stream and delivers its records to
// the fetch that asked for them.
func (s *Session) handleFetchStream(stream ReceiveStream, reader *bufio.Reader) {
	requestID, err := wire2.ParseFetchHeader(reader)
	if err != nil {
		s.fail(err)
		return
	}
	fetch, ok := s.fetchByRequestID(requestID)
	if !ok {
		// A response to a fetch we have already given up on.
		stream.Stop(uint32(StreamErrorCancelled))
		return
	}

	objects := wire2.NewFetchReader(reader, fetch.GroupOrder())
	for {
		record, err := objects.Next()
		if err != nil {
			// A FIN completes the response: any gap up to the End Location
			// means those Objects do not exist. A reset does not.
			if errors.Is(err, io.EOF) {
				fetch.responseComplete(true)
				return
			}
			s.failIfProtocolError(err)
			fetch.responseComplete(false)
			return
		}
		s.qlog.logFetchObject(moqt.FetchObjectEventParsed, stream, record)
		preference := ObjectForwardingPreferenceSubgroup
		if record.Datagram {
			preference = ObjectForwardingPreferenceDatagram
		}
		if err := fetch.deliver(s.ctx, &FetchObject{
			Object: Object{
				GroupID:              record.GroupID,
				SubgroupID:           record.SubgroupID,
				ObjectID:             record.ObjectID,
				ForwardingPreference: preference,
				Priority:             record.Priority,
				Properties:           record.Properties,
				Payload:              record.Payload,
			},
			EndOfRange: record.EndOfRange,
		}); err != nil {
			return
		}
	}
}

// ErrFetchComplete is what ReadObject returns once the whole response has been
// delivered and drained. It is the expected ending of a FETCH, not a failure.
var ErrFetchComplete = errors.New("fetch response complete")

var (
	errFetchResponseClosed = errors.New("fetch response is closed")
	errFetchClosedLocally  = errors.New("fetch cancelled by the application")
)
