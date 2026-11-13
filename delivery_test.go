package moqtransport

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/Eyevinn/moqtransport/internal/wire2"
	"github.com/mengelbart/qlog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestSubscribeFastEnd covers two races a publisher that accepts, serves and
// closes in one breath used to expose: awaitEstablished returning "peer
// finished its half of the request stream" although SUBSCRIBE_OK arrived, and
// PUBLISH_DONE overtaking the object still on its subgroup stream. Both must
// now be deterministic: Subscribe succeeds, the object arrives, then the end.
func TestSubscribeFastEnd(t *testing.T) {
	forEachVersion(t, func(t *testing.T, version wire2.Version) {
		server := &Session{
			SubscribeHandler: SubscribeHandlerFunc(func(r *SubscribeRequest) {
				sub, err := r.Accept(WithLargestObject(Location{Group: 1, Object: 0}))
				if err != nil {
					return
				}
				sg, err := sub.OpenSubgroup(1, 0, 128)
				if err != nil {
					return
				}
				_, _ = sg.WriteObject(0, []byte("only"))
				_ = sg.Close()
				_ = sub.Close(PublishDoneTrackEnded, "all done")
			}),
		}
		client := &Session{}
		runSessionsVersion(t, version, client, server)

		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		for i := range 40 {
			track, err := client.Subscribe(ctx, []string{"fast"}, fmt.Sprintf("track%d", i))
			require.NoError(t, err, "SUBSCRIBE_OK must win over the request stream ending")

			obj, err := track.ReadObject(ctx)
			require.NoError(t, err, "the object must not be lost to PUBLISH_DONE overtaking its stream")
			assert.Equal(t, "only", string(obj.Payload))

			_, err = track.ReadObject(ctx)
			require.Error(t, err)
			done, ok := track.PublishDone()
			require.True(t, ok)
			assert.Equal(t, PublishDoneTrackEnded, done.Code)
		}
	})
}

// TestSubgroupEndEvents: with Session.SubgroupEndEvents set, a subgroup
// stream's end is delivered as a synthetic Object -- EndsSubgroup for a FIN,
// SubgroupReset for a reset -- and the header's END_OF_GROUP bit reaches
// every Object.
func TestSubgroupEndEvents(t *testing.T) {
	forEachVersion(t, func(t *testing.T, version wire2.Version) {
		published := make(chan *Subscription, 2)
		server := &Session{
			SubscribeHandler: SubscribeHandlerFunc(func(r *SubscribeRequest) {
				sub, err := r.Accept()
				if err != nil {
					return
				}
				published <- sub
			}),
		}
		client := &Session{SubgroupEndEvents: true}
		runSessionsVersion(t, version, client, server)

		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		track, err := client.Subscribe(ctx, []string{"events"}, "track")
		require.NoError(t, err)
		sub := <-published

		// A finished subgroup, with the END_OF_GROUP bit set.
		sg, err := sub.OpenSubgroup(1, 0, 128, WithEndOfGroup())
		require.NoError(t, err)
		_, err = sg.WriteObject(0, []byte("payload"))
		require.NoError(t, err)

		obj, err := track.ReadObject(ctx)
		require.NoError(t, err)
		assert.Equal(t, "payload", string(obj.Payload))
		assert.True(t, obj.EndOfGroup, "the header's END_OF_GROUP bit reaches the Object")
		assert.False(t, obj.EndsSubgroup)

		require.NoError(t, sg.Close())
		marker, err := track.ReadObject(ctx)
		require.NoError(t, err)
		assert.True(t, marker.EndsSubgroup)
		assert.False(t, marker.SubgroupReset)
		assert.True(t, marker.EndOfGroup)
		assert.Equal(t, uint64(1), marker.GroupID)
		assert.Equal(t, uint64(0), marker.SubgroupID)
		assert.Empty(t, marker.Payload)

		// A reset subgroup. The object is read before the reset goes out, so the
		// reset cannot discard it in flight.
		sg2, err := sub.OpenSubgroup(2, 0, 128)
		require.NoError(t, err)
		_, err = sg2.WriteObject(0, []byte("doomed"))
		require.NoError(t, err)
		obj, err = track.ReadObject(ctx)
		require.NoError(t, err)
		assert.Equal(t, "doomed", string(obj.Payload))
		assert.False(t, obj.EndOfGroup)

		sg2.Reset(StreamErrorTooFarBehind)
		marker, err = track.ReadObject(ctx)
		require.NoError(t, err)
		assert.True(t, marker.SubgroupReset)
		assert.False(t, marker.EndsSubgroup)
		assert.Equal(t, uint64(2), marker.GroupID)
	})
}

