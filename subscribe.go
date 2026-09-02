package moqtransport

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/Eyevinn/moqtransport/internal/wire2"
	"github.com/mengelbart/qlog/moqt"
)

// SubscribeHandler answers SUBSCRIBE requests from the peer.
//
// HandleSubscribe runs on a goroutine of its own and may keep the request for
// as long as it likes: the request's Context is what says when the subscriber
// has gone. A nil handler on the session rejects every SUBSCRIBE with
// NOT_SUPPORTED, which is a legitimate answer and better than silence.
type SubscribeHandler interface {
	HandleSubscribe(*SubscribeRequest)
}

// SubscribeHandlerFunc adapts a function to [SubscribeHandler].
type SubscribeHandlerFunc func(*SubscribeRequest)

func (f SubscribeHandlerFunc) HandleSubscribe(r *SubscribeRequest) { f(r) }

// publisherSession is what a subscription needs from the session that carries
// it. It is an interface so a subscription can be built and tested without a
// whole session, and so the coupling stays visible: three calls, all of them
// about getting Objects onto the connection.
type publisherSession interface {
	// trackAlias returns the alias this session uses for a track, allocating
	// one on first use. The same track always gets the same alias, because
	// Section 11.1 forbids one alias naming two tracks at once but says
	// nothing against two concurrent subscriptions to one track sharing it.
	trackAlias(namespace []string, track string) uint64

	// openUniStream opens a unidirectional stream for a subgroup.
	openUniStream(ctx context.Context) (SendStream, error)

	// sendDatagram sends one datagram, which may be dropped if it exceeds the
	// session's maximum datagram size.
	sendDatagram([]byte) error

	// registerSubscription indexes an accepted subscription by the Request ID
	// the peer gave it, which is how a Joining FETCH names the subscription it
	// joins (Section 10.12.2).
	registerSubscription(requestID uint64, sub *Subscription)

	// unregisterSubscription drops that index entry when the subscription ends.
	unregisterSubscription(requestID uint64)

	// priorityMapper is how this session reduces a MOQT priority to one the
	// transport understands.
	priorityMapper() PriorityMapper
}

// SubscribeRequest is an incoming SUBSCRIBE, and the bidirectional stream that
// carries it (draft-ietf-moq-transport-18, Section 10.7).
type SubscribeRequest struct {
	*requestStream

	session   publisherSession
	requestID uint64
	namespace []string
	track     string

	mu           sync.Mutex
	params       wire2.Parameters
	subscription *Subscription
	answered     bool
}

func newSubscribeRequest(rs *requestStream, session publisherSession, msg *wire2.Subscribe) *SubscribeRequest {
	namespace := make([]string, len(msg.TrackNamespace))
	for i, field := range msg.TrackNamespace {
		namespace[i] = string(field)
	}
	return &SubscribeRequest{
		requestStream: rs,
		session:       session,
		requestID:     msg.RequestID,
		namespace:     namespace,
		track:         string(msg.TrackName),
		params:        msg.Parameters,
	}
}

// RequestID is the ID the subscriber assigned to this SUBSCRIBE.
//
// Responses do not carry it -- the stream identifies them -- but it stays
// meaningful because a Joining FETCH names the subscription it joins by
// Request ID.
func (r *SubscribeRequest) RequestID() uint64 { return r.requestID }

// Namespace is the Track Namespace the subscriber asked for.
func (r *SubscribeRequest) Namespace() []string { return r.namespace }

// Track is the Track Name the subscriber asked for.
func (r *SubscribeRequest) Track() string { return r.track }

// Parameters returns the Message Parameters the subscriber sent, including any
// this package does not interpret.
func (r *SubscribeRequest) Parameters() Parameters {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.params
}

// SubscriberPriority is the priority the subscriber asked for, or 128 if it
// said nothing (Section 10.2.7).
func (r *SubscribeRequest) SubscriberPriority() uint8 {
	return r.Parameters().SubscriberPriority()
}

// GroupOrder is the order the subscriber asked for, and whether it asked at
// all. When it did not, the publisher's own preference for the Track applies.
func (r *SubscribeRequest) GroupOrder() (GroupOrder, bool, error) {
	return r.Parameters().GroupOrder()
}

