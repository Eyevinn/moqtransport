package moqtransport

import (
	"context"
	"testing"
	"time"

	"github.com/Eyevinn/moqtransport/internal/wire2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// runSessions brings up both ends of a session pair. Neither Run can finish
// alone: each waits for the peer's SETUP, and Section 3.3 gives the handshake
// no client/server ordering to rely on.
func runSessions(t *testing.T, client, server *Session) {
	t.Helper()
	clientConn, serverConn := newMemConnPair(wire2.Version18.ALPN())

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)

	errs := make(chan error, 2)
	go func() { errs <- client.Run(ctx, clientConn) }()
	go func() { errs <- server.Run(ctx, serverConn) }()
	require.NoError(t, <-errs)
	require.NoError(t, <-errs)

	t.Cleanup(func() {
		_ = client.Close(SessionErrorNoError, "test over")
		_ = server.Close(SessionErrorNoError, "test over")
	})
}

func TestSessionHandshake(t *testing.T) {
	client := &Session{Implementation: "test/client"}
	server := &Session{Implementation: "test/server"}
	runSessions(t, client, server)

	assert.Equal(t, wire2.Version18, client.Version())
	assert.Equal(t, wire2.Version18, server.Version())
	assert.NoError(t, client.Context().Err())
	assert.NoError(t, server.Context().Err())
}

// A peer that negotiated something this build does not speak cannot be
// recovered from: from draft-17 there is no version field to fall back on.
func TestSessionRejectsUnknownALPN(t *testing.T) {
	conn, _ := newMemConnPair("moqt-16")
	err := (&Session{}).Run(context.Background(), conn)
	assert.ErrorIs(t, err, errUnsupportedVersion)
}

// The whole stack end to end: a SUBSCRIBE, its answer, Objects on a subgroup
// stream and in a datagram, and PUBLISH_DONE.
func TestSessionSubscribeEndToEnd(t *testing.T) {
	published := make(chan *Subscription, 1)
	server := &Session{
		SubscribeHandler: SubscribeHandlerFunc(func(r *SubscribeRequest) {
			if r.Track() != "video0" {
				_ = r.Reject(RequestErrorDoesNotExist, "no such track")
				return
			}
			sub, err := r.Accept(
				WithLargestObject(Location{Group: 7, Object: 2}),
				WithTrackProperties(KVPList{{Type: wire2.PropertyDefaultPublisherPriority, ValueVarInt: 42}}),
			)
			if err != nil {
				return
			}
			published <- sub
		}),
	}
	client := &Session{}
	runSessions(t, client, server)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	track, err := client.Subscribe(ctx, []string{"example.com", "live"}, "video0",
		WithSubscriberPriority(3),
		WithFilter(SubscriptionFilter{Type: FilterLargestObject}),
	)
	require.NoError(t, err)

	largest, present := track.LargestObject()
	require.True(t, present)
	assert.Equal(t, uint64(7), largest.Group)

	sub := <-published
	assert.Equal(t, []string{"example.com", "live"}, sub.Namespace())
	assert.Equal(t, "video0", sub.Track())
	assert.Equal(t, uint8(3), sub.SubscriberPriority())
	assert.Equal(t, track.TrackAlias(), sub.TrackAlias())

	// Objects on a subgroup stream.
	sg, err := sub.OpenSubgroup(7, 0, 128, WithEndOfGroup())
	require.NoError(t, err)
	_, err = sg.WriteObject(3, []byte("first"))
	require.NoError(t, err)
	_, err = sg.WriteObject(4, []byte("second"))
	require.NoError(t, err)
	require.NoError(t, sg.Close())

	first, err := track.ReadObject(ctx)
	require.NoError(t, err)
	assert.Equal(t, "first", string(first.Payload))
	assert.Equal(t, uint64(7), first.GroupID)
	assert.Equal(t, uint64(3), first.ObjectID)
	assert.Equal(t, ObjectForwardingPreferenceSubgroup, first.ForwardingPreference)

	second, err := track.ReadObject(ctx)
	require.NoError(t, err)
	assert.Equal(t, "second", string(second.Payload))

	// And one in a datagram, which has no Subgroup of its own. It is sent
	// after the stream has been drained on purpose: a datagram and a stream
	// have no ordering between them, and Section 7 in fact has datagrams take
	// precedence in a tie.
	require.NoError(t, sub.SendDatagram(Object{
		GroupID:  8,
		ObjectID: 0,
		Priority: 9,
		Payload:  []byte("datagram"),
	}))

	datagram, err := track.ReadObject(ctx)
	require.NoError(t, err)
	assert.Equal(t, "datagram", string(datagram.Payload))
	assert.Equal(t, uint64(8), datagram.GroupID)
	assert.Equal(t, ObjectForwardingPreferenceDatagram, datagram.ForwardingPreference)

	// PUBLISH_DONE ends the subscription, and its Stream Count is how the
	// subscriber knows it has seen every stream.
	require.NoError(t, sub.Close(PublishDoneTrackEnded, "that's all"))

	_, err = track.ReadObject(ctx)
	require.Error(t, err)
	done, ok := track.PublishDone()
	require.True(t, ok)
	assert.Equal(t, PublishDoneTrackEnded, done.Code)
	assert.Equal(t, uint64(1), done.StreamCount)
}

