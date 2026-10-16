package moqtransport

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"sync"
	"time"

	"github.com/Eyevinn/moqtransport/internal/wire2"
)

// subscriberSession is what a subscription this endpoint initiated needs from
// the session that carries it.
type subscriberSession interface {
	// registerTrackAlias records the alias the publisher assigned, so that
	// incoming subgroup streams and datagrams can be routed to t.
	registerTrackAlias(alias uint64, t *RemoteTrack) error

	// unregisterTrackAlias drops that registration when the subscription ends.
	unregisterTrackAlias(alias uint64, t *RemoteTrack)

	// nextRequestID allocates a Request ID. A REQUEST_UPDATE consumes one of
	// its own (draft-18 Section 10.1, draft-22 Section 6.4.2.1); it does not
	// reuse the ID of the request it updates.
	nextRequestID() uint64

	// peerMaxRequestUpdates is how many unanswered REQUEST_UPDATEs the peer
	// accepts on one stream, 0 for no limit.
	peerMaxRequestUpdates() uint64

	// registerFetchReceiver and unregisterFetchReceiver route a fill stream,
	// which names only the Request ID that asked for it, to its FillStream.
	registerFetchReceiver(requestID uint64, r fetchReceiver)
	unregisterFetchReceiver(requestID uint64)
}

// PublishDone is the publisher's report that a subscription has ended
// (draft-ietf-moq-transport-18, Section 10.11).
type PublishDone struct {
	Code   PublishDoneCode
	Reason string

	// StreamCount is how many data streams the publisher opened for this
	// subscription, including any that carried no Objects. It is the
	// subscriber's signal that it has seen everything: PUBLISH_DONE travels on
	// a control stream and usually overtakes late-opening data streams.
	//
	// A publisher that cannot count exactly sets 2^62-1 in draft-18 and
	// 2^64-1 in draft-22. In draft-22 the count includes fill fetch streams.
	StreamCount uint64
}

// RemoteTrack is a subscription this endpoint initiated: the SUBSCRIBE it sent
// and the Objects that come back.
type RemoteTrack struct {
	*requestStream

	session   subscriberSession
	requestID uint64
	namespace []string
	track     string

	// established is closed once the publisher has answered, with either
	// SUBSCRIBE_OK or REQUEST_ERROR.
	established   chan struct{}
	establishOnce sync.Once

	// objects is never closed. Its senders are the reader goroutines of every
	// subgroup stream and the session's datagram loop, so there is no single
	// goroutine that could close it safely; ReadObject uses the delivery
	// context to know when no more will come.
	objects chan *Object

	// delivery is the lifetime of Object delivery, distinct from the request
	// stream's: PUBLISH_DONE travels on the request stream and usually
	// overtakes late data streams, so after a clean end delivery stays open
	// until the announced StreamCount of data streams has ended (or a grace
	// period gives up on them). Its parent is the session's context, so a
	// dying session still ends it.
	deliveryCtx    context.Context
	cancelDelivery context.CancelCauseFunc

	// streamEnded is signalled (never blocking) each time a data stream of
	// this subscription ends, waking the StreamCount wait.
	streamEnded chan struct{}

	mu           sync.Mutex
	trackAlias   uint64
	params       wire2.Parameters
	properties   KVPList
	answerErr    error
	publishDone  *PublishDone
	endedStreams uint64

	// largest, paused and filter are the subscription's state as the
	// publisher last reported it: SUBSCRIBE_OK, then REQUEST_UPDATE_OK and
	// PUBLISH_STATE_NOTIFY (draft-22), with paused and filter also following
	// what we asked for ourselves.
	largest    Location
	hasLargest bool
	paused     bool
	filter     LocationFilter
	hasFilter  bool

	// groupOrder is what the SUBSCRIBE asked for, if anything. A fill that
	// does not say inherits it.
	groupOrder    GroupOrder
	hasGroupOrder bool

	// pendingUpdates holds a slot per REQUEST_UPDATE not yet answered, in the
	// order they were sent, which is the order the answers come in. A slot
	// holds the fill the update asked for, if it did. A draft-22 peer can
	// limit how many there are with MAX_REQUEST_UPDATES.
	pendingUpdates []*FillStream

	// fill is the fill the SUBSCRIBE asked for; fills holds every fill,
	// Refills included, so that ending the subscription can end them too.
	fill  *FillStream
	fills []*FillStream

	// states carries PUBLISH_STATE_NOTIFYs to the application. Like
	// Subscription.Updates it is created on first ask, written only by the
	// request stream's reader goroutine, and closed by it when the stream
	// ends.
	states       chan PublishState
	statesClosed bool
	statesEnded  bool
}

