package moqtransport

import (
	"context"
	"math"
	"testing"
	"testing/synctest"
	"time"

	"github.com/Eyevinn/moqtransport/internal/wire2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The behavior that differs between draft-18 and draft-22, other than FETCH
// and fill, which have files of their own.

// withSubscribeParameter adds a raw parameter to an outgoing SUBSCRIBE, for
// the parameters the public options do not cover.
func withSubscribeParameter(p wire2.Parameter) SubscribeOption {
	return func(s *wire2.Subscribe) error {
		s.Parameters = append(s.Parameters, p)
		return nil
	}
}

// PUBLISH_STATE_NOTIFY is the publisher's unilateral report that the
// subscription changed. It carries the Largest Object the publisher knows,
// which a subscription tracks from what it has written.
func TestPublishStateNotify(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		published := make(chan *Subscription, 1)
		server := &Session{
			SubscribeHandler: SubscribeHandlerFunc(func(r *SubscribeRequest) {
				sub, err := r.Accept(WithLargestObject(Location{Group: 5, Object: 1}))
				if err == nil {
					published <- sub
				}
			}),
		}
		client := &Session{}
		runSessionsVersion(t, wire2.Version22, client, server)

		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		track, err := client.Subscribe(ctx, []string{"ns"}, "track")
		require.NoError(t, err)
		states := track.StateUpdates()
		sub := <-published

		subgroup, err := sub.OpenSubgroup(6, 0, 128)
		require.NoError(t, err)
		_, err = subgroup.WriteObject(0, []byte("x"))
		require.NoError(t, err)
		require.NoError(t, subgroup.Close())
		largest, ok := sub.LargestObject()
		require.True(t, ok)
		assert.Equal(t, Location{Group: 6}, largest, "writing an Object moves the Largest Object")

		require.NoError(t, sub.NotifyState(PublishState{Paused: true, HasPaused: true}))
		assert.True(t, sub.Paused())

		select {
		case state := <-states:
			assert.True(t, state.HasPaused)
			assert.True(t, state.Paused)
			assert.True(t, state.HasLargestObject, "the publisher MUST include it when known")
			assert.Equal(t, Location{Group: 6}, state.LargestObject)
			assert.False(t, state.HasFilter)
		case <-ctx.Done():
			t.Fatal("PUBLISH_STATE_NOTIFY never arrived")
		}
		assert.True(t, track.Paused())
		got, _ := track.LargestObject()
		assert.Equal(t, Location{Group: 6}, got)

		// The filter now in effect, when the publisher reports one.
		require.NoError(t, sub.NotifyState(PublishState{Filter: NextGroup(), HasFilter: true}))
		state := <-states
		assert.True(t, state.HasFilter)
		assert.Equal(t, NextGroup(), state.Filter)
		f, ok := sub.Filter()
		assert.True(t, ok)
		assert.Equal(t, NextGroup(), f)

		// Closing the subscription closes the channel.
		require.NoError(t, sub.Close(PublishDoneTrackEnded, "done"))
		for range states {
		}
	})
}

func TestNotifyStateIsDraft22Only(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		published := make(chan *Subscription, 1)
		server := &Session{
			SubscribeHandler: SubscribeHandlerFunc(func(r *SubscribeRequest) {
				if sub, err := r.Accept(); err == nil {
					published <- sub
				}
			}),
		}
		client := &Session{}
		runSessions(t, client, server)

		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, err := client.Subscribe(ctx, []string{"ns"}, "track")
		require.NoError(t, err)
		sub := <-published
		assert.ErrorIs(t, sub.NotifyState(PublishState{Paused: true, HasPaused: true}), ErrNotInThisDraft)
		assert.False(t, sub.Paused(), "a refused notification changes nothing")
	})
}

