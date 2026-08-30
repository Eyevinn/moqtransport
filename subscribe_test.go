package moqtransport

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/Eyevinn/moqtransport/internal/wire2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakePublisherSession stands in for the session a subscription belongs to. It
// hands out aliases per track and records everything that leaves.
type fakePublisherSession struct {
	mu            sync.Mutex
	aliases       map[string]uint64
	streams       []*sendCapture
	datagrams     [][]byte
	subscriptions map[uint64]*Subscription
	mapper        PriorityMapper
	prioritized   []*prioritizedStream
	openErr       error

	t *testing.T
}

func newFakePublisherSession(t *testing.T) *fakePublisherSession {
	return &fakePublisherSession{aliases: map[string]uint64{}, t: t}
}

func (s *fakePublisherSession) trackAlias(namespace []string, track string) uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := track
	for _, f := range namespace {
		key = f + "/" + key
	}
	if alias, ok := s.aliases[key]; ok {
		return alias
	}
	alias := uint64(len(s.aliases))
	s.aliases[key] = alias
	return alias
}

func (s *fakePublisherSession) openUniStream(context.Context) (SendStream, error) {
	if s.openErr != nil {
		return nil, s.openErr
	}
	stream, capture := newSendCapture(s.t)
	// Wrapped so the stream can be scheduled: no released transport can be
	// yet, and a session that never reaches the mapper would test nothing.
	prioritized := &prioritizedStream{SendStream: stream}
	s.mu.Lock()
	s.streams = append(s.streams, capture)
	s.prioritized = append(s.prioritized, prioritized)
	s.mu.Unlock()
	return prioritized, nil
}

func (s *fakePublisherSession) sendDatagram(b []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.datagrams = append(s.datagrams, b)
	return nil
}

func (s *fakePublisherSession) registerSubscription(requestID uint64, sub *Subscription) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.subscriptions == nil {
		s.subscriptions = map[uint64]*Subscription{}
	}
	s.subscriptions[requestID] = sub
}

func (s *fakePublisherSession) unregisterSubscription(requestID uint64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.subscriptions, requestID)
}

func (s *fakePublisherSession) priorityMapper() PriorityMapper {
	if s.mapper != nil {
		return s.mapper
	}
	return DefaultPriorityMapper
}

// newIncomingSubscribe builds an incoming SUBSCRIBE as if the session's accept
// loop had just read it, and returns the peer's end of the wire.
func newIncomingSubscribe(t *testing.T, params wire2.Parameters) (*SubscribeRequest, *requestStreamPeer, *fakePublisherSession) {
	t.Helper()
	rs, peer := newRequestStreamPeer(t, context.Background())
	session := newFakePublisherSession(t)
	req := newSubscribeRequest(rs, session, &wire2.Subscribe{
		RequestID:      2,
		TrackNamespace: [][]byte{[]byte("example.com"), []byte("live")},
		TrackName:      []byte("video0"),
		Parameters:     params,
	})
	return req, peer, session
}

func TestSubscribeRequestAccessors(t *testing.T) {
	filter, err := wire2.SubscriptionFilterParameter(wire2.SubscriptionFilter{
		Type:          wire2.FilterAbsoluteStart,
		StartLocation: wire2.Location{Group: 3, Object: 1},
	})
	require.NoError(t, err)

	req, _, _ := newIncomingSubscribe(t, wire2.Parameters{
		wire2.Uint8Parameter(wire2.ParamSubscriberPriority, 7),
		wire2.Uint8Parameter(wire2.ParamGroupOrder, uint8(GroupOrderDescending)),
		wire2.Uint8Parameter(wire2.ParamForward, 0),
		filter,
	})
	defer req.cancel(StreamErrorCancelled, errors.New("test over"))

	assert.Equal(t, uint64(2), req.RequestID())
	assert.Equal(t, []string{"example.com", "live"}, req.Namespace())
	assert.Equal(t, "video0", req.Track())
	assert.Equal(t, uint8(7), req.SubscriberPriority())

	order, present, err := req.GroupOrder()
	require.NoError(t, err)
	assert.True(t, present)
	assert.Equal(t, GroupOrderDescending, order)

	forward, err := req.Forward()
	require.NoError(t, err)
	assert.False(t, forward)

	f, present, err := req.Filter()
	require.NoError(t, err)
	assert.True(t, present)
	assert.Equal(t, FilterAbsoluteStart, f.Type)
	assert.Equal(t, uint64(3), f.StartLocation.Group)
}