// remoteTrackObjectBuffer is how many Objects are held for a consumer that is
// not reading. Beyond it the subgroup stream's reader blocks, which is the
// right backpressure: it stops reading, QUIC stops granting flow control, and
// the publisher finds out.
const remoteTrackObjectBuffer = 64

func newRemoteTrack(sessionCtx context.Context, rs *requestStream, session subscriberSession,
	requestID uint64, namespace []string, track string) *RemoteTrack {
	deliveryCtx, cancelDelivery := context.WithCancelCause(sessionCtx)
	return &RemoteTrack{
		requestStream:  rs,
		session:        session,
		requestID:      requestID,
		namespace:      namespace,
		track:          track,
		established:    make(chan struct{}),
		objects:        make(chan *Object, remoteTrackObjectBuffer),
		deliveryCtx:    deliveryCtx,
		cancelDelivery: cancelDelivery,
		streamEnded:    make(chan struct{}, 1),
	}
}

// RequestID is the ID this endpoint assigned to the SUBSCRIBE.
//
// Responses do not carry it -- the stream identifies them -- but it stays
// meaningful because a Joining FETCH names the subscription it joins by
// Request ID.
func (t *RemoteTrack) RequestID() uint64 { return t.requestID }

// Namespace is the Track Namespace this subscription is for.
func (t *RemoteTrack) Namespace() []string { return t.namespace }

// Track is the Track Name this subscription is for.
func (t *RemoteTrack) Track() string { return t.track }

// fullName identifies the track for the DUPLICATE_TRACK_ALIAS check, which is
// about a Track Alias naming two different tracks at once.
func (t *RemoteTrack) fullName() string {
	return strings.Join(t.namespace, "/") + "\x00" + t.track
}

// TrackAlias is the alias the publisher assigned, which Subgroups and
// Datagrams for this subscription carry instead of the full track name.
func (t *RemoteTrack) TrackAlias() uint64 {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.trackAlias
}

// Parameters returns the Message Parameters from SUBSCRIBE_OK.
func (t *RemoteTrack) Parameters() Parameters {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.params
}

// TrackProperties returns the Track Properties from SUBSCRIBE_OK. Unlike
// Parameters they describe the Track rather than this subscription, and a
// relay forwards them unchanged.
func (t *RemoteTrack) TrackProperties() KVPList {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.properties
}

// LargestObject returns the largest Location the publisher reported, and
// whether it reported one. A publisher that has published anything on the
// track MUST send it, so its absence means the track is empty so far.
//
// It is what a subscriber using a relative filter needs in order to know where
// its subscription actually starts. SUBSCRIBE_OK sets it; on a draft-22
// session REQUEST_UPDATE_OK and PUBLISH_STATE_NOTIFY move it on.
func (t *RemoteTrack) LargestObject() (Location, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.largest, t.hasLargest
}

// Paused reports whether the subscription is paused, either because we asked
// for it with WithForward(false) or an update, or because the publisher said
// so with PUBLISH_STATE_NOTIFY.
func (t *RemoteTrack) Paused() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.paused
}

// StateUpdates returns the channel the publisher's PUBLISH_STATE_NOTIFYs
// arrive on (draft-22 only; a draft-18 session never sends one). It is closed
// when the subscription's stream ends.
//
// Call it once, before the publisher could have anything to report. As with
// Subscription.Updates, the first call is what starts queueing, and a
// consumer that stops reading eventually stalls this subscription's stream.
// LargestObject and Paused are kept current whether or not anyone reads.
func (t *RemoteTrack) StateUpdates() <-chan PublishState {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.states == nil {
		t.states = make(chan PublishState, subscriptionUpdateBuffer)
		if t.statesEnded {
			close(t.states)
			t.statesClosed = true
		}
	}
	return t.states
}