// Forward reports whether the subscriber wants Objects delivered now. A
// subscription with Forward false is established but idle, and a
// REQUEST_UPDATE can turn it on later.
func (r *SubscribeRequest) Forward() (bool, error) {
	return r.Parameters().Forward()
}

// Filter returns the Subscription Filter, and whether one was sent. An absent
// filter means the subscription is unfiltered.
func (r *SubscribeRequest) Filter() (SubscriptionFilter, bool, error) {
	return r.Parameters().Filter()
}

// RendezvousTimeout is how long the subscriber is willing to wait for a
// publisher of the Track when there is none yet, and whether it said so
// (Section 10.2.6). A relay honoring it holds the request up to that long --
// it may choose less -- and answers TIMEOUT when the wait runs out; without
// the parameter the subscriber wants an immediate DOES_NOT_EXIST instead.
func (r *SubscribeRequest) RendezvousTimeout() (time.Duration, bool) {
	return r.Parameters().RendezvousTimeout()
}

// Accept answers with SUBSCRIBE_OK and returns the publishing side.
//
// Objects cannot be sent before it returns, which is the point of separating
// the two: with one object serving as both response writer and publisher, it
// is possible to write Objects on a subscription the peer has not been told
// about.
func (r *SubscribeRequest) Accept(opts ...SubscribeOkOption) (*Subscription, error) {
	r.mu.Lock()
	if r.answered {
		r.mu.Unlock()
		return nil, errRequestAlreadyAnswered
	}
	params := r.params
	r.mu.Unlock()

	priority := params.SubscriberPriority()
	forward, err := params.Forward()
	if err != nil {
		return nil, err
	}
	groupOrder, present, err := params.GroupOrder()
	if err != nil {
		return nil, err
	}
	if !present {
		// Section 10.2.8: omitted from a SUBSCRIBE, the publisher's own
		// preference for the Track applies. Nothing here tracks one, so it is
		// Ascending, which is also the DEFAULT_PUBLISHER_GROUP_ORDER default.
		groupOrder = GroupOrderAscending
	}
	filter, hasFilter, err := params.Filter()
	if err != nil {
		return nil, err
	}

	ok := &wire2.SubscribeOk{
		TrackAlias: r.session.trackAlias(r.namespace, r.track),
		Parameters: wire2.Parameters{},
	}
	for _, opt := range opts {
		opt(ok)
	}

	sub := &Subscription{
		requestStream: r.requestStream,
		session:       r.session,
		trackAlias:    ok.TrackAlias,
		namespace:     r.namespace,
		track:         r.track,
		requestID:     r.requestID,
		priority:      priority,
		groupOrder:    groupOrder,
		forward:       forward,
		filter:        filter,
		hasFilter:     hasFilter,
	}
	sub.joiningLocation, sub.hasJoiningLocation = ok.Parameters.LargestObject()

	r.mu.Lock()
	if r.answered {
		r.mu.Unlock()
		return nil, errRequestAlreadyAnswered
	}
	r.answered = true
	r.subscription = sub
	r.mu.Unlock()

	r.session.registerSubscription(r.requestID, sub)
	if err := r.write(ok); err != nil {
		return nil, err
	}
	return sub, nil
}

// Reject answers with REQUEST_ERROR and finishes the stream, which Section
// 3.3.2 asks for when a request is turned down without any application
// processing.
func (r *SubscribeRequest) Reject(code RequestErrorCode, reason string) error {
	r.mu.Lock()
	if r.answered {
		r.mu.Unlock()
		return errRequestAlreadyAnswered
	}
	r.answered = true
	r.mu.Unlock()

	err := r.finish(&wire2.RequestError{
		ErrorCode:   uint64(code),
		ErrorReason: reason,
	})
	r.cancelCtx(fmt.Errorf("rejected the subscription: %v", code))
	return err
}

