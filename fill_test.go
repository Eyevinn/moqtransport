package moqtransport

import (
	"context"
	"testing"
	"testing/synctest"
	"time"

	"github.com/Eyevinn/moqtransport/internal/wire2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// drainFill reads a whole fill.
func drainFill(t *testing.T, ctx context.Context, fill *FillStream) []*FetchObject {
	t.Helper()
	var records []*FetchObject
	for {
		record, err := fill.ReadObject(ctx)
		if err != nil {
			require.ErrorIs(t, err, ErrFetchComplete)
			return records
		}
		records = append(records, record)
	}
}

// serveRange writes one Object per Location in rng's Groups 0..objects-1,
// in the request's Group Order.
func serveRange(r *FillRequest, objects uint64) error {
	response, err := r.Accept()
	if err != nil {
		return err
	}
	rng := r.Range()
	groups := []uint64{}
	for g := rng.Start.Group; g <= rng.End.Group; g++ {
		groups = append(groups, g)
	}
	if r.GroupOrder() == GroupOrderDescending {
		for i, j := 0, len(groups)-1; i < j; i, j = i+1, j-1 {
			groups[i], groups[j] = groups[j], groups[i]
		}
	}
	for _, g := range groups {
		for o := range objects {
			if !rng.Contains(Location{Group: g, Object: o}) {
				continue
			}
			if err := response.WriteObject(Object{GroupID: g, ObjectID: o, Priority: 128, Payload: []byte{byte(g), byte(o)}}); err != nil {
				return err
			}
		}
	}
	return response.Close()
}

func locations(records []*FetchObject) []Location {
	var locs []Location
	for _, r := range records {
		locs = append(locs, Location{Group: r.GroupID, Object: r.ObjectID})
	}
	return locs
}

// Joining at the current Group (draft-22 Section 3.5): the subscription starts
// at the Next Object and the fill delivers the current Group up to it, so
// every Object arrives exactly once.
func TestFillJoinsAtTheCurrentGroup(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		published := make(chan *Subscription, 1)
		fills := make(chan *FillRequest, 1)
		server := &Session{
			SubscribeHandler: SubscribeHandlerFunc(func(r *SubscribeRequest) {
				if sub, err := r.Accept(WithLargestObject(Location{Group: 10, Object: 2})); err == nil {
					published <- sub
				}
			}),
			FillHandler: FillHandlerFunc(func(r *FillRequest) {
				fills <- r
				assert.NoError(t, serveRange(r, 3))
			}),
		}
		client := &Session{}
		runSessionsVersion(t, wire2.Version22, client, server)

		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		track, err := client.Subscribe(ctx, []string{"ns"}, "track",
			WithFilter(NextObject()), WithFill(FillRange(RelativeStart(1))))
		require.NoError(t, err)
		fill := track.Fill()
		require.NotNil(t, fill)
		assert.Equal(t, track.RequestID(), fill.RequestID(), "the initial fill carries the SUBSCRIBE's Request ID")

		request := <-fills
		assert.Equal(t, track.RequestID(), request.RequestID())
		assert.Equal(t, LocationRange{Start: Location{Group: 10}, End: Location{Group: 10, Object: 2}}, request.Range())

		assert.Equal(t, []Location{{Group: 10, Object: 0}, {Group: 10, Object: 1}, {Group: 10, Object: 2}},
			locations(drainFill(t, ctx, fill)))

		// The subscription delivers what comes next.
		sub := <-published
		subgroup, err := sub.OpenSubgroup(10, 0, 128)
		require.NoError(t, err)
		_, err = subgroup.WriteObject(3, []byte("next"))
		require.NoError(t, err)
		require.NoError(t, subgroup.Close())
		object, err := track.ReadObject(ctx)
		require.NoError(t, err)
		assert.Equal(t, Location{Group: 10, Object: 3}, Location{Group: object.GroupID, Object: object.ObjectID})
	})
}

// A fill that names no range takes the subscription's filter, evaluated like
// a FETCH so it stops at the Largest Object.
func TestFillInheritsTheSubscriptionFilter(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		fills := make(chan *FillRequest, 1)
		server := &Session{
			SubscribeHandler: SubscribeHandlerFunc(func(r *SubscribeRequest) {
				_, _ = r.Accept(WithLargestObject(Location{Group: 9, Object: 1}))
			}),
			FillHandler: FillHandlerFunc(func(r *FillRequest) {
				fills <- r
				assert.NoError(t, serveRange(r, 2))
			}),
		}
		client := &Session{}
		runSessionsVersion(t, wire2.Version22, client, server)

		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		track, err := client.Subscribe(ctx, []string{"ns"}, "track",
			WithFilter(AbsoluteStart(Location{Group: 8, Object: 1})), WithFill())
		require.NoError(t, err)
		assert.Equal(t, LocationRange{Start: Location{Group: 8, Object: 1}, End: Location{Group: 9, Object: 1}}, (<-fills).Range())
		assert.Equal(t, []Location{{Group: 8, Object: 1}, {Group: 9, Object: 0}, {Group: 9, Object: 1}},
			locations(drainFill(t, ctx, track.Fill())))
	})
}