// PublishDone returns the publisher's report that the subscription ended, if
// one arrived. A subscription can also end without it, by the stream being
// reset; then the request's context carries the reason and this returns false.
func (t *RemoteTrack) PublishDone() (PublishDone, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.publishDone == nil {
		return PublishDone{}, false
	}
	return *t.publishDone, true
}

// ReadObject returns the next Object, blocking until one arrives.
//
// It returns the reason the subscription ended once there are no Objects
// left: the publisher's PUBLISH_DONE, a reset from either side, or the
// session going away. Objects already received are drained first, and after a
// clean PUBLISH_DONE delivery stays open until the StreamCount of data
// streams it announced have ended, so nothing a data stream carried is lost
// to the control stream overtaking it.
func (t *RemoteTrack) ReadObject(ctx context.Context) (*Object, error) {
	// Anything already buffered comes first, whatever else has happened.
	select {
	case o := <-t.objects:
		return o, nil
	default:
	}

	select {
	case o := <-t.objects:
		return o, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-t.deliveryCtx.Done():
		// The subscription ended while we waited. Drain whatever raced in
		// before reporting it.
		select {
		case o := <-t.objects:
			return o, nil
		default:
		}
		return nil, context.Cause(t.deliveryCtx)
	}
}

// deliver hands an Object to the consumer. It blocks while the buffer is full,
// which is deliberate: the caller is a data stream's reader goroutine, and
// stalling it is how backpressure reaches the publisher.
func (t *RemoteTrack) deliver(ctx context.Context, o *Object) error {
	select {
	case t.objects <- o:
		return nil
	case <-ctx.Done():
		return context.Cause(ctx)
	case <-t.deliveryCtx.Done():
		return context.Cause(t.deliveryCtx)
	}
}

// subgroupStreamEnded records that one of this subscription's subgroup
// streams is over. With markers enabled it first delivers the synthetic
// end-of-subgroup Object -- before counting, so the marker beats the
// StreamCount-completed end of delivery.
func (t *RemoteTrack) subgroupStreamEnded(ctx context.Context, header *wire2.SubgroupHeader,
	recvErr error, markers bool) {
	if markers {
		marker := &Object{
			GroupID:              header.GroupID,
			SubgroupID:           header.SubgroupID,
			ForwardingPreference: ObjectForwardingPreferenceSubgroup,
			EndOfGroup:           header.EndOfGroup,
		}
		if recvErr != nil {
			marker.SubgroupReset = true
		} else {
			marker.EndsSubgroup = true
		}
		_ = t.deliver(ctx, marker)
	}
	t.dataStreamEnded()
}

// Update sends a REQUEST_UPDATE on this subscription's own stream. The
// publisher answers with REQUEST_OK or, if it cannot apply the update, with
// REQUEST_ERROR followed by PUBLISH_DONE.
//
// A parameter the update does not mention keeps its value; there is no way to
// remove one.
//
// A draft-22 publisher may limit how many updates can be unanswered at once
// (MAX_REQUEST_UPDATES); an update beyond that fails with
// ErrTooManyRequestUpdates rather than being sent, since sending it would end
// the session.
func (t *RemoteTrack) Update(opts ...SubscribeUpdateOption) error {
	update := &wire2.RequestUpdate{Parameters: wire2.Parameters{}}
	for _, opt := range opts {
		if err := opt(update); err != nil {
			return err
		}
	}
	return t.update(update.Parameters, nil)
}