// TestSubgroupEndEventsOffByDefault: without the session flag, subgroup ends
// stay silent, exactly as before.
func TestSubgroupEndEventsOffByDefault(t *testing.T) {
	forEachVersion(t, func(t *testing.T, version wire2.Version) {
		server := &Session{
			SubscribeHandler: SubscribeHandlerFunc(func(r *SubscribeRequest) {
				sub, err := r.Accept()
				if err != nil {
					return
				}
				sg, err := sub.OpenSubgroup(1, 0, 128)
				if err != nil {
					return
				}
				_, _ = sg.WriteObject(0, []byte("only"))
				_ = sg.Close()
				_ = sub.Close(PublishDoneTrackEnded, "done")
			}),
		}
		client := &Session{}
		runSessionsVersion(t, version, client, server)

		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		track, err := client.Subscribe(ctx, []string{"quiet"}, "track")
		require.NoError(t, err)
		obj, err := track.ReadObject(ctx)
		require.NoError(t, err)
		assert.Equal(t, "only", string(obj.Payload))
		_, err = track.ReadObject(ctx)
		require.Error(t, err, "no marker: the next read reports the subscription's end")
	})
}

// TestFetchTrackProperties: the FETCH_OK's Track Properties are readable on
// the FetchStream, so a proxying relay can forward them.
func TestFetchTrackProperties(t *testing.T) {
	forEachVersion(t, func(t *testing.T, version wire2.Version) {
		props := KVPList{{Type: wire2.PropertyDefaultPublisherPriority, ValueVarInt: 42}}
		server := &Session{
			FetchHandler: FetchHandlerFunc(func(r *FetchRequest) {
				response, err := r.Accept(WithFetchTrackProperties(props), WithEndOfTrack())
				if err != nil {
					return
				}
				_ = response.WriteObject(Object{GroupID: 0, ObjectID: 0, Priority: 128, Payload: []byte("x")})
				_ = response.Close()
			}),
		}
		client := &Session{}
		runSessionsVersion(t, version, client, server)

		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		fs, err := client.Fetch(ctx, []string{"fetch"}, "track", AbsoluteRange(Location{}, Location{}))
		require.NoError(t, err)
		assert.Equal(t, props, fs.TrackProperties())
		assert.True(t, fs.EndOfTrack())

		_, err = fs.ReadObject(ctx)
		require.NoError(t, err)
		_, err = fs.ReadObject(ctx)
		require.ErrorIs(t, err, ErrFetchComplete)
	})
}

// countingQlogHandler is a QlogHandler that is not a *qlog.Logger.
type countingQlogHandler struct {
	events atomic.Int64
}

func (c *countingQlogHandler) Log(qlog.Event) { c.events.Add(1) }

// TestQlogHandlerInterface: any QlogHandler can stand in for *qlog.Logger and
// sees the session's events before serialization.
func TestQlogHandlerInterface(t *testing.T) {
	forEachVersion(t, func(t *testing.T, version wire2.Version) {
		counter := &countingQlogHandler{}
		client := &Session{Qlogger: counter}
		server := &Session{}
		runSessionsVersion(t, version, client, server)

		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, err := client.Subscribe(ctx, []string{"nowhere"}, "nothing")
		require.Error(t, err) // nil handler rejects with NOT_SUPPORTED

		assert.Positive(t, counter.events.Load())
	})
}

