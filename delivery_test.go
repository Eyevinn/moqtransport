package moqtransport

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
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
	runSessions(t, client, server)

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
}

// TestSubgroupEndEvents: with Session.SubgroupEndEvents set, a subgroup
// stream's end is delivered as a synthetic Object -- EndsSubgroup for a FIN,
// SubgroupReset for a reset -- and the header's END_OF_GROUP bit reaches
// every Object.
func TestSubgroupEndEvents(t *testing.T) {
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
	runSessions(t, client, server)

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
}

// TestSubgroupEndEventsOffByDefault: without the session flag, subgroup ends
// stay silent, exactly as before.
func TestSubgroupEndEventsOffByDefault(t *testing.T) {
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
	runSessions(t, client, server)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	track, err := client.Subscribe(ctx, []string{"quiet"}, "track")
	require.NoError(t, err)
	obj, err := track.ReadObject(ctx)
	require.NoError(t, err)
	assert.Equal(t, "only", string(obj.Payload))
	_, err = track.ReadObject(ctx)
	require.Error(t, err, "no marker: the next read reports the subscription's end")
}

// TestFetchTrackProperties: the FETCH_OK's Track Properties are readable on
// the FetchStream, so a proxying relay can forward them.
func TestFetchTrackProperties(t *testing.T) {
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
	runSessions(t, client, server)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	fs, err := client.Fetch(ctx, []string{"fetch"}, "track",
		Location{Group: 0, Object: 0}, Location{Group: 0, Object: 1})
	require.NoError(t, err)
	assert.Equal(t, props, fs.TrackProperties())
	assert.True(t, fs.EndOfTrack())

	_, err = fs.ReadObject(ctx)
	require.NoError(t, err)
	_, err = fs.ReadObject(ctx)
	require.ErrorIs(t, err, ErrFetchComplete)
}

// countingQlogHandler is a QlogHandler that is not a *qlog.Logger.
type countingQlogHandler struct {
	events atomic.Int64
}

func (c *countingQlogHandler) Log(qlog.Event) { c.events.Add(1) }

// TestQlogHandlerInterface: any QlogHandler can stand in for *qlog.Logger and
// sees the session's events before serialization.
func TestQlogHandlerInterface(t *testing.T) {
	counter := &countingQlogHandler{}
	client := &Session{Qlogger: counter}
	server := &Session{}
	runSessions(t, client, server)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := client.Subscribe(ctx, []string{"nowhere"}, "nothing")
	require.Error(t, err) // nil handler rejects with NOT_SUPPORTED

	assert.Positive(t, counter.events.Load())
}