// update sends a REQUEST_UPDATE carrying params. newFill, when set, makes the
// fill the update asks for once its Request ID is known.
func (t *RemoteTrack) update(params wire2.Parameters, newFill func(requestID uint64) *FillStream) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if limit := t.session.peerMaxRequestUpdates(); limit > 0 && uint64(len(t.pendingUpdates)) >= limit {
		return ErrTooManyRequestUpdates
	}

	// The Request ID is allocated only once the update is going out, so an
	// update refused above leaves no hole in the sequence. Holding the lock
	// across the write keeps the slots in the order the updates are sent.
	update := &wire2.RequestUpdate{RequestID: t.session.nextRequestID(), Parameters: params}
	var fill *FillStream
	if newFill != nil {
		fill = newFill(update.RequestID)
		t.session.registerFetchReceiver(update.RequestID, fill)
	}
	if err := t.write(update); err != nil {
		if fill != nil {
			t.session.unregisterFetchReceiver(update.RequestID)
		}
		return err
	}
	t.pendingUpdates = append(t.pendingUpdates, fill)
	if fill != nil {
		t.fills = append(t.fills, fill)
	}
	if forward, ok := params.Get(wire2.ParamForward); ok {
		t.paused = forward.Number == 0
	}
	if f, ok := params.LocationFilter(); ok {
		t.filter, t.hasFilter = activeFilter(f, true)
	}
	return nil
}

// ErrTooManyRequestUpdates means the publisher's MAX_REQUEST_UPDATES limit is
// reached: wait for an earlier update to be answered.
var ErrTooManyRequestUpdates = errors.New("too many unanswered REQUEST_UPDATEs")

// Close ends the subscription by resetting its request stream, which is what
// draft-18 uses in place of UNSUBSCRIBE. It also ends delivery at once: a
// subscriber that closed is not waiting for straggling data streams, nor for
// the rest of a fill.
func (t *RemoteTrack) Close() error {
	t.cancel(StreamErrorCancelled, errSubscriptionClosedLocally)
	t.finishDelivery(errSubscriptionClosedLocally)
	t.mu.Lock()
	fills := append([]*FillStream(nil), t.fills...)
	t.mu.Unlock()
	for _, f := range fills {
		_ = f.Close()
	}
	return nil
}