// TestFetchFastEnd is the FETCH flavor of the fast-end race: a publisher that
// accepts and closes an empty response in one breath completes the fetch
// while FETCH_OK is still being awaited, and the answer must win.
func TestFetchFastEnd(t *testing.T) {
	forEachVersion(t, func(t *testing.T, version wire2.Version) {
		server := &Session{
			FetchHandler: FetchHandlerFunc(func(r *FetchRequest) {
				response, err := r.Accept()
				if err != nil {
					return
				}
				_ = response.Close()
			}),
		}
		client := &Session{}
		runSessionsVersion(t, version, client, server)

		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		for range 40 {
			fetch, err := client.Fetch(ctx, []string{"fast"}, "track",
				AbsoluteRange(Location{}, Location{Object: MaxObjectID}))
			require.NoError(t, err, "FETCH_OK must win over the response completing")
			_, err = fetch.ReadObject(ctx)
			require.ErrorIs(t, err, ErrFetchComplete)
		}
	})
}

// TestPublishDoneGraceTimeout: a PUBLISH_DONE whose Stream Count promises a
// data stream that never finishes ends delivery after the grace period
// rather than holding the subscription open forever. Under synctest the
// grace second is fake time, so the test is instant and the timing exact.
func TestPublishDoneGraceTimeout(t *testing.T) {
	forEachVersion(t, func(t *testing.T, version wire2.Version) {
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
		runSessionsVersion(t, version, client, server)

		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		track, err := client.Subscribe(ctx, []string{"grace"}, "track")
		require.NoError(t, err)
		sub := <-published

		// One subgroup is opened (and so counted), delivers an object, and is
		// never finished: its end is what the PUBLISH_DONE below promises and
		// the subscriber never gets.
		sg, err := sub.OpenSubgroup(1, 0, 128)
		require.NoError(t, err)
		_, err = sg.WriteObject(0, []byte("delivered"))
		require.NoError(t, err)
		require.NoError(t, sub.Close(PublishDoneTrackEnded, "promises one finished stream"))

		obj, err := track.ReadObject(ctx)
		require.NoError(t, err)
		assert.Equal(t, "delivered", string(obj.Payload))

		// The end arrives only when the grace gives up on the missing FIN.
		start := time.Now()
		_, err = track.ReadObject(ctx)
		require.Error(t, err)
		assert.Equal(t, publishDoneGrace, time.Since(start))
		done, ok := track.PublishDone()
		require.True(t, ok)
		assert.Equal(t, PublishDoneTrackEnded, done.Code)

		_ = sg.Close()
	})
}

// TestTrackAliasWaitStopsUnknownStream: a data stream naming an alias no
// subscription uses is held for trackAliasWait -- long enough for a slow
// SUBSCRIBE_OK -- and then stopped rather than parked forever.
func TestTrackAliasWaitStopsUnknownStream(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		client := &Session{}
		server := &Session{}
		clientConn, serverConn := newMemConnPair(wire2.Version18.ALPN())

		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		errs := make(chan error, 2)
		go func() { errs <- client.Run(ctx, clientConn) }()
		go func() { errs <- server.Run(ctx, serverConn) }()
		require.NoError(t, <-errs)
		require.NoError(t, <-errs)
		defer func() {
			_ = client.Close(SessionErrorNoError, "test over")
			_ = server.Close(SessionErrorNoError, "test over")
		}()

		stream, err := clientConn.OpenUniStream()
		require.NoError(t, err)
		header, err := wire2.AppendSubgroupHeader(nil, &wire2.SubgroupHeader{
			TrackAlias:     99,
			GroupID:        1,
			SubgroupIDMode: wire2.SubgroupIDExplicit,
			Priority:       128,
		})
		require.NoError(t, err)
		_, err = stream.Write(header)
		require.NoError(t, err)

		// Just before the wait elapses the stream is still being held open.
		time.Sleep(trackAliasWait - time.Millisecond)
		synctest.Wait()
		_, err = stream.Write([]byte{0})
		require.NoError(t, err)

		// Once it has elapsed, the server has stopped the stream, which a
		// sender sees as its writes failing.
		time.Sleep(2 * time.Millisecond)
		synctest.Wait()
		_, err = stream.Write([]byte{0})
		require.Error(t, err)
	})
}

