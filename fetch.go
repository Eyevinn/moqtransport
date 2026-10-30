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
// serialized, because they do not exist, their status is unknown, or (in
// draft-22) a relay stopped waiting for them.
type EndOfRange = wire2.EndOfRange

const (
	// EndOfRangeNonExistent says the covered Objects are known not to exist.
	EndOfRangeNonExistent = wire2.EndOfRangeNonExistent
	// EndOfRangeUnknown says the publisher cannot determine their status.
	EndOfRangeUnknown = wire2.EndOfRangeUnknown
	// EndOfRangeTimedOut says a relay gave up waiting for them upstream when
	// the request's Fill Timeout ran out. Draft-22 only; writing it on a
	// draft-18 session fails.
	EndOfRangeTimedOut = wire2.EndOfRangeTimedOut
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

	// priorityMapper is how this session reduces a MOQT priority to one the
	// transport understands.
	priorityMapper() PriorityMapper

	// subscriptionByRequestID finds a peer-initiated subscription, which is
	// what a Joining FETCH names.
	subscriptionByRequestID(requestID uint64) (*Subscription, bool)

	registerFetchReceiver(requestID uint64, r fetchReceiver)
	unregisterFetchReceiver(requestID uint64)
}

// fetchReceiver is where the records of a stream opened with FETCH_HEADER go:
// a FETCH this endpoint made, or a fill one of its subscriptions asked for.
// The header carries only a Request ID, so both are indexed by it.
type fetchReceiver interface {
	// awaitOrder returns the Group Order the stream's Group ID deltas run in,
	// waiting if it is not known yet.
	awaitOrder(ctx context.Context) (GroupOrder, error)
	// attach hands the receiver its stream, reporting false if it no longer
	// wants one.
	attach(stream ReceiveStream) bool
	deliver(ctx context.Context, o *FetchObject) error
	// responseComplete reports that the stream ended: with a FIN when
	// complete, otherwise by a reset or an error.
	responseComplete(complete bool)
}

// FetchRequest is an incoming FETCH.
//
// Its range is a location filter in both drafts. Draft-22 carries one in
// LOCATION_FILTER, and it may be relative to the Largest Object or open ended,
// which only the publisher can resolve; draft-18's explicit Start and End
// Locations arrive as the equivalent absolute filter. A draft-18 Joining FETCH
// names a subscription instead, and by the time a handler sees it the range
// has been resolved against that subscription, so Namespace, Track and Filter
// answer the same way for every kind.
type FetchRequest struct {
	*requestStream

	session          fetchSession
	requestID        uint64
	fetchType        wire2.FetchType
	joiningRequestID uint64
	namespace        []string
	track            string
	filter           LocationFilter
	order            GroupOrder
	params           wire2.Parameters

	mu       sync.Mutex
	answered bool
	resolved *LocationRange
}

// RequestID is the ID the subscriber assigned to this FETCH.
func (r *FetchRequest) RequestID() uint64 { return r.requestID }

// Namespace is the Track Namespace being fetched.
func (r *FetchRequest) Namespace() []string { return r.namespace }

// Track is the Track Name being fetched.
func (r *FetchRequest) Track() string { return r.track }

// Filter is the requested range as a location filter. The zero filter asks
// for the whole Track up to the Largest Object.
func (r *FetchRequest) Filter() LocationFilter { return r.filter }

// Range resolves the request against the publisher's Largest Object
// (hasLargest false when nothing has been published), the way draft-22
// Section 9.20.9 evaluates a FETCH: an open or too distant end stops at the
// Largest Object. Both ends are inclusive.
//
// It returns false for an empty range -- nothing published, or a start after
// the Largest Object -- which the draft answers with INVALID_RANGE. A
// resolved range also becomes the End Location Accept reports, unless
// WithFetchEndLocation says otherwise.
func (r *FetchRequest) Range(largest Location, hasLargest bool) (LocationRange, bool) {
	rng, ok := r.filter.FetchRange(largest, hasLargest)
	if ok {
		r.mu.Lock()
		r.resolved = &rng
		r.mu.Unlock()
	}
	return rng, ok
}

// GroupOrder is the order the response must be delivered in. A FETCH that did
// not ask is Ascending (Section 10.2.8).
func (r *FetchRequest) GroupOrder() GroupOrder { return r.order }