// awaitEstablished blocks until the publisher has answered the SUBSCRIBE.
func (t *RemoteTrack) awaitEstablished(ctx context.Context) error {
	if err := awaitAnswer(ctx, t.established, t.ctx); err != nil {
		return err
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.answerErr
}

// run reads the publisher's half of the request stream until it ends.
func (t *RemoteTrack) run() error {
	err := t.requestStream.run(t.handleMessage)
	t.closeStates()

	// Whatever ended the subscription, release anyone still waiting for the
	// answer that is now never coming.
	t.establishOnce.Do(func() {
		t.mu.Lock()
		if t.answerErr == nil {
			t.answerErr = context.Cause(t.ctx)
		}
		t.mu.Unlock()
		close(t.established)
	})

	// A clean end with a PUBLISH_DONE that counted its streams keeps delivery
	// (and the Track Alias, which routes late data streams here) open until
	// those streams have ended; anything else -- a reset, a session error, a
	// publisher that could not count -- ends delivery now.
	t.mu.Lock()
	done := t.publishDone
	ended := t.endedStreams
	t.mu.Unlock()
	cause := context.Cause(t.ctx)
	if err == nil && done != nil && done.StreamCount != unknownStreamCount(t.version) && ended < done.StreamCount {
		go t.awaitDataStreams(cause, done.StreamCount)
	} else {
		t.finishDelivery(cause)
	}
	return err
}

// unknownStreamCount is the PUBLISH_DONE Stream Count of a publisher that
// cannot count exactly, so there is nothing to wait for: 2^62-1 in draft-18
// (Section 10.11), the largest vi64 from draft-20 on (WG PR #1831).
func unknownStreamCount(version wire2.Version) uint64 {
	if version.AtLeast(wire2.Version22) {
		return math.MaxUint64
	}
	return 1<<62 - 1
}

// publishDoneGrace bounds how long delivery waits for the data streams a
// PUBLISH_DONE announced. The missing ones are usually milliseconds behind
// the control stream; ones that never come (reset before their header could
// name this subscription, or a miscounting publisher) must not hold the
// subscription open forever. The tests run under testing/synctest, where the
// second is fake time, so nothing needs to override it.
const publishDoneGrace = time.Second

// awaitDataStreams ends delivery once the announced number of data streams
// has ended, or the grace period has, or something else (Close, the session)
// ended delivery first.
func (t *RemoteTrack) awaitDataStreams(cause error, want uint64) {
	timer := time.NewTimer(publishDoneGrace)
	defer timer.Stop()
	for {
		t.mu.Lock()
		ended := t.endedStreams
		t.mu.Unlock()
		if ended >= want {
			break
		}
		select {
		case <-t.streamEnded:
		case <-timer.C:
			t.finishDelivery(cause)
			return
		case <-t.deliveryCtx.Done():
			t.finishDelivery(context.Cause(t.deliveryCtx))
			return
		}
	}
	t.finishDelivery(cause)
}

// finishDelivery ends Object delivery and stops routing data streams here. It
// is idempotent; the first cause wins. A fill whose stream never came is over
// too: the subscription that would have carried it is.
func (t *RemoteTrack) finishDelivery(cause error) {
	t.cancelDelivery(cause)
	t.mu.Lock()
	alias := t.trackAlias
	registered := t.params != nil
	fills := append([]*FillStream(nil), t.fills...)
	t.mu.Unlock()
	if registered {
		t.session.unregisterTrackAlias(alias, t)
	}
	for _, f := range fills {
		f.mu.Lock()
		attached := f.stream != nil
		f.mu.Unlock()
		if !attached {
			f.finish(false, context.Cause(t.deliveryCtx))
		}
		t.session.unregisterFetchReceiver(f.requestID)
	}
}

// dataStreamEnded counts a data stream of this subscription toward the
// PUBLISH_DONE Stream Count; in draft-22 fill streams count too.
func (t *RemoteTrack) dataStreamEnded() {
	t.mu.Lock()
	t.endedStreams++
	t.mu.Unlock()
	select {
	case t.streamEnded <- struct{}{}:
	default:
	}
}

// awaitGroupOrder returns the Group Order a fill inherits from the
// subscription: what the SUBSCRIBE asked for, or else the publisher's
// preference for the Track from SUBSCRIBE_OK. It waits for SUBSCRIBE_OK when
// it has to.
func (t *RemoteTrack) awaitGroupOrder(ctx context.Context) (GroupOrder, error) {
	t.mu.Lock()
	order, ok := t.groupOrder, t.hasGroupOrder
	t.mu.Unlock()
	if ok {
		return order, nil
	}
	if err := t.awaitEstablished(ctx); err != nil {
		return 0, err
	}
	return t.TrackProperties().DefaultPublisherGroupOrder()
}

func (t *RemoteTrack) handleMessage(msg wire2.ControlMessage) error {
	switch m := msg.(type) {
	case *wire2.SubscribeOk:
		return t.accepted(m)

	case *wire2.RequestError:
		t.mu.Lock()
		if t.params != nil {
			// Established, so this answers a REQUEST_UPDATE. The publisher
			// ends the subscription with PUBLISH_DONE(UPDATE_FAILED) next.
			fill := t.answeredUpdateLocked()
			t.mu.Unlock()
			if fill != nil {
				fill.finish(false, &RequestError{Code: RequestErrorCode(m.ErrorCode), Reason: m.ErrorReason})
			}
			return nil
		}
		t.answerErr = &RequestError{
			Code:   RequestErrorCode(m.ErrorCode),
			Reason: m.ErrorReason,
		}
		t.mu.Unlock()
		t.establishOnce.Do(func() { close(t.established) })
		return nil

	case *wire2.PublishDone:
		t.mu.Lock()
		t.publishDone = &PublishDone{
			Code:        PublishDoneCode(m.StatusCode),
			Reason:      m.ErrorReason,
			StreamCount: m.StreamCount,
		}
		t.mu.Unlock()
		// The publisher FINs right after this, which is what ends the
		// subscription; there is nothing to answer.
		return nil

	case *wire2.RequestOk:
		// The answer to a REQUEST_UPDATE we sent. We already know what we
		// asked for; what is new is the publisher's Largest Object, which a
		// draft-22 REQUEST_UPDATE_OK carries. Track Properties belong only in
		// a TRACK_STATUS_OK (Section 10.5).
		if len(m.TrackProperties) > 0 {
			return errUnexpectedTrackProperties
		}
		t.mu.Lock()
		fill := t.answeredUpdateLocked()
		if largest, ok := m.Parameters.LargestObject(); ok {
			t.noteLargestLocked(largest)
		}
		filter, largest, hasLargest, paused := t.filter, t.largest, t.hasLargest, t.paused
		if !t.hasFilter {
			filter = LocationFilter{}
		}
		t.mu.Unlock()
		if fill != nil {
			fill.expect(filter, largest, hasLargest, paused)
		}
		return nil

	case *wire2.PublishStateNotify:
		return t.stateNotified(m)
	}
	return errUnexpectedMessageOnRequestStream
}

// answeredUpdateLocked frees the oldest update's slot, returning the fill it
// asked for, if any.
func (t *RemoteTrack) answeredUpdateLocked() *FillStream {
	if len(t.pendingUpdates) == 0 {
		return nil
	}
	fill := t.pendingUpdates[0]
	t.pendingUpdates = t.pendingUpdates[1:]
	return fill
}

func (t *RemoteTrack) noteLargestLocked(l Location) {
	if !t.hasLargest || l.Compare(t.largest) > 0 {
		t.largest, t.hasLargest = l, true
	}
}

// stateNotified applies a PUBLISH_STATE_NOTIFY and hands it to the
// application. It is unilateral: there is nothing to answer.
func (t *RemoteTrack) stateNotified(m *wire2.PublishStateNotify) error {
	state, err := publishStateFrom(m.Parameters)
	if err != nil {
		return err
	}
	t.mu.Lock()
	if t.params == nil {
		// Draft-22 Section 9.10 sends it only on an established
		// subscription.
		t.mu.Unlock()
		return errUnexpectedMessageOnRequestStream
	}
	if state.HasLargestObject {
		t.noteLargestLocked(state.LargestObject)
	}
	if state.HasPaused {
		t.paused = state.Paused
	}
	if state.HasFilter {
		t.filter, t.hasFilter = activeFilter(state.Filter, true)
	}
	states := t.states
	closed := t.statesClosed
	t.mu.Unlock()
	if states == nil || closed {
		return nil
	}
	select {
	case states <- state:
		return nil
	case <-t.ctx.Done():
		return context.Cause(t.ctx)
	}
}

// closeStates closes the PUBLISH_STATE_NOTIFY channel. It runs on the reader
// goroutine after the read loop has finished, so no send can be in flight.
func (t *RemoteTrack) closeStates() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.statesEnded = true
	if t.states != nil && !t.statesClosed {
		close(t.states)
		t.statesClosed = true
	}
}