// readAvailable reads the Objects a track has buffered, stopping at the first
// read that would block.
func readAvailable(t *testing.T, track *RemoteTrack) []string {
	t.Helper()
	var payloads []string
	for {
		ctx, cancel := context.WithTimeout(t.Context(), time.Millisecond)
		o, err := track.ReadObject(ctx)
		cancel()
		if err != nil {
			return payloads
		}
		payloads = append(payloads, string(o.Payload))
	}
}

// TestSubscriptionsToOneTrackKeepTheirObjects: two subscriptions on one
// session to the same track each open their own data streams, and each
// subscriber sees only its own subscription's Objects.
func TestSubscriptionsToOneTrackKeepTheirObjects(t *testing.T) {
	forEachVersion(t, func(t *testing.T, version wire2.Version) {
		subs := make(chan *Subscription, 2)
		server := &Session{SubscribeHandler: SubscribeHandlerFunc(func(r *SubscribeRequest) {
			if sub, err := r.Accept(); err == nil {
				subs <- sub
			}
		})}
		client := &Session{}
		runSessionsVersion(t, version, client, server)

		first, err := client.Subscribe(t.Context(), []string{"ns"}, "track")
		require.NoError(t, err)
		firstSub := <-subs
		second, err := client.Subscribe(t.Context(), []string{"ns"}, "track")
		require.NoError(t, err)
		secondSub := <-subs
		assert.NotEqual(t, first.TrackAlias(), second.TrackAlias())

		for _, w := range []struct {
			sub     *Subscription
			payload string
		}{{firstSub, "first"}, {secondSub, "second"}} {
			sg, err := w.sub.OpenSubgroup(1, 0, 128)
			require.NoError(t, err)
			_, err = sg.WriteObject(0, []byte(w.payload))
			require.NoError(t, err)
			require.NoError(t, sg.Close())
		}
		synctest.Wait()
		assert.Equal(t, []string{"first"}, readAvailable(t, first))
		assert.Equal(t, []string{"second"}, readAvailable(t, second))
	})
}

// TestSharedAliasStreamWaitsForPendingSubscribe: a publisher may give a
// second subscription to a track the alias the first one has (draft-22
// Section 3.1.3). A data stream that overtakes the second SUBSCRIBE_OK then
// cannot be told apart from one of the first subscription's, so it waits for
// that answer and goes to both.
func TestSharedAliasStreamWaitsForPendingSubscribe(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		reqs := make(chan *SubscribeRequest, 2)
		server := &Session{SubscribeHandler: SubscribeHandlerFunc(func(r *SubscribeRequest) {
			reqs <- r
		})}
		client := &Session{}
		clientConn, serverConn := newMemConnPair(wire2.Version22.ALPN())
		errs := make(chan error, 2)
		go func() { errs <- client.Run(t.Context(), clientConn) }()
		go func() { errs <- server.Run(t.Context(), serverConn) }()
		require.NoError(t, <-errs)
		require.NoError(t, <-errs)
		defer func() {
			_ = client.Close(SessionErrorNoError, "test over")
			_ = server.Close(SessionErrorNoError, "test over")
		}()

		go func() { _, _ = (<-reqs).Accept() }()
		first, err := client.Subscribe(t.Context(), []string{"ns"}, "track")
		require.NoError(t, err)

		answered := make(chan *RemoteTrack)
		go func() {
			second, err := client.Subscribe(t.Context(), []string{"ns"}, "track")
			assert.NoError(t, err)
			answered <- second
		}()
		req := <-reqs

		// The stream goes out under the shared alias before SUBSCRIBE_OK.
		stream, err := serverConn.OpenUniStream()
		require.NoError(t, err)
		sg, err := newSubgroup(stream, &wire2.SubgroupHeader{
			TrackAlias:     first.TrackAlias(),
			GroupID:        1,
			SubgroupIDMode: wire2.SubgroupIDExplicit,
			Priority:       128,
		}, qlogger{}, nil)
		require.NoError(t, err)
		_, err = sg.WriteObject(0, []byte("shared"))
		require.NoError(t, err)
		require.NoError(t, sg.Close())
		synctest.Wait()

		// Answer as a publisher that shares one alias per track.
		server.mu.Lock()
		server.nextTrackAlias = first.TrackAlias()
		server.mu.Unlock()
		_, err = req.Accept()
		require.NoError(t, err)
		second := <-answered
		require.Equal(t, first.TrackAlias(), second.TrackAlias())

		synctest.Wait()
		assert.Equal(t, []string{"shared"}, readAvailable(t, first))
		assert.Equal(t, []string{"shared"}, readAvailable(t, second))
	})
}