// Joining reports whether this is a draft-18 Joining FETCH, and the Request ID
// of the subscription it joins. The range has already been resolved against
// that subscription, so this is for logging rather than for working anything
// out.
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
// FETCH_OK's End Location is the last Location the response covers. Accept
// takes it from WithFetchEndLocation, or else from the range Range resolved,
// or else from the filter's own end. A filter with an open end that nobody
// resolved has no End Location yet, and the draft forbids FETCH_OK until it
// is known, so Accept fails.
//
// The response stream is separate from the request stream, so Objects cannot
// be written before the FETCH has been answered.
func (r *FetchRequest) Accept(opts ...FetchOkOption) (*FetchResponse, error) {
	var cfg fetchOkConfig
	for _, opt := range opts {
		opt(&cfg)
	}
	includeProperties, err := r.params.IncludeProperties()
	if err != nil {
		return nil, err
	}

	r.mu.Lock()
	if r.answered {
		r.mu.Unlock()
		return nil, errRequestAlreadyAnswered
	}
	end, known := cfg.endLocation, cfg.hasEndLocation
	if !known && r.resolved != nil {
		end, known = r.resolved.End, true
	}
	if !known {
		end, known = r.filter.End()
	}
	if !known {
		r.mu.Unlock()
		return nil, errFetchEndLocationUnknown
	}
	r.answered = true
	r.mu.Unlock()

	ok := &wire2.FetchOk{
		EndOfTrack:      cfg.endOfTrack,
		EndLocation:     end,
		Parameters:      wire2.Parameters{},
		TrackProperties: cfg.properties,
	}
	if r.version == wire2.Version18 {
		ok.EndLocation = wire2.V18EndLocation(end)
	}
	if !includeProperties {
		ok.TrackProperties = nil
	}

	stream, err := r.session.openUniStream(r.ctx)
	if err != nil {
		return nil, err
	}
	// A FETCH response is one stream carrying objects that may have different
	// publisher priorities (Section 7.1), so the stream can only be scheduled
	// on what the request fixes: the subscriber priority and the group order.
	applyPriority(stream, r.session.priorityMapper(), ObjectPriority{
		SubscriberPriority: r.Parameters().SubscriberPriority(),
		PublisherPriority:  wire2.DefaultPublisherPriority,
		GroupOrder:         r.order,
	})
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
		writer: wire2.NewFetchWriter(r.order, r.version),
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

var errFetchEndLocationUnknown = errors.New(
	"fetch has an open end: resolve it with Range or set WithFetchEndLocation before accepting")

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

	// onDone, when set, is called once the response has been closed or reset.
	onDone func()
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
	if f.done {
		f.mu.Unlock()
		return nil
	}
	f.done = true
	err := f.stream.Close()
	f.mu.Unlock()
	f.finished()
	return err
}

// Reset abandons the response, saying the range was not delivered in full.
func (f *FetchResponse) Reset(code StreamErrorCode) {
	f.mu.Lock()
	if f.done {
		f.mu.Unlock()
		return
	}
	f.done = true
	f.stream.Reset(uint32(code))
	f.mu.Unlock()
	f.finished()
}

func (f *FetchResponse) finished() {
	if f.onDone != nil {
		f.onDone()
	}
}

// FetchOkOption customises the FETCH_OK sent by Accept.
type FetchOkOption func(*fetchOkConfig)

type fetchOkConfig struct {
	endOfTrack     bool
	endLocation    Location
	hasEndLocation bool
	properties     KVPList
}

// WithEndOfTrack says the End Location is the end of the track, so no Object
// at or beyond it will ever exist.
func WithEndOfTrack() FetchOkOption {
	return func(c *fetchOkConfig) { c.endOfTrack = true }
}

// WithFetchEndLocation sets the End Location reported in FETCH_OK: the last
// Location the response covers, inclusive. A gap between the last Object and
// it means those Objects do not exist. On a draft-18 session the library
// encodes it the way that draft does, as the last Object plus one.
func WithFetchEndLocation(l Location) FetchOkOption {
	return func(c *fetchOkConfig) { c.endLocation, c.hasEndLocation = l, true }
}