// A request with no parameters at all is the case where the defaults have to
// be right, since nothing on the wire says otherwise.
func TestSubscribeRequestDefaults(t *testing.T) {
	req, _, _ := newIncomingSubscribe(t, nil)
	defer req.cancel(StreamErrorCancelled, errors.New("test over"))

	assert.Equal(t, DefaultSubscriberPriority, req.SubscriberPriority())
	forward, err := req.Forward()
	require.NoError(t, err)
	assert.True(t, forward)
}

func TestSubscribeAcceptSendsSubscribeOk(t *testing.T) {
	req, peer, _ := newIncomingSubscribe(t, nil)
	defer req.cancel(StreamErrorCancelled, errors.New("test over"))

	sub, err := req.Accept(WithLargestObject(Location{Group: 9, Object: 4}))
	require.NoError(t, err)

	sent := peer.sent(t)
	require.Len(t, sent, 1)
	ok, isOk := sent[0].(*wire2.SubscribeOk)
	require.True(t, isOk)
	assert.Equal(t, sub.TrackAlias(), ok.TrackAlias)

	largest, present := ok.Parameters.LargestObject()
	assert.True(t, present)
	assert.Equal(t, uint64(9), largest.Group)

	// A request is answered once. Neither a second Accept nor a late Reject
	// may put a second response on the stream.
	_, err = req.Accept()
	assert.ErrorIs(t, err, errRequestAlreadyAnswered)
	assert.ErrorIs(t, req.Reject(RequestErrorInternal, "too late"), errRequestAlreadyAnswered)
}

// Two subscriptions to the same track share an alias; different tracks do not.
// Section 11.1 forbids one alias naming two tracks, not one track having two
// subscriptions.
func TestSubscribeTrackAliasPerTrack(t *testing.T) {
	first, _, session := newIncomingSubscribe(t, nil)
	defer first.cancel(StreamErrorCancelled, errors.New("test over"))

	rs, _ := newRequestStreamPeer(t, context.Background())
	second := newSubscribeRequest(rs, session, &wire2.Subscribe{
		RequestID:      4,
		TrackNamespace: [][]byte{[]byte("example.com"), []byte("live")},
		TrackName:      []byte("video0"),
	})
	defer second.cancel(StreamErrorCancelled, errors.New("test over"))

	other := newSubscribeRequest(rs, session, &wire2.Subscribe{
		RequestID:      6,
		TrackNamespace: [][]byte{[]byte("example.com"), []byte("live")},
		TrackName:      []byte("audio0"),
	})

	a, err := first.Accept()
	require.NoError(t, err)
	b, err := second.Accept()
	require.NoError(t, err)
	c, err := other.Accept()
	require.NoError(t, err)

	assert.Equal(t, a.TrackAlias(), b.TrackAlias(), "same track, same alias")
	assert.NotEqual(t, a.TrackAlias(), c.TrackAlias(), "different track, different alias")
}

func TestSubscribeReject(t *testing.T) {
	req, peer, _ := newIncomingSubscribe(t, nil)

	require.NoError(t, req.Reject(RequestErrorDoesNotExist, "no such track"))

	sent := peer.sent(t)
	require.Len(t, sent, 1)
	reqErr, isErr := sent[0].(*wire2.RequestError)
	require.True(t, isErr)
	assert.Equal(t, uint64(RequestErrorDoesNotExist), reqErr.ErrorCode)
	assert.Equal(t, "no such track", reqErr.ErrorReason)

	_, _, closes := peer.state()
	assert.Equal(t, 1, closes, "a rejection FINs the stream")
	<-req.Context().Done()
}

// With no handler, a SUBSCRIBE is answered with NOT_SUPPORTED rather than left
// hanging.
func TestSubscribeNoHandler(t *testing.T) {
	req, peer, _ := newIncomingSubscribe(t, nil)

	require.NoError(t, req.serve(nil))

	sent := peer.sent(t)
	require.Len(t, sent, 1)
	reqErr, isErr := sent[0].(*wire2.RequestError)
	require.True(t, isErr)
	assert.Equal(t, uint64(RequestErrorNotSupported), reqErr.ErrorCode)
}