// serve runs the request to completion: it hands the request to the handler on
// a goroutine of its own, then reads the stream until it ends.
//
// The handler gets its own goroutine because it may keep the request for the
// subscription's whole life, while this goroutine has to stay free to notice
// the peer resetting the stream -- which in draft-18 is the only signal that
// the subscriber has gone.
//
// A nil handler rejects with NOT_SUPPORTED. That is a legitimate answer and
// better than leaving the peer waiting on a request nothing will ever read.
func (r *SubscribeRequest) serve(h SubscribeHandler) error {
	if h == nil {
		return r.Reject(RequestErrorNotSupported, "subscribe is not supported")
	}
	go h.HandleSubscribe(r)
	err := r.run(r.handleMessage)
	r.streamEnded()
	return err
}

// streamEnded runs on the reader goroutine once the stream is over, and is
// what releases anyone ranging over the subscription's updates.
func (r *SubscribeRequest) streamEnded() {
	r.mu.Lock()
	sub := r.subscription
	r.mu.Unlock()
	if sub != nil {
		r.session.unregisterSubscription(r.requestID)
		sub.streamEnded()
	}
}

// handleMessage is the request stream's read callback. Only REQUEST_UPDATE is
// expected after the SUBSCRIBE itself; anything else on a subscribe stream
// from the subscriber's side is a protocol violation.
func (r *SubscribeRequest) handleMessage(msg wire2.ControlMessage) error {
	update, ok := msg.(*wire2.RequestUpdate)
	if !ok {
		return errUnexpectedMessageOnRequestStream
	}

	r.mu.Lock()
	sub := r.subscription
	if sub == nil {
		// The subscriber updated its request before we answered it. There is
		// nothing to apply it to yet, so it folds into the parameters the
		// handler will read.
		r.params = mergeParameters(r.params, update.Parameters)
		r.mu.Unlock()
		return r.write(&wire2.RequestOk{Parameters: wire2.Parameters{}})
	}
	r.mu.Unlock()

	return sub.applyUpdate(update)
}

// mergeParameters overlays update onto base. Section 10.9: a parameter absent
// from a REQUEST_UPDATE keeps its value, and there is no way to remove one.
func mergeParameters(base, update wire2.Parameters) wire2.Parameters {
	merged := make(wire2.Parameters, 0, len(base)+len(update))
	for _, p := range base {
		if _, replaced := update.Get(p.Type); !replaced {
			merged = append(merged, p)
		}
	}
	return append(merged, update...)
}

// A Subscription is an accepted SUBSCRIBE: the publishing side of a request
// this endpoint answered with SUBSCRIBE_OK.
type Subscription struct {
	*requestStream

	session    publisherSession
	trackAlias uint64
	namespace  []string
	track      string
	requestID  uint64

	// joiningLocation is the Largest Location reported in SUBSCRIBE_OK. A
	// Joining FETCH ends there, so that what it retrieves and what the
	// subscription delivers are contiguous and do not overlap (Section 5.1).
	joiningLocation    Location
	hasJoiningLocation bool

	mu          sync.Mutex
	priority    uint8
	groupOrder  GroupOrder
	forward     bool
	filter      wire2.SubscriptionFilter
	hasFilter   bool
	streamCount uint64
	closed      bool

	// updates is written and closed only by the request stream's reader
	// goroutine, which is what makes closing it safe: there is never a send in
	// flight when it closes. Other goroutines only read the field, under mu.
	updates       chan SubscriptionUpdate
	updatesClosed bool
	ended         bool
}

// subscriptionUpdateBuffer is how many updates are queued before a consumer
// that has stopped reading blocks its own subscription's stream. Ordinary
// bursts never reach it.
const subscriptionUpdateBuffer = 16

// SubscriptionUpdate is a REQUEST_UPDATE that arrived on this subscription's
// stream, after it has been applied.
//
// The library applies an update to the Subscription before delivering the
// notification, and answers the peer itself, because Section 10.9 requires
// exactly one REQUEST_OK or REQUEST_ERROR per update and a failed update also
// ends the subscription. A consumer that falls behind therefore loses
// timeliness, never state: the accessors on Subscription are always current.
type SubscriptionUpdate struct {
	// SubscriberPriority, Forward and Filter are the values now in effect,
	// not only the ones the update changed.
	SubscriberPriority uint8
	Forward            bool
	Filter             SubscriptionFilter
	HasFilter          bool

	// Parameters is the update's own parameter block, for anything this
	// package does not interpret.
	Parameters Parameters
}