// TestDatagramForUnknownAliasIsDropped: a datagram under an alias no
// subscription uses is dropped, and the datagrams behind it still arrive.
func TestDatagramForUnknownAliasIsDropped(t *testing.T) {
	forEachVersion(t, func(t *testing.T, version wire2.Version) {
		subs := make(chan *Subscription, 1)
		server := &Session{SubscribeHandler: SubscribeHandlerFunc(func(r *SubscribeRequest) {
			if sub, err := r.Accept(); err == nil {
				subs <- sub
			}
		})}
		client := &Session{}
		runSessionsVersion(t, version, client, server)

		track, err := client.Subscribe(t.Context(), []string{"ns"}, "track")
		require.NoError(t, err)
		sub := <-subs

		stray, err := wire2.AppendObjectDatagram(nil, &wire2.ObjectDatagram{
			TrackAlias: track.TrackAlias() + 1,
			GroupID:    1,
			Payload:    []byte("stray"),
		})
		require.NoError(t, err)
		require.NoError(t, server.conn.SendDatagram(stray))
		require.NoError(t, sub.SendDatagram(Object{GroupID: 1, Payload: []byte("known")}))

		synctest.Wait()
		assert.Equal(t, []string{"known"}, readAvailable(t, track))
	})
}

// TestOversizedObjectStopsOnlyItsStream: an Object over MaxObjectSize stops
// the stream carrying it with EXCESSIVE_LOAD. The limit is local policy, not a
// protocol violation, so the session and the subscription carry on.
func TestOversizedObjectStopsOnlyItsStream(t *testing.T) {
	forEachVersion(t, func(t *testing.T, version wire2.Version) {
		subs := make(chan *Subscription, 1)
		server := &Session{SubscribeHandler: SubscribeHandlerFunc(func(r *SubscribeRequest) {
			if sub, err := r.Accept(); err == nil {
				subs <- sub
			}
		})}
		client := &Session{MaxObjectSize: 8}
		runSessionsVersion(t, version, client, server)

		track, err := client.Subscribe(t.Context(), []string{"ns"}, "track")
		require.NoError(t, err)
		sub := <-subs

		big, err := sub.OpenSubgroup(1, 0, 128)
		require.NoError(t, err)
		_, err = big.WriteObject(0, []byte("more than eight bytes"))
		require.NoError(t, err)
		synctest.Wait()
		_, err = big.WriteObject(1, []byte("x"))
		var stopped *memStreamError
		require.ErrorAs(t, err, &stopped, "the stream is stopped")
		assert.Equal(t, uint32(StreamErrorExcessiveLoad), stopped.code)

		small, err := sub.OpenSubgroup(2, 0, 128)
		require.NoError(t, err)
		_, err = small.WriteObject(0, []byte("eight b."))
		require.NoError(t, err)
		require.NoError(t, small.Close())
		synctest.Wait()
		assert.Equal(t, []string{"eight b."}, readAvailable(t, track))
		assert.NoError(t, client.ctx.Err(), "the session is still up")
	})
}