// WithFetchTrackProperties attaches Track Properties to the response. A
// draft-22 subscriber can ask for none with INCLUDE_PROPERTIES, and then none
// are sent.
func WithFetchTrackProperties(properties KVPList) FetchOkOption {
	return func(c *fetchOkConfig) { c.properties = append(c.properties, properties...) }
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
	properties  KVPList
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

// EndLocation is the last Location the response covers, inclusive, from
// FETCH_OK. A gap between the last Object received and it means those Objects
// do not exist, provided the stream ended with a FIN. An Object of
// MaxObjectID covers the rest of its Group.
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

// TrackProperties returns the Track Properties from FETCH_OK, so that a
// proxying relay can forward them. Nil when the publisher sent none.
func (f *FetchStream) TrackProperties() KVPList {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.properties
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
//
// "Arrived" means established is closed, not that the answer has been
// recorded: between the two, a cancel here would end the request before
// awaitEstablished could see the answer, and Fetch would fail with
// ErrFetchComplete. Whichever of this and finishIfComplete runs second does
// the cancel.
func (f *FetchStream) responseComplete(complete bool) {
	f.mu.Lock()
	f.dataDone = complete
	f.mu.Unlock()

	f.responseOnce.Do(func() { close(f.responseDone) })
	select {
	case <-f.established:
		f.cancel(StreamErrorCancelled, ErrFetchComplete)
	default:
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

// awaitOrder implements fetchReceiver: a FETCH's order is fixed when it is
// sent.
func (f *FetchStream) awaitOrder(context.Context) (GroupOrder, error) { return f.order, nil }

// attach implements fetchReceiver.
func (f *FetchStream) attach(ReceiveStream) bool { return true }

// deliver implements fetchReceiver.
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
	f.session.unregisterFetchReceiver(f.requestID)
	// A request stream that ends without the data stream having finished means
	// the response will never complete.
	f.responseOnce.Do(func() { close(f.responseDone) })
	return err
}

func (f *FetchStream) handleMessage(msg wire2.ControlMessage) error {
	switch m := msg.(type) {
	case *wire2.FetchOk:
		if err := wire2.ValidateTrackProperties(m.TrackProperties); err != nil {
			return err
		}
		f.mu.Lock()
		if f.answered {
			f.mu.Unlock()
			return errDuplicateResponse
		}
		f.answered = true
		f.endLocation = m.EndLocation
		if f.version == wire2.Version18 {
			f.endLocation = wire2.InclusiveFromV18End(m.EndLocation)
		}
		f.endOfTrack = m.EndOfTrack
		f.properties = m.TrackProperties
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
	if err := awaitAnswer(ctx, f.established, f.ctx); err != nil {
		return err
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

// Fetch asks for the Objects of a track that already exist and that filter
// admits. The zero filter asks for the whole Track up to the publisher's
// Largest Object; AbsoluteRange asks for an explicit span, both ends
// inclusive.
//
// On a draft-18 session the range has to be explicit, so a relative start or
// the Next Object cannot be sent and fail here; an open end becomes "through
// the last Group there could be", which the publisher clamps to what it has.
func (s *Session) Fetch(ctx context.Context, namespace []string, track string, filter LocationFilter, opts ...FetchOption) (*FetchStream, error) {
	msg := &wire2.Fetch{
		FetchType:      wire2.FetchTypeStandalone,
		TrackNamespace: namespaceFields(namespace),
		TrackName:      []byte(track),
		Parameters:     wire2.Parameters{},
	}
	if s.version == wire2.Version18 {
		start, end, err := wire2.V18FetchRange(filter)
		if err != nil {
			return nil, err
		}
		msg.StartLocation, msg.EndLocation = start, end
	} else if filter.Type != LocationFilterNone {
		msg.Parameters = append(msg.Parameters, wire2.LocationFilterParameter(filter))
	}
	return s.fetch(ctx, msg, opts...)
}

// FetchRelative asks for the Objects immediately before a subscription's
// starting point, going back groupsBack Groups, with a draft-18 Joining
// FETCH.
//
// The publisher works the range out from the subscription, so what the fetch
// retrieves and what the subscription delivers are contiguous and do not
// overlap. Draft-22 has no Joining FETCH -- a subscription asks for its fill
// itself -- so on a draft-22 session this returns ErrNotInThisDraft.
func (s *Session) FetchRelative(ctx context.Context, subscription *RemoteTrack, groupsBack uint64, opts ...FetchOption) (*FetchStream, error) {
	if s.version != wire2.Version18 {
		return nil, errJoiningFetchNotInDraft
	}
	return s.fetch(ctx, &wire2.Fetch{
		FetchType: wire2.FetchTypeRelativeJoining,
		Joining: &wire2.JoiningFetch{
			JoiningRequestID: subscription.RequestID(),
			JoiningStart:     groupsBack,
		},
	}, opts...)
}

// FetchAbsolute asks for the Objects from an explicit Group up to a
// subscription's starting point, with a draft-18 Joining FETCH. Like
// FetchRelative it returns ErrNotInThisDraft on a draft-22 session.
func (s *Session) FetchAbsolute(ctx context.Context, subscription *RemoteTrack, startGroup uint64, opts ...FetchOption) (*FetchStream, error) {
	if s.version != wire2.Version18 {
		return nil, errJoiningFetchNotInDraft
	}
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
	if msg.Parameters == nil {
		msg.Parameters = wire2.Parameters{}
	}
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
	if err := checkRequest(s.version, msg); err != nil {
		return nil, err
	}

	stream, err := s.conn.OpenStreamSync(ctx)
	if err != nil {
		return nil, err
	}
	rs := newRequestStream(s.ctx, stream, s.qlog, s.version)
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
	s.registerFetchReceiver(msg.RequestID, fetch)

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
// the subscription it names. A range that cannot be resolved, or that uses
// Range Filters, comes back as the REQUEST_ERROR code the draft prescribes.
func newFetchRequest(rs *requestStream, session fetchSession, msg *wire2.Fetch) (*FetchRequest, error) {
	rs.scope.opened(msg.Type(), false)
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

	if msg.Parameters.HasRangeFilter() {
		return req, &fetchRangeError{code: RequestErrorInvalidFilter, reason: "range filters are not supported"}
	}

	if msg.FetchType != wire2.FetchTypeStandalone {
		return req, resolveJoiningFetch(req, session, msg)
	}
	req.namespace = namespaceStrings(msg.TrackNamespace)
	req.track = string(msg.TrackName)
	if rs.version != wire2.Version18 {
		req.filter, _ = msg.Parameters.LocationFilter()
		return req, nil
	}
	filter, err := wire2.FetchFilterFromV18(msg.StartLocation, msg.EndLocation)
	if err != nil {
		return req, &fetchRangeError{code: RequestErrorInvalidRange, reason: err.Error()}
	}
	req.filter = filter
	return req, nil
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

	var start Location
	switch msg.FetchType {
	case wire2.FetchTypeRelativeJoining:
		if msg.Joining.JoiningStart > joining.Group {
			return &fetchRangeError{
				code:   RequestErrorInvalidRange,
				reason: "the joining start is before the beginning of the track",
			}
		}
		start = Location{Group: joining.Group - msg.Joining.JoiningStart}
	case wire2.FetchTypeAbsoluteJoining:
		if msg.Joining.JoiningStart > joining.Group {
			return &fetchRangeError{
				code:   RequestErrorInvalidRange,
				reason: "the joining start is after the joining location",
			}
		}
		start = Location{Group: msg.Joining.JoiningStart}
	}
	// The last Object the fetch returns is the one at the Joining Location,
	// which is what makes the fetch and the subscription contiguous without
	// overlapping.
	req.filter = AbsoluteRange(start, joining)
	return nil
}

// fetchRangeError is a FETCH that cannot be served, carrying the REQUEST_ERROR
// code the draft names for it. It ends the request, not the session.
type fetchRangeError struct {
	code   RequestErrorCode
	reason string
}

func (e *fetchRangeError) Error() string { return fmt.Sprintf("%v: %v", e.code, e.reason) }

// handleFetchStream reads a stream opened with FETCH_HEADER -- a FETCH
// response, or in draft-22 a fill -- and delivers its records to the request
// that asked for them.
func (s *Session) handleFetchStream(stream ReceiveStream, reader *bufio.Reader) {
	requestID, err := wire2.ParseFetchHeader(reader)
	if err != nil {
		s.headerFailed(stream, err)
		return
	}
	fetch, ok := s.fetchReceiverByRequestID(requestID)
	if !ok || !fetch.attach(stream) {
		// A response to a fetch we have already given up on, or a fill we
		// had concluded was not coming.
		stream.Stop(uint32(StreamErrorCancelled))
		return
	}
	order, err := fetch.awaitOrder(s.ctx)
	if err != nil {
		stream.Stop(uint32(StreamErrorCancelled))
		fetch.responseComplete(false)
		return
	}

	objects := wire2.NewFetchReader(reader, order, s.version)
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
	// errJoiningFetchNotInDraft refuses a Joining FETCH on a draft-22
	// session, where the subscription asks for its own fill instead.
	errJoiningFetchNotInDraft = fmt.Errorf("%w: joining FETCH, replaced by fill in draft-20", ErrNotInThisDraft)

	errFetchResponseClosed = errors.New("fetch response is closed")
	errFetchClosedLocally  = errors.New("fetch cancelled by the application")
)