// accepted records SUBSCRIBE_OK and registers the Track Alias, which is what
// lets incoming data streams find this subscription.
func (t *RemoteTrack) accepted(ok *wire2.SubscribeOk) error {
	if err := wire2.ValidateTrackProperties(ok.TrackProperties); err != nil {
		return err
	}

	t.mu.Lock()
	if t.params != nil {
		t.mu.Unlock()
		return errDuplicateResponse
	}
	t.trackAlias = ok.TrackAlias
	t.params = ok.Parameters
	t.properties = ok.TrackProperties
	t.largest, t.hasLargest = ok.Parameters.LargestObject()
	fill, filter, paused := t.fill, t.filter, t.paused
	largest, hasLargest := t.largest, t.hasLargest
	if !t.hasFilter {
		filter = LocationFilter{}
	}
	t.mu.Unlock()
	if fill != nil {
		fill.expect(filter, largest, hasLargest, paused)
	}

	// Registering can fail with DUPLICATE_TRACK_ALIAS, which closes the
	// session rather than just this request.
	if err := t.session.registerTrackAlias(ok.TrackAlias, t); err != nil {
		return err
	}
	t.establishOnce.Do(func() { close(t.established) })
	return nil
}

// defaultPriority is what an Object inherits when its Subgroup header or
// datagram leaves the Publisher Priority field off the wire: the Track's
// DEFAULT_PUBLISHER_PRIORITY, or 128 (Section 12.4).
func (t *RemoteTrack) defaultPriority() uint8 {
	priority, err := t.TrackProperties().DefaultPublisherPriority()
	if err != nil {
		return wire2.DefaultPublisherPriority
	}
	return priority
}