// Draft-22's REQUEST_UPDATE_OK carries the Largest Object, so a subscriber
// that widened its filter learns what it missed.
func TestRequestUpdateOkCarriesLargestObject(t *testing.T) {
	forEachVersion(t, func(t *testing.T, version wire2.Version) {
		published := make(chan *Subscription, 1)
		server := &Session{
			SubscribeHandler: SubscribeHandlerFunc(func(r *SubscribeRequest) {
				if sub, err := r.Accept(WithLargestObject(Location{Group: 3})); err == nil {
					published <- sub
				}
			}),
		}
		client := &Session{}
		runSessionsVersion(t, version, client, server)

		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		track, err := client.Subscribe(ctx, []string{"ns"}, "track")
		require.NoError(t, err)
		sub := <-published
		sub.UpdateLargestObject(Location{Group: 8, Object: 2})
		sub.UpdateLargestObject(Location{Group: 7}) // not larger, so ignored

		require.NoError(t, track.Update(WithUpdatedSubscriberPriority(1)))
		synctest.Wait()

		got, ok := track.LargestObject()
		require.True(t, ok)
		if version == wire2.Version18 {
			assert.Equal(t, Location{Group: 3}, got, "draft-18's REQUEST_OK says nothing about it")
		} else {
			assert.Equal(t, Location{Group: 8, Object: 2}, got)
		}
	})
}

// This implementation advertises no MAX_FILTER_RANGES, so a Range Filter on a
// SUBSCRIBE is over the limit and is refused with INVALID_FILTER before the
// handler sees it.
func TestSubscribeRangeFilterRejected(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		server := &Session{
			SubscribeHandler: SubscribeHandlerFunc(func(r *SubscribeRequest) {
				t.Error("a range-filtered subscribe reached the handler")
				_ = r.Reject(RequestErrorInternal, "unreachable")
			}),
		}
		client := &Session{}
		runSessionsVersion(t, wire2.Version22, client, server)

		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, err := client.Subscribe(ctx, []string{"ns"}, "track",
			withSubscribeParameter(wire2.BytesParameter(wire2.ParamSubgroupFilter, []byte{0x00, 0x00})))
		var reqErr *RequestError
		require.ErrorAs(t, err, &reqErr)
		assert.Equal(t, RequestErrorInvalidFilter, reqErr.Code)
		assert.NoError(t, server.Context().Err())
	})
}

// A REQUEST_UPDATE carrying a Range Filter fails the update, which ends the
// subscription with UPDATE_FAILED and the session not at all.
func TestUpdateRangeFilterRejected(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		server := &Session{
			SubscribeHandler: SubscribeHandlerFunc(func(r *SubscribeRequest) { _, _ = r.Accept() }),
		}
		client := &Session{}
		runSessionsVersion(t, wire2.Version22, client, server)

		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		track, err := client.Subscribe(ctx, []string{"ns"}, "track")
		require.NoError(t, err)
		require.NoError(t, track.Update(func(u *wire2.RequestUpdate) error {
			u.Parameters = append(u.Parameters, wire2.BytesParameter(wire2.ParamPriorityFilter, []byte{0x00, 0x05}))
			return nil
		}))

		_, err = track.ReadObject(ctx)
		require.Error(t, err)
		done, ok := track.PublishDone()
		require.True(t, ok)
		assert.Equal(t, PublishDoneUpdateFailed, done.Code)
		assert.NoError(t, server.Context().Err())
	})
}

// INCLUDE_PROPERTIES 0 asks for an empty Track Properties block in
// SUBSCRIBE_OK, whatever the publisher would have sent.
func TestSubscribeIncludeProperties(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		props := KVPList{{Type: wire2.PropertyDefaultPublisherPriority, ValueVarInt: 42}}
		server := &Session{
			SubscribeHandler: SubscribeHandlerFunc(func(r *SubscribeRequest) {
				_, _ = r.Accept(WithTrackProperties(props))
			}),
		}
		client := &Session{}
		runSessionsVersion(t, wire2.Version22, client, server)

		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		with, err := client.Subscribe(ctx, []string{"ns"}, "a")
		require.NoError(t, err)
		assert.Equal(t, props, with.TrackProperties())

		without, err := client.Subscribe(ctx, []string{"ns"}, "b",
			withSubscribeParameter(wire2.Uint8Parameter(wire2.ParamIncludeProperties, 0)))
		require.NoError(t, err)
		assert.Empty(t, without.TrackProperties())
	})
}

// A filter draft-18 cannot express fails the Subscribe rather than being sent
// as a different one.
func TestSubscribeFilterNotInDraft18(t *testing.T) {
	forEachVersion(t, func(t *testing.T, version wire2.Version) {
		filters := make(chan LocationFilter, 1)
		server := &Session{
			SubscribeHandler: SubscribeHandlerFunc(func(r *SubscribeRequest) {
				f, _ := r.Filter()
				filters <- f
				_, _ = r.Accept()
			}),
		}
		client := &Session{}
		runSessionsVersion(t, version, client, server)

		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, err := client.Subscribe(ctx, []string{"ns"}, "track", WithFilter(RelativeStart(1)))
		if version == wire2.Version18 {
			assert.Error(t, err)
			return
		}
		require.NoError(t, err)
		assert.Equal(t, RelativeStart(1), <-filters)
	})
}