// No stream is opened for an empty fill range or a paused subscription, and
// the subscriber, working out the same from the answer, does not wait for
// one.
func TestFillWithNothingToSend(t *testing.T) {
	for name, opts := range map[string][]SubscribeOption{
		"empty range":           {WithFill(FillRange(NextObject()))},
		"nothing published":     {WithFill()},
		"paused":                {WithForward(false), WithFill(FillRange(RelativeStart(1)))},
		"range after largest":   {WithFill(FillRange(AbsoluteStart(Location{Group: 99})))},
		"subscription filtered": {WithFilter(NextGroup()), WithFill()},
	} {
		t.Run(name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				server := &Session{
					SubscribeHandler: SubscribeHandlerFunc(func(r *SubscribeRequest) {
						if r.Track() == "empty" {
							_, _ = r.Accept()
							return
						}
						_, _ = r.Accept(WithLargestObject(Location{Group: 5, Object: 5}))
					}),
					FillHandler: FillHandlerFunc(func(r *FillRequest) {
						t.Errorf("a fill with nothing to send reached the handler: %v", r.Range())
						_ = r.Reject(StreamErrorInternal)
					}),
				}
				client := &Session{}
				runSessionsVersion(t, wire2.Version22, client, server)

				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()

				trackName := "track"
				if name == "nothing published" {
					trackName = "empty"
				}
				track, err := client.Subscribe(ctx, []string{"ns"}, trackName, opts...)
				require.NoError(t, err)
				assert.Empty(t, drainFill(t, ctx, track.Fill()))
			})
		})
	}
}

// Without a FillHandler the publisher refuses the fill the only way draft-22
// has: open the stream and reset it. The subscription carries on.
func TestFillWithoutHandler(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		published := make(chan *Subscription, 1)
		server := &Session{
			SubscribeHandler: SubscribeHandlerFunc(func(r *SubscribeRequest) {
				if sub, err := r.Accept(WithLargestObject(Location{Group: 3})); err == nil {
					published <- sub
				}
			}),
		}
		client := &Session{}
		runSessionsVersion(t, wire2.Version22, client, server)

		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		track, err := client.Subscribe(ctx, []string{"ns"}, "track", WithFill(FillRange(RelativeStart(1))))
		require.NoError(t, err)
		_, err = track.Fill().ReadObject(ctx)
		require.Error(t, err)
		assert.NotErrorIs(t, err, ErrFetchComplete)

		sub := <-published
		subgroup, err := sub.OpenSubgroup(4, 0, 128)
		require.NoError(t, err)
		_, err = subgroup.WriteObject(0, []byte("x"))
		require.NoError(t, err)
		require.NoError(t, subgroup.Close())
		object, err := track.ReadObject(ctx)
		require.NoError(t, err, "a failed fill leaves the subscription alone")
		assert.Equal(t, uint64(4), object.GroupID)
	})
}

// A REQUEST_UPDATE can ask for another fill, which arrives under the update's
// Request ID and does not disturb one already under way.
func TestRefill(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		fills := make(chan *FillRequest, 1)
		server := &Session{
			SubscribeHandler: SubscribeHandlerFunc(func(r *SubscribeRequest) {
				_, _ = r.Accept(WithLargestObject(Location{Group: 6, Object: 1}))
			}),
			FillHandler: FillHandlerFunc(func(r *FillRequest) {
				fills <- r
				assert.NoError(t, serveRange(r, 2))
			}),
		}
		client := &Session{}
		runSessionsVersion(t, wire2.Version22, client, server)

		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		track, err := client.Subscribe(ctx, []string{"ns"}, "track", WithFilter(NextObject()))
		require.NoError(t, err)
		assert.Nil(t, track.Fill(), "no fill was asked for")

		fill, err := track.Refill(FillRange(AbsoluteRange(Location{Group: 2}, Location{Group: 3, Object: MaxObjectID})))
		require.NoError(t, err)
		request := <-fills
		assert.Equal(t, fill.RequestID(), request.RequestID())
		assert.NotEqual(t, track.RequestID(), fill.RequestID(), "a REQUEST_UPDATE has a Request ID of its own")
		assert.Equal(t, []Location{{Group: 2}, {Group: 2, Object: 1}, {Group: 3}, {Group: 3, Object: 1}},
			locations(drainFill(t, ctx, fill)))
	})
}