// RequestError is a request the peer refused.
type RequestError struct {
	Code   RequestErrorCode
	Reason string
}

func (e *RequestError) Error() string {
	if e.Reason == "" {
		return fmt.Sprintf("request rejected: %v", e.Code)
	}
	return fmt.Sprintf("request rejected: %v: %v", e.Code, e.Reason)
}

// SubscribeOption customises an outgoing SUBSCRIBE.
type SubscribeOption func(*wire2.Subscribe) error

// WithSubscriberPriority asks for a priority relative to this session's other
// subscriptions. Lower numbers get higher priority; the default is 128.
func WithSubscriberPriority(priority uint8) SubscribeOption {
	return func(s *wire2.Subscribe) error {
		s.Parameters = append(s.Parameters, wire2.Uint8Parameter(wire2.ParamSubscriberPriority, priority))
		return nil
	}
}

// WithGroupOrder asks for Groups in a particular order. Omitted, the
// publisher's own preference for the Track applies.
func WithGroupOrder(order GroupOrder) SubscribeOption {
	return func(s *wire2.Subscribe) error {
		if !order.Valid() {
			return fmt.Errorf("invalid group order: %v", order)
		}
		s.Parameters = append(s.Parameters, wire2.Uint8Parameter(wire2.ParamGroupOrder, uint8(order)))
		return nil
	}
}

// WithForward establishes the subscription without asking for Objects yet. A
// later Update can turn delivery on, which is how a subscriber holds a
// subscription open while it is not consuming.
func WithForward(forward bool) SubscribeOption {
	return func(s *wire2.Subscribe) error {
		value := uint8(0)
		if forward {
			value = 1
		}
		s.Parameters = append(s.Parameters, wire2.Uint8Parameter(wire2.ParamForward, value))
		return nil
	}
}

// WithRendezvousTimeout asks a relay to hold the SUBSCRIBE for up to d while
// the Track has no publisher, instead of answering DOES_NOT_EXIST at once
// (Section 10.2.6). The relay may hold it for less; a wait that runs out is
// answered with TIMEOUT. The wire carries whole milliseconds.
func WithRendezvousTimeout(d time.Duration) SubscribeOption {
	return func(s *wire2.Subscribe) error {
		if d < 0 {
			return errNegativeRendezvousTimeout
		}
		s.Parameters = append(s.Parameters,
			wire2.VarintParameter(wire2.ParamRendezvousTimeout, uint64(d/time.Millisecond)))
		return nil
	}
}

// WithFilter asks for part of the Track. Omitted, the subscription is
// unfiltered. A filter the session's draft cannot express fails the
// Subscribe rather than being widened.
func WithFilter(filter LocationFilter) SubscribeOption {
	return func(s *wire2.Subscribe) error {
		s.Parameters = append(s.Parameters, wire2.LocationFilterParameter(filter))
		return nil
	}
}

// SubscribeUpdateOption customises a REQUEST_UPDATE on an existing
// subscription.
type SubscribeUpdateOption func(*wire2.RequestUpdate) error

// WithUpdatedSubscriberPriority changes the subscription's priority.
func WithUpdatedSubscriberPriority(priority uint8) SubscribeUpdateOption {
	return func(u *wire2.RequestUpdate) error {
		u.Parameters = append(u.Parameters, wire2.Uint8Parameter(wire2.ParamSubscriberPriority, priority))
		return nil
	}
}