// TrackAlias returns the alias Objects on this subscription are sent under.
func (s *Subscription) TrackAlias() uint64 { return s.trackAlias }

// Namespace is the Track Namespace this subscription is for.
func (s *Subscription) Namespace() []string { return s.namespace }

// Track is the Track Name this subscription is for.
func (s *Subscription) Track() string { return s.track }

// RequestID is the ID the subscriber gave this subscription. A Joining FETCH
// names the subscription it joins by it.
func (s *Subscription) RequestID() uint64 { return s.requestID }

// JoiningLocation is the Largest Location reported in SUBSCRIBE_OK, and
// whether one was reported. A Joining FETCH ends there.
func (s *Subscription) JoiningLocation() (Location, bool) {
	return s.joiningLocation, s.hasJoiningLocation
}

// SubscriberPriority is the priority currently in effect.
func (s *Subscription) SubscriberPriority() uint8 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.priority
}

// Forward reports whether the subscriber currently wants Objects delivered.
func (s *Subscription) Forward() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.forward
}

// GroupOrder is the order Groups are delivered in for this subscription. It
// cannot change once the subscription is established.
func (s *Subscription) GroupOrder() GroupOrder { return s.groupOrder }

// Filter returns the Subscription Filter currently in effect, and whether
// there is one.
func (s *Subscription) Filter() (SubscriptionFilter, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.filter, s.hasFilter
}

// Updates returns the channel REQUEST_UPDATE notifications arrive on. The
// channel is closed when the subscription ends, so a range over it terminates
// on its own.
//
// Call it once, right after Accept and before publishing. The first call is
// what starts queueing: a subscription whose Updates is never called allocates
// no channel and queues nothing, which keeps the common case -- an application
// that does not care -- safe by default.
//
// Drain it, or do not ask for it. A consumer that stops reading eventually
// blocks this subscription's stream reader, and with it the notice that the
// subscriber has gone.
func (s *Subscription) Updates() <-chan SubscriptionUpdate {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.updates == nil {
		s.updates = make(chan SubscriptionUpdate, subscriptionUpdateBuffer)
		if s.ended {
			// Asking after the stream is over still gets a channel, so that a
			// range over it terminates rather than blocking forever.
			close(s.updates)
			s.updatesClosed = true
		}
	}
	return s.updates
}

// streamEnded closes the updates channel. It runs on the reader goroutine
// after the read loop has finished, so no send can be in flight.
func (s *Subscription) streamEnded() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ended = true
	if s.updates != nil && !s.updatesClosed {
		close(s.updates)
		s.updatesClosed = true
	}
}

// OpenSubgroup opens a stream for a Subgroup of this subscription. Objects
// written to it must have increasing Object IDs.
func (s *Subscription) OpenSubgroup(groupID, subgroupID uint64, priority uint8, opts ...SubgroupOption) (*Subgroup, error) {
	header := &wire2.SubgroupHeader{
		TrackAlias:     s.trackAlias,
		GroupID:        groupID,
		SubgroupIDMode: wire2.SubgroupIDExplicit,
		SubgroupID:     subgroupID,
		Priority:       priority,
	}
	for _, opt := range opts {
		opt(header)
	}

	stream, err := s.session.openUniStream(s.ctx)
	if err != nil {
		return nil, err
	}
	// Section 7.2 orders this stream against every other schedulable object on
	// the session. Hand the transport what it can use of that ordering; a
	// transport that cannot schedule ignores it.
	applyPriority(stream, s.session.priorityMapper(), ObjectPriority{
		SubscriberPriority: s.SubscriberPriority(),
		PublisherPriority:  header.Priority,
		GroupOrder:         s.groupOrder,
		GroupID:            groupID,
		SubgroupID:         subgroupID,
	})
	sg, err := newSubgroup(stream, header, s.qlog)
	if err != nil {
		stream.Reset(uint32(StreamErrorInternal))
		return nil, err
	}

	// Every stream opened counts towards PUBLISH_DONE's Stream Count,
	// including one that ends up carrying no Objects.
	s.mu.Lock()
	s.streamCount++
	s.mu.Unlock()
	return sg, nil
}