// Draft-22 can remove a filter with an explicit None; draft-18 cannot, and
// the closest it has, a start at {0, 0}, admits the same Objects.
func TestUpdateRemovesFilter(t *testing.T) {
	forEachVersion(t, func(t *testing.T, version wire2.Version) {
		published := make(chan *Subscription, 1)
		server := &Session{
			SubscribeHandler: SubscribeHandlerFunc(func(r *SubscribeRequest) {
				if sub, err := r.Accept(); err == nil {
					published <- sub
				}
			}),
		}
		client := &Session{}
		runSessionsVersion(t, version, client, server)

		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		track, err := client.Subscribe(ctx, []string{"ns"}, "track", WithFilter(AbsoluteStart(Location{Group: 4})))
		require.NoError(t, err)
		sub := <-published
		updates := sub.Updates()
		_, has := sub.Filter()
		require.True(t, has)

		require.NoError(t, track.Update(WithUpdatedFilter(LocationFilter{})))
		update := <-updates
		if version == wire2.Version18 {
			assert.True(t, update.HasFilter)
			assert.Equal(t, AbsoluteStart(Location{}), update.Filter)
		} else {
			assert.False(t, update.HasFilter)
		}
	})
}

// A publisher that cannot count its streams sends 2^62-1 in draft-18 and
// 2^64-1 in draft-22; either way there is nothing to wait for.
func TestUnknownStreamCountPerDraft(t *testing.T) {
	assert.Equal(t, uint64(1<<62-1), unknownStreamCount(wire2.Version18))
	assert.Equal(t, uint64(math.MaxUint64), unknownStreamCount(wire2.Version22))
}

// fakeSubscriberSession is just enough session for a RemoteTrack.
type fakeSubscriberSession struct {
	maxUpdates uint64
	nextID     uint64
}

func (s *fakeSubscriberSession) registerTrackAlias(uint64, *RemoteTrack) error { return nil }
func (s *fakeSubscriberSession) unregisterTrackAlias(uint64, *RemoteTrack)     {}
func (s *fakeSubscriberSession) peerMaxRequestUpdates() uint64                 { return s.maxUpdates }
func (s *fakeSubscriberSession) registerFetchReceiver(uint64, fetchReceiver)   {}
func (s *fakeSubscriberSession) unregisterFetchReceiver(uint64)                {}
func (s *fakeSubscriberSession) nextRequestID() uint64 {
	s.nextID += 2
	return s.nextID
}

// A draft-22 publisher can cap the unanswered REQUEST_UPDATEs on a stream.
// Going over closes the session, so the subscriber holds back instead; an
// answer, success or failure, frees a slot.
func TestMaxRequestUpdatesLimitsTheSender(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		rs, peer := newRequestStreamPeerVersion(t, ctx, wire2.Version22)
		session := &fakeSubscriberSession{maxUpdates: 2}
		track := newRemoteTrack(ctx, rs, session, 0, []string{"ns"}, "track")
		go func() { _ = track.run() }()

		peer.send(t, &wire2.SubscribeOk{TrackAlias: 1, Parameters: wire2.Parameters{}})
		require.NoError(t, track.awaitEstablished(ctx))

		require.NoError(t, track.Update(WithUpdatedSubscriberPriority(1)))
		require.NoError(t, track.Update(WithUpdatedSubscriberPriority(2)))
		assert.ErrorIs(t, track.Update(WithUpdatedSubscriberPriority(3)), ErrTooManyRequestUpdates)

		peer.send(t, &wire2.RequestOk{Parameters: wire2.Parameters{}})
		synctest.Wait()
		require.NoError(t, track.Update(WithUpdatedSubscriberPriority(3)))
		assert.ErrorIs(t, track.Update(WithUpdatedSubscriberPriority(4)), ErrTooManyRequestUpdates)

		// Only the updates that went out consumed Request IDs.
		var ids []uint64
		for _, msg := range peer.sent(t) {
			if update, ok := msg.(*wire2.RequestUpdate); ok {
				ids = append(ids, update.RequestID)
			}
		}
		assert.Equal(t, []uint64{2, 4, 6}, ids)
	})
}