// WithUpdatedForward turns delivery on or off.
func WithUpdatedForward(forward bool) SubscribeUpdateOption {
	return func(u *wire2.RequestUpdate) error {
		value := uint8(0)
		if forward {
			value = 1
		}
		u.Parameters = append(u.Parameters, wire2.Uint8Parameter(wire2.ParamForward, value))
		return nil
	}
}

// WithUpdatedFilter narrows or widens the subscription. On a draft-22
// session the zero LocationFilter removes the filter; draft-18 has no way to
// say that, and sends a filter starting at {0, 0}, which admits the same.
func WithUpdatedFilter(filter LocationFilter) SubscribeUpdateOption {
	return func(u *wire2.RequestUpdate) error {
		u.Parameters = append(u.Parameters, wire2.LocationFilterParameter(filter))
		return nil
	}
}

// remoteTrackIndex routes incoming data streams and datagrams to the
// subscriptions they belong to.
//
// It is one-to-many, and has to be: the publisher chooses the Track Alias, and
// nothing stops it giving two of our subscriptions to the same track the same
// one. Section 11.1 forbids only the reverse -- one alias naming two different
// tracks -- and that is what add enforces.
type remoteTrackIndex struct {
	mu      sync.Mutex
	byAlias map[uint64][]*RemoteTrack

	// added is closed and replaced whenever an alias appears, so a data stream
	// that overtook its SUBSCRIBE_OK can wait for it without polling.
	added chan struct{}
}

func newRemoteTrackIndex() *remoteTrackIndex {
	return &remoteTrackIndex{
		byAlias: map[uint64][]*RemoteTrack{},
		added:   make(chan struct{}),
	}
}

// add registers a track under an alias, refusing an alias that already names a
// different track.
func (i *remoteTrackIndex) add(alias uint64, t *RemoteTrack) error {
	i.mu.Lock()
	defer i.mu.Unlock()
	for _, existing := range i.byAlias[alias] {
		if existing.fullName() != t.fullName() {
			return errDuplicateTrackAlias
		}
	}
	i.byAlias[alias] = append(i.byAlias[alias], t)
	close(i.added)
	i.added = make(chan struct{})
	return nil
}

func (i *remoteTrackIndex) remove(alias uint64, t *RemoteTrack) {
	i.mu.Lock()
	defer i.mu.Unlock()
	tracks := i.byAlias[alias]
	for n, existing := range tracks {
		if existing == t {
			i.byAlias[alias] = append(tracks[:n], tracks[n+1:]...)
			break
		}
	}
	if len(i.byAlias[alias]) == 0 {
		delete(i.byAlias, alias)
	}
}

// await returns the subscriptions using alias, waiting for one to appear if
// none has yet.
//
// The wait is the whole point. A subgroup stream carrying a Track Alias can
// overtake the SUBSCRIBE_OK that established it, and Section 11.4.2 blesses
// buffering briefly rather than abandoning the stream. Blocking this stream's
// reader is also what withholds its flow control while we wait, which the same
// section allows.
func (i *remoteTrackIndex) await(ctx context.Context, alias uint64) ([]*RemoteTrack, bool) {
	for {
		i.mu.Lock()
		tracks := i.byAlias[alias]
		added := i.added
		i.mu.Unlock()
		if len(tracks) > 0 {
			// A copy, so the caller is not reading the slice we append to.
			return append([]*RemoteTrack(nil), tracks...), true
		}
		select {
		case <-ctx.Done():
			return nil, false
		case <-added:
		}
	}
}

var (
	errSubscriptionClosedLocally = errors.New("subscription closed by the application")
	errNegativeRendezvousTimeout = errors.New("rendezvous timeout must not be negative")

	errUnexpectedTrackProperties = ProtocolError{
		code:    SessionErrorProtocolViolation,
		message: "track properties in a REQUEST_OK that is not a TRACK_STATUS_OK",
	}

	errDuplicateResponse = ProtocolError{
		code:    SessionErrorProtocolViolation,
		message: "more than one response to a request",
	}

	errDuplicateTrackAlias = ProtocolError{
		code:    SessionErrorDuplicateTrackAlias,
		message: "track alias already names a different track",
	}
)