// The publisher's DEFAULT_PUBLISHER_PRIORITY applies to Objects whose stream
// left the priority off the wire.
func TestSessionInheritsPublisherPriority(t *testing.T) {
	published := make(chan *Subscription, 1)
	server := &Session{
		SubscribeHandler: SubscribeHandlerFunc(func(r *SubscribeRequest) {
			sub, err := r.Accept(WithTrackProperties(KVPList{
				{Type: wire2.PropertyDefaultPublisherPriority, ValueVarInt: 42},
			}))
			if err != nil {
				return
			}
			published <- sub
		}),
	}
	client := &Session{}
	runSessions(t, client, server)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	track, err := client.Subscribe(ctx, []string{"ns"}, "track")
	require.NoError(t, err)
	sub := <-published

	sg, err := sub.OpenSubgroup(0, 0, 0, WithSubscriptionPriority())
	require.NoError(t, err)
	_, err = sg.WriteObject(0, []byte("x"))
	require.NoError(t, err)
	require.NoError(t, sg.Close())

	object, err := track.ReadObject(ctx)
	require.NoError(t, err)
	assert.Equal(t, uint8(42), object.Priority)
}

func TestSessionSubscribeRejected(t *testing.T) {
	server := &Session{
		SubscribeHandler: SubscribeHandlerFunc(func(r *SubscribeRequest) {
			_ = r.Reject(RequestErrorDoesNotExist, "no such track")
		}),
	}
	client := &Session{}
	runSessions(t, client, server)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, err := client.Subscribe(ctx, []string{"ns"}, "missing")
	var reqErr *RequestError
	require.ErrorAs(t, err, &reqErr)
	assert.Equal(t, RequestErrorDoesNotExist, reqErr.Code)
	assert.Equal(t, "no such track", reqErr.Reason)
}

// With no handler the peer gets NOT_SUPPORTED rather than silence.
func TestSessionSubscribeWithoutHandler(t *testing.T) {
	client, server := &Session{}, &Session{}
	runSessions(t, client, server)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, err := client.Subscribe(ctx, []string{"ns"}, "track")
	var reqErr *RequestError
	require.ErrorAs(t, err, &reqErr)
	assert.Equal(t, RequestErrorNotSupported, reqErr.Code)
}