// SendDatagram sends one Object as a datagram. The Object's Subgroup ID is
// ignored: a datagram Object has no Subgroup.
func (s *Subscription) SendDatagram(o Object) error {
	datagram := &wire2.ObjectDatagram{
		TrackAlias: s.trackAlias,
		GroupID:    o.GroupID,
		ObjectID:   o.ObjectID,
		Priority:   o.Priority,
		Properties: o.Properties,
		Status:     o.Status,
		Payload:    o.Payload,
	}
	buf, err := wire2.AppendObjectDatagram(nil, datagram)
	if err != nil {
		return err
	}
	if err := s.session.sendDatagram(buf); err != nil {
		return err
	}
	s.qlog.logDatagram(moqt.ObjectDatagramEventCreated, datagram)
	return nil
}

// Close ends the subscription gracefully: PUBLISH_DONE followed by a FIN, as
// Section 10.11 asks for.
//
// It must not be called until every stream this subscription will open has
// been closed and there are no datagrams left to send, because PUBLISH_DONE's
// Stream Count is the subscriber's signal that it has seen everything.
func (s *Subscription) Close(code PublishDoneCode, reason string) error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	streams := s.streamCount
	s.mu.Unlock()

	err := s.finish(&wire2.PublishDone{
		StatusCode:  uint64(code),
		StreamCount: streams,
		ErrorReason: reason,
	})

	// Our half is now FINed, so this sends STOP_SENDING and nothing else. The
	// draft lets the publisher destroy subscription state as soon as
	// PUBLISH_DONE is sent, and Section 3.3.2 asks that whatever direction is
	// still open be terminated rather than left hanging. It is also what ends
	// the reader goroutine, and with it the updates channel.
	s.cancel(StreamErrorCancelled, fmt.Errorf("subscription ended: %v", code))
	return err
}

// applyUpdate applies a REQUEST_UPDATE and answers it.
//
// A failed update does not merely fail: Section 10.9.1 requires the publisher
// to end the subscription with PUBLISH_DONE(UPDATE_FAILED) as well, so an
// unparseable filter takes the whole subscription with it.
func (s *Subscription) applyUpdate(update *wire2.RequestUpdate) error {
	notification, err := s.mergeUpdate(update)
	if err != nil {
		writeErr := s.write(&wire2.RequestError{
			ErrorCode:   uint64(RequestErrorInternal),
			ErrorReason: err.Error(),
		})
		if closeErr := s.Close(PublishDoneUpdateFailed, err.Error()); writeErr == nil {
			writeErr = closeErr
		}
		return errors.Join(err, writeErr)
	}

	if err := s.write(&wire2.RequestOk{Parameters: wire2.Parameters{}}); err != nil {
		return err
	}
	return s.notify(notification)
}

// mergeUpdate overlays an update's parameters on the current state. A
// parameter the update does not mention keeps its value: Section 10.9 gives no
// way to remove one.
func (s *Subscription) mergeUpdate(update *wire2.RequestUpdate) (SubscriptionUpdate, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	priority := s.priority
	if p, ok := update.Parameters.Get(wire2.ParamSubscriberPriority); ok {
		priority = uint8(p.Number)
	}
	forward := s.forward
	if _, ok := update.Parameters.Get(wire2.ParamForward); ok {
		v, err := update.Parameters.Forward()
		if err != nil {
			return SubscriptionUpdate{}, err
		}
		forward = v
	}
	filter, hasFilter := s.filter, s.hasFilter
	if f, present, err := update.Parameters.Filter(); err != nil {
		return SubscriptionUpdate{}, err
	} else if present {
		filter, hasFilter = f, true
	}

	s.priority = priority
	s.forward = forward
	s.filter = filter
	s.hasFilter = hasFilter

	return SubscriptionUpdate{
		SubscriberPriority: priority,
		Forward:            forward,
		Filter:             filter,
		HasFilter:          hasFilter,
		Parameters:         update.Parameters,
	}, nil
}