func TestSubscriptionOpenSubgroup(t *testing.T) {
	req, _, session := newIncomingSubscribe(t, nil)
	defer req.cancel(StreamErrorCancelled, errors.New("test over"))

	sub, err := req.Accept()
	require.NoError(t, err)

	sg, err := sub.OpenSubgroup(3, 1, 200, WithEndOfGroup())
	require.NoError(t, err)
	_, err = sg.WriteObject(0, []byte("payload"))
	require.NoError(t, err)
	require.NoError(t, sg.Close())

	require.Len(t, session.streams, 1)
	objects, err := receiveAll(t, session.streams[0].buf.Bytes(), 0)
	require.NoError(t, err)
	require.Len(t, objects, 1)
	assert.Equal(t, uint64(3), objects[0].GroupID)
	assert.Equal(t, uint64(1), objects[0].SubgroupID)
	assert.Equal(t, uint8(200), objects[0].Priority)
	assert.Equal(t, "payload", string(objects[0].Payload))
}

func TestSubscriptionSendDatagram(t *testing.T) {
	req, _, session := newIncomingSubscribe(t, nil)
	defer req.cancel(StreamErrorCancelled, errors.New("test over"))

	sub, err := req.Accept()
	require.NoError(t, err)
	require.NoError(t, sub.SendDatagram(Object{
		GroupID:  4,
		ObjectID: 5,
		Priority: 9,
		Payload:  []byte("frame"),
	}))

	require.Len(t, session.datagrams, 1)
	d, err := wire2.ParseObjectDatagram(session.datagrams[0])
	require.NoError(t, err)
	assert.Equal(t, sub.TrackAlias(), d.TrackAlias)
	assert.Equal(t, uint64(4), d.GroupID)
	assert.Equal(t, uint64(5), d.ObjectID)
	assert.Equal(t, "frame", string(d.Payload))
}

// PUBLISH_DONE's Stream Count is how a subscriber knows it has seen every
// stream, so it has to count the streams actually opened.
func TestSubscriptionCloseReportsStreamCount(t *testing.T) {
	req, peer, _ := newIncomingSubscribe(t, nil)

	sub, err := req.Accept()
	require.NoError(t, err)
	for group := range uint64(3) {
		sg, err := sub.OpenSubgroup(group, 0, 128)
		require.NoError(t, err)
		require.NoError(t, sg.Close())
	}
	require.NoError(t, sub.Close(PublishDoneTrackEnded, "that's all"))

	sent := peer.sent(t)
	require.Len(t, sent, 2)
	done, isDone := sent[1].(*wire2.PublishDone)
	require.True(t, isDone)
	assert.Equal(t, uint64(PublishDoneTrackEnded), done.StatusCode)
	assert.Equal(t, uint64(3), done.StreamCount)
	assert.Equal(t, "that's all", done.ErrorReason)

	_, stops, closes := peer.state()
	assert.Equal(t, 1, closes, "PUBLISH_DONE is followed by a FIN")
	assert.NotEmpty(t, stops, "and by STOP_SENDING, since the request is over")

	require.NoError(t, sub.Close(PublishDoneInternalError, "again"), "closing twice is a no-op")
	assert.Len(t, peer.sent(t), 2)
}

// An update is applied to the subscription, answered with REQUEST_OK, and then
// reported. The state is live whether or not anyone reads the channel.
func TestSubscriptionUpdate(t *testing.T) {
	req, peer, _ := newIncomingSubscribe(t, wire2.Parameters{
		wire2.Uint8Parameter(wire2.ParamSubscriberPriority, 10),
	})

	sub, err := req.Accept()
	require.NoError(t, err)
	updates := sub.Updates()

	served := make(chan error, 1)
	go func() { served <- req.run(req.handleMessage) }()

	filter, err := wire2.SubscriptionFilterParameter(wire2.SubscriptionFilter{
		Type:          wire2.FilterAbsoluteRange,
		StartLocation: wire2.Location{Group: 2},
		EndGroupDelta: 5,
	})
	require.NoError(t, err)
	peer.send(t, &wire2.RequestUpdate{
		RequestID: 2,
		Parameters: wire2.Parameters{
			wire2.Uint8Parameter(wire2.ParamForward, 0),
			filter,
		},
	})

	got := <-updates
	assert.False(t, got.Forward)
	assert.True(t, got.HasFilter)
	assert.Equal(t, FilterAbsoluteRange, got.Filter.Type)
	// A parameter the update does not mention keeps its value.
	assert.Equal(t, uint8(10), got.SubscriberPriority)

	assert.False(t, sub.Forward())
	assert.Equal(t, uint8(10), sub.SubscriberPriority())
	_, hasFilter := sub.Filter()
	assert.True(t, hasFilter)

	sent := peer.sent(t)
	require.Len(t, sent, 2)
	assert.Equal(t, wire2.ControlMessageTypeRequestOk, sent[1].Type(), "every update is answered")

	// Ending the stream closes the channel, so a range over it terminates.
	peer.fin()
	require.NoError(t, <-served)
	req.streamEnded()
	_, open := <-updates
	assert.False(t, open)
}