// draft-18 has no UNSUBSCRIBE: the subscriber resets its request stream, and
// the publisher's only notice is its request context ending.
func TestSessionUnsubscribeCancelsThePublisher(t *testing.T) {
	gone := make(chan error, 1)
	server := &Session{
		SubscribeHandler: SubscribeHandlerFunc(func(r *SubscribeRequest) {
			if _, err := r.Accept(); err != nil {
				return
			}
			<-r.Context().Done()
			gone <- context.Cause(r.Context())
		}),
	}
	client := &Session{}
	runSessions(t, client, server)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	track, err := client.Subscribe(ctx, []string{"ns"}, "track")
	require.NoError(t, err)
	require.NoError(t, track.Close())

	select {
	case err := <-gone:
		assert.Error(t, err)
	case <-ctx.Done():
		t.Fatal("the publisher never noticed the subscriber leaving")
	}
}

// A REQUEST_UPDATE travels on the subscription's own stream and is reported to
// the publisher after the library has applied and answered it.
func TestSessionSubscriptionUpdate(t *testing.T) {
	published := make(chan *Subscription, 1)
	server := &Session{
		SubscribeHandler: SubscribeHandlerFunc(func(r *SubscribeRequest) {
			sub, err := r.Accept()
			if err != nil {
				return
			}
			published <- sub
		}),
	}
	client := &Session{}
	runSessions(t, client, server)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	track, err := client.Subscribe(ctx, []string{"ns"}, "track", WithSubscriberPriority(5))
	require.NoError(t, err)
	sub := <-published
	updates := sub.Updates()

	require.NoError(t, track.Update(
		WithUpdatedSubscriberPriority(200),
		WithUpdatedForward(false),
	))

	select {
	case update := <-updates:
		assert.Equal(t, uint8(200), update.SubscriberPriority)
		assert.False(t, update.Forward)
	case <-ctx.Done():
		t.Fatal("the update never arrived")
	}
	assert.Equal(t, uint8(200), sub.SubscriberPriority())
	assert.False(t, sub.Forward())
}

// A subgroup stream can overtake the SUBSCRIBE_OK that establishes its Track
// Alias, so the receiver waits briefly rather than abandoning it.
func TestRemoteTrackIndexAwaitsAlias(t *testing.T) {
	index := newRemoteTrackIndex()
	track := &RemoteTrack{namespace: []string{"ns"}, track: "t"}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	found := make(chan []*RemoteTrack, 1)
	go func() {
		tracks, _ := index.await(ctx, 7)
		found <- tracks
	}()

	require.NoError(t, index.add(7, track))
	select {
	case tracks := <-found:
		require.Len(t, tracks, 1)
		assert.Same(t, track, tracks[0])
	case <-ctx.Done():
		t.Fatal("the waiter was never woken")
	}

	// An alias nobody subscribed to gives up rather than waiting forever.
	expired, cancelExpired := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancelExpired()
	_, ok := index.await(expired, 9)
	assert.False(t, ok)
}

// Two subscriptions to one track may share an alias; one alias naming two
// different tracks closes the session (Section 11.1).
func TestRemoteTrackIndexDuplicateAlias(t *testing.T) {
	index := newRemoteTrackIndex()
	first := &RemoteTrack{namespace: []string{"ns"}, track: "a"}
	same := &RemoteTrack{namespace: []string{"ns"}, track: "a"}
	other := &RemoteTrack{namespace: []string{"ns"}, track: "b"}

	require.NoError(t, index.add(1, first))
	require.NoError(t, index.add(1, same))
	assert.ErrorIs(t, index.add(1, other), errDuplicateTrackAlias)

	tracks, ok := index.await(context.Background(), 1)
	require.True(t, ok)
	assert.Len(t, tracks, 2)

	index.remove(1, first)
	index.remove(1, same)
	expired, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	_, ok = index.await(expired, 1)
	assert.False(t, ok, "an emptied alias is forgotten")
}

// The PATH setup option is for native QUIC clients only.
func TestSessionPathOptionIsQUICClientOnly(t *testing.T) {
	_, serverConn := newMemConnPair(wire2.Version18.ALPN())
	err := (&Session{Path: "/live"}).Run(context.Background(), serverConn)
	assert.ErrorIs(t, err, errPathFromServer)
}