func TestFillIsDraft22Only(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		server := &Session{
			SubscribeHandler: SubscribeHandlerFunc(func(r *SubscribeRequest) { _, _ = r.Accept() }),
		}
		client := &Session{}
		runSessions(t, client, server)

		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		_, err := client.Subscribe(ctx, []string{"ns"}, "track", WithFill())
		assert.ErrorIs(t, err, ErrNotInThisDraft)

		track, err := client.Subscribe(ctx, []string{"ns"}, "track")
		require.NoError(t, err)
		_, err = track.Refill()
		assert.ErrorIs(t, err, ErrNotInThisDraft)
	})
}

// A fill stream is one of the subscription's data streams, and PUBLISH_DONE's
// Stream Count includes it (draft-22 Section 9.9), which is what lets the
// subscriber end delivery as soon as it has seen it.
func TestFillCountsTowardPublishDone(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		filled := make(chan struct{})
		server := &Session{
			SubscribeHandler: SubscribeHandlerFunc(func(r *SubscribeRequest) {
				sub, err := r.Accept(WithLargestObject(Location{Group: 1}))
				if err != nil {
					return
				}
				<-filled
				_ = sub.Close(PublishDoneTrackEnded, "done")
			}),
			FillHandler: FillHandlerFunc(func(r *FillRequest) {
				assert.NoError(t, serveRange(r, 1))
				close(filled)
			}),
		}
		client := &Session{}
		runSessionsVersion(t, wire2.Version22, client, server)

		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		track, err := client.Subscribe(ctx, []string{"ns"}, "track", WithFill(FillRange(RelativeStart(1))))
		require.NoError(t, err)
		assert.Len(t, drainFill(t, ctx, track.Fill()), 1)

		start := time.Now()
		_, err = track.ReadObject(ctx)
		require.Error(t, err)
		done, ok := track.PublishDone()
		require.True(t, ok)
		assert.Equal(t, uint64(1), done.StreamCount)
		assert.Less(t, time.Since(start), publishDoneGrace, "delivery ended on the count, not the grace period")
	})
}

// Cancelling the subscription resets its fill streams; closing a fill leaves
// the subscription alone.
func TestFillEndsWithTheSubscription(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		writeAfterCancel := make(chan error, 1)
		server := &Session{
			SubscribeHandler: SubscribeHandlerFunc(func(r *SubscribeRequest) {
				_, _ = r.Accept(WithLargestObject(Location{Group: 1}))
			}),
			FillHandler: FillHandlerFunc(func(r *FillRequest) {
				response, err := r.Accept()
				if err != nil {
					return
				}
				_ = response.WriteObject(Object{GroupID: 0, ObjectID: 0, Priority: 1, Payload: []byte("a")})
				<-r.Context().Done()
				writeAfterCancel <- response.WriteObject(Object{GroupID: 1, ObjectID: 0, Priority: 1, Payload: []byte("b")})
			}),
		}
		client := &Session{}
		runSessionsVersion(t, wire2.Version22, client, server)

		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		track, err := client.Subscribe(ctx, []string{"ns"}, "track", WithFill())
		require.NoError(t, err)
		first, err := track.Fill().ReadObject(ctx)
		require.NoError(t, err)
		assert.Equal(t, "a", string(first.Payload))

		require.NoError(t, track.Close())
		assert.ErrorIs(t, <-writeAfterCancel, errFetchResponseClosed, "the library reset the fill stream")
		_, err = track.Fill().ReadObject(ctx)
		assert.Error(t, err)
	})
}

// A fill that names no Group Order inherits the subscription's, which here is
// the publisher's preference from SUBSCRIBE_OK: the fill stream's Group ID
// deltas can only be decoded once that has arrived.
func TestFillInheritsThePublisherGroupOrder(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		server := &Session{
			SubscribeHandler: SubscribeHandlerFunc(func(r *SubscribeRequest) {
				_, _ = r.Accept(
					WithLargestObject(Location{Group: 4}),
					WithTrackProperties(KVPList{{Type: wire2.PropertyDefaultPublisherGroupOrder, ValueVarInt: uint64(GroupOrderDescending)}}),
				)
			}),
			FillHandler: FillHandlerFunc(func(r *FillRequest) {
				assert.Equal(t, GroupOrderDescending, r.GroupOrder())
				assert.NoError(t, serveRange(r, 1))
			}),
		}
		client := &Session{}
		runSessionsVersion(t, wire2.Version22, client, server)

		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		track, err := client.Subscribe(ctx, []string{"ns"}, "track", WithFill(FillRange(RelativeStart(3))))
		require.NoError(t, err)
		assert.Equal(t, []Location{{Group: 4}, {Group: 3}, {Group: 2}}, locations(drainFill(t, ctx, track.Fill())))
	})
}