// notify delivers an update to a consumer, if there is one. The send is
// selected against the request's context so that a stalled consumer blocks
// this one subscription's reader and nothing else, and stops blocking when the
// subscription ends. Nothing is dropped silently.
func (s *Subscription) notify(u SubscriptionUpdate) error {
	s.mu.Lock()
	updates := s.updates
	closed := s.updatesClosed
	s.mu.Unlock()
	if updates == nil || closed {
		return nil
	}

	select {
	case updates <- u:
		return nil
	case <-s.ctx.Done():
		return context.Cause(s.ctx)
	}
}

// SubscribeOkOption customises the SUBSCRIBE_OK sent by Accept.
type SubscribeOkOption func(*wire2.SubscribeOk)

// WithLargestObject reports the largest Location the publisher has for the
// Track. Section 10.2.11 requires it whenever anything has been published, and
// it is what a subscriber using a relative filter needs in order to know where
// its subscription actually starts.
func WithLargestObject(l Location) SubscribeOkOption {
	return func(ok *wire2.SubscribeOk) {
		ok.Parameters = append(ok.Parameters, wire2.LocationParameter(wire2.ParamLargestObject, l))
	}
}

// WithExpires says the publisher will end the subscription after d
// milliseconds. It is advisory; the subscriber can ask for an extension with a
// REQUEST_UPDATE.
func WithExpires(milliseconds uint64) SubscribeOkOption {
	return func(ok *wire2.SubscribeOk) {
		ok.Parameters = append(ok.Parameters, wire2.VarintParameter(wire2.ParamExpires, milliseconds))
	}
}

// WithTrackProperties attaches Track Properties to the response. They describe
// the Track rather than the subscription, are visible to relays, and are
// forwarded and cached with it.
func WithTrackProperties(properties KVPList) SubscribeOkOption {
	return func(ok *wire2.SubscribeOk) {
		ok.TrackProperties = append(ok.TrackProperties, properties...)
	}
}

// SubgroupOption customises a subgroup stream's header.
type SubgroupOption func(*wire2.SubgroupHeader)

// WithObjectProperties opens the subgroup for Object Properties. The
// PROPERTIES bit is in the header and covers every Object on the stream, so
// this has to be decided when the subgroup is opened, not per Object.
func WithObjectProperties() SubgroupOption {
	return func(h *wire2.SubgroupHeader) { h.HasProperties = true }
}

// WithEndOfGroup says this subgroup carries the largest Object in its Group,
// so a FIN on the stream tells the subscriber the Group is complete.
func WithEndOfGroup() SubgroupOption {
	return func(h *wire2.SubgroupHeader) { h.EndOfGroup = true }
}

// WithFirstObject says the first Object on the stream is the first the
// original publisher published in the subgroup.
func WithFirstObject() SubgroupOption {
	return func(h *wire2.SubgroupHeader) { h.FirstObject = true }
}

// WithSubscriptionPriority leaves the Publisher Priority off the wire, so the
// Objects inherit the priority the subscription was established with. It
// overrides the priority passed to OpenSubgroup.
func WithSubscriptionPriority() SubgroupOption {
	return func(h *wire2.SubgroupHeader) { h.DefaultPriority = true }
}

// WithSubgroupIDFromFirstObject leaves the Subgroup ID off the wire, taking it
// from the first Object's ID instead. It overrides the ID passed to
// OpenSubgroup.
func WithSubgroupIDFromFirstObject() SubgroupOption {
	return func(h *wire2.SubgroupHeader) {
		h.SubgroupIDMode = wire2.SubgroupIDFirstObject
		h.SubgroupID = 0
	}
}

var (
	errRequestAlreadyAnswered = errors.New("request has already been accepted or rejected")

	errUnexpectedMessageOnRequestStream = ProtocolError{
		code:    SessionErrorProtocolViolation,
		message: "unexpected message type on an established request stream",
	}
)