// An update that cannot be applied does not merely fail: Section 10.9.1 also
// requires the subscription to end with UPDATE_FAILED.
func TestSubscriptionUpdateFailureEndsSubscription(t *testing.T) {
	req, peer, _ := newIncomingSubscribe(t, nil)

	_, err := req.Accept()
	require.NoError(t, err)

	served := make(chan error, 1)
	go func() { served <- req.run(req.handleMessage) }()

	// A filter naming a type the draft does not define.
	peer.send(t, &wire2.RequestUpdate{
		RequestID: 2,
		Parameters: wire2.Parameters{
			wire2.BytesParameter(wire2.ParamSubscriptionFilter, []byte{0x7}),
		},
	})

	require.Error(t, <-served)

	sent := peer.sent(t)
	require.Len(t, sent, 3)
	assert.Equal(t, wire2.ControlMessageTypeSubscribeOk, sent[0].Type())
	assert.Equal(t, wire2.ControlMessageTypeRequestError, sent[1].Type())
	done, isDone := sent[2].(*wire2.PublishDone)
	require.True(t, isDone)
	assert.Equal(t, uint64(PublishDoneUpdateFailed), done.StatusCode)
}

// An update that arrives before the request has been answered has no
// subscription to apply to, so it folds into what the handler will read.
func TestSubscriptionUpdateBeforeAccept(t *testing.T) {
	req, peer, _ := newIncomingSubscribe(t, wire2.Parameters{
		wire2.Uint8Parameter(wire2.ParamSubscriberPriority, 10),
		wire2.Uint8Parameter(wire2.ParamForward, 1),
	})
	defer req.cancel(StreamErrorCancelled, errors.New("test over"))

	require.NoError(t, req.handleMessage(&wire2.RequestUpdate{
		RequestID:  2,
		Parameters: wire2.Parameters{wire2.Uint8Parameter(wire2.ParamSubscriberPriority, 3)},
	}))

	assert.Equal(t, uint8(3), req.SubscriberPriority(), "the update wins")
	forward, err := req.Forward()
	require.NoError(t, err)
	assert.True(t, forward, "and what it did not mention is unchanged")

	sent := peer.sent(t)
	require.Len(t, sent, 1)
	assert.Equal(t, wire2.ControlMessageTypeRequestOk, sent[0].Type())
}

// Anything other than a REQUEST_UPDATE from the subscriber on an established
// subscribe stream is a protocol violation.
func TestSubscriptionRejectsUnexpectedMessage(t *testing.T) {
	req, _, _ := newIncomingSubscribe(t, nil)
	defer req.cancel(StreamErrorCancelled, errors.New("test over"))

	err := req.handleMessage(&wire2.SubscribeOk{})
	assert.ErrorIs(t, err, errUnexpectedMessageOnRequestStream)
}

// A subscription whose Updates is never called queues nothing, so an
// application that does not care cannot stall its own stream.
func TestSubscriptionWithoutUpdatesConsumer(t *testing.T) {
	req, peer, _ := newIncomingSubscribe(t, nil)

	_, err := req.Accept()
	require.NoError(t, err)

	served := make(chan error, 1)
	go func() { served <- req.run(req.handleMessage) }()

	for range subscriptionUpdateBuffer * 2 {
		peer.send(t, &wire2.RequestUpdate{RequestID: 2})
	}
	peer.fin()
	require.NoError(t, <-served)
	req.streamEnded()

	assert.Len(t, peer.sent(t), 1+subscriptionUpdateBuffer*2, "every update is still answered")
}
