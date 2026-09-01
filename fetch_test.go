package moqtransport

import (
	"context"
	"errors"
	"testing"
	"testing/synctest"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// drainFetch reads a whole response. A FETCH is finite by construction, so
// reaching the end is the expected outcome and has an error of its own.
func drainFetch(t *testing.T, ctx context.Context, fetch *FetchStream) []*FetchObject {
	t.Helper()
	var records []*FetchObject
	for {
		record, err := fetch.ReadObject(ctx)
		if err != nil {
			require.ErrorIs(t, err, ErrFetchComplete)
			return records
		}
		records = append(records, record)
	}
}

func TestFetchEndToEnd(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		server := &Session{
			FetchHandler: FetchHandlerFunc(func(r *FetchRequest) {
				if r.Track() != "video0" {
					_ = r.Reject(RequestErrorDoesNotExist, "no such track")
					return
				}
				start, end := r.Range()
				assert.Equal(t, uint64(2), start.Group)
				assert.Equal(t, uint64(4), end.Group)

				response, err := r.Accept(WithEndOfTrack())
				if err != nil {
					return
				}
				for group := uint64(2); group < 4; group++ {
					for object := range uint64(2) {
						_ = response.WriteObject(Object{
							GroupID:  group,
							ObjectID: object,
							Priority: 128,
							Payload:  []byte{byte(group), byte(object)},
						})
					}
				}
				_ = response.Close()
			}),
		}
		client := &Session{}
		runSessions(t, client, server)

		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		fetch, err := client.Fetch(ctx, []string{"example.com"}, "video0",
			Location{Group: 2}, Location{Group: 4})
		require.NoError(t, err)
		assert.True(t, fetch.EndOfTrack())
		assert.Equal(t, uint64(4), fetch.EndLocation().Group)

		records := drainFetch(t, ctx, fetch)
		require.Len(t, records, 4)
		for i, record := range records {
			group := uint64(2 + i/2)
			object := uint64(i % 2)
			assert.Equal(t, group, record.GroupID, "record %d", i)
			assert.Equal(t, object, record.ObjectID, "record %d", i)
			assert.Equal(t, []byte{byte(group), byte(object)}, record.Payload, "record %d", i)
		}
	})
}

// A response with no Objects is a header and a FIN, which is a meaningful
// answer rather than an empty one: it says the range is genuinely empty.
func TestFetchEmptyRange(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
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
		runSessions(t, client, server)

		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		fetch, err := client.Fetch(ctx, []string{"ns"}, "track", Location{}, Location{Group: 1})
		require.NoError(t, err)
		assert.Empty(t, drainFetch(t, ctx, fetch))
	})
}

// An End of Range marker stands in for a run of Objects the publisher did not
// serialize, saying whether they are known absent or of unknown status.
func TestFetchEndOfRangeMarkers(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		server := &Session{
			FetchHandler: FetchHandlerFunc(func(r *FetchRequest) {
				response, err := r.Accept()
				if err != nil {
					return
				}
				_ = response.WriteObject(Object{GroupID: 1, ObjectID: 0, Priority: 1, Payload: []byte("a")})
				_ = response.WriteEndOfRange(EndOfRangeNonExistent, Location{Group: 1, Object: 5})
				_ = response.WriteEndOfRange(EndOfRangeUnknown, Location{Group: 1, Object: 9})
				_ = response.WriteObject(Object{GroupID: 1, ObjectID: 10, Priority: 1, Payload: []byte("b")})
				_ = response.Close()
			}),
		}
		client := &Session{}
		runSessions(t, client, server)

		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		fetch, err := client.Fetch(ctx, []string{"ns"}, "track", Location{Group: 1}, Location{Group: 2})
		require.NoError(t, err)

		records := drainFetch(t, ctx, fetch)
		require.Len(t, records, 4)
		assert.Equal(t, EndOfRange(0), records[0].EndOfRange)
		assert.Equal(t, EndOfRangeNonExistent, records[1].EndOfRange)
		assert.Equal(t, uint64(5), records[1].ObjectID)
		assert.Equal(t, EndOfRangeUnknown, records[2].EndOfRange)
		assert.Equal(t, uint64(9), records[2].ObjectID)
		assert.Equal(t, "b", string(records[3].Payload))
		assert.Equal(t, uint64(10), records[3].ObjectID)
	})
}

func TestFetchRejected(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		server := &Session{
			FetchHandler: FetchHandlerFunc(func(r *FetchRequest) {
				_ = r.Reject(RequestErrorInvalidRange, "out of range")
			}),
		}
		client := &Session{}
		runSessions(t, client, server)

		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		_, err := client.Fetch(ctx, []string{"ns"}, "track", Location{}, Location{Group: 1})
		var reqErr *RequestError
		require.ErrorAs(t, err, &reqErr)
		assert.Equal(t, RequestErrorInvalidRange, reqErr.Code)
	})
}

func TestFetchWithoutHandler(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		client, server := &Session{}, &Session{}
		runSessions(t, client, server)

		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		_, err := client.Fetch(ctx, []string{"ns"}, "track", Location{}, Location{Group: 1})
		var reqErr *RequestError
		require.ErrorAs(t, err, &reqErr)
		assert.Equal(t, RequestErrorNotSupported, reqErr.Code)
	})
}

// A Joining FETCH names a subscription rather than a range, and the publisher
// works the range out so that what the fetch returns and what the subscription
// delivers are contiguous and do not overlap.
func TestJoiningFetchResolvesTheRange(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ranges := make(chan [2]Location, 2)
		server := &Session{
			SubscribeHandler: SubscribeHandlerFunc(func(r *SubscribeRequest) {
				// The Largest Location reported here is the Joining Location the
				// fetch will end at.
				_, _ = r.Accept(WithLargestObject(Location{Group: 10, Object: 3}))
			}),
			FetchHandler: FetchHandlerFunc(func(r *FetchRequest) {
				start, end := r.Range()
				ranges <- [2]Location{start, end}
				assert.Equal(t, "video0", r.Track())
				assert.Equal(t, []string{"ns"}, r.Namespace())
				response, err := r.Accept()
				if err != nil {
					return
				}
				_ = response.Close()
			}),
		}
		client := &Session{}
		runSessions(t, client, server)

		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		track, err := client.Subscribe(ctx, []string{"ns"}, "video0")
		require.NoError(t, err)

		// Two groups back from the joining location.
		_, err = client.FetchRelative(ctx, track, 2)
		require.NoError(t, err)
		got := <-ranges
		assert.Equal(t, Location{Group: 8}, got[0])
		assert.Equal(t, Location{Group: 10, Object: 4}, got[1],
			"the end is one past the joining location, so the two are contiguous")

		// From an explicit group.
		_, err = client.FetchAbsolute(ctx, track, 3)
		require.NoError(t, err)
		got = <-ranges
		assert.Equal(t, Location{Group: 3}, got[0])
		assert.Equal(t, Location{Group: 10, Object: 4}, got[1])
	})
}

// The ways a Joining FETCH cannot be resolved each have their own error code,
// and the handler never sees the request.
func TestJoiningFetchErrors(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		fetched := make(chan struct{}, 1)
		server := &Session{
			SubscribeHandler: SubscribeHandlerFunc(func(r *SubscribeRequest) {
				if r.Track() == "empty" {
					// No Largest Object: nothing has been published on the track.
					_, _ = r.Accept()
					return
				}
				_, _ = r.Accept(WithLargestObject(Location{Group: 4, Object: 1}))
			}),
			FetchHandler: FetchHandlerFunc(func(r *FetchRequest) {
				select {
				case fetched <- struct{}{}:
				default:
				}
				_ = r.Reject(RequestErrorInternal, "should not be reached")
			}),
		}
		client := &Session{}
		runSessions(t, client, server)

		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		track, err := client.Subscribe(ctx, []string{"ns"}, "video0")
		require.NoError(t, err)

		// Going further back than the track goes.
		_, err = client.FetchRelative(ctx, track, 99)
		var reqErr *RequestError
		require.ErrorAs(t, err, &reqErr)
		assert.Equal(t, RequestErrorInvalidRange, reqErr.Code)

		// Starting after the joining location.
		_, err = client.FetchAbsolute(ctx, track, 99)
		require.ErrorAs(t, err, &reqErr)
		assert.Equal(t, RequestErrorInvalidRange, reqErr.Code)

		// A track with nothing published on it has no joining location to end at.
		empty, err := client.Subscribe(ctx, []string{"ns"}, "empty")
		require.NoError(t, err)
		_, err = client.FetchRelative(ctx, empty, 1)
		require.ErrorAs(t, err, &reqErr)
		assert.Equal(t, RequestErrorInvalidRange, reqErr.Code)

		// A subscription that has ended is no longer there to join. The close is
		// a stream reset with no answer, so poll until the server has processed
		// it; a poll that arrives first still resolves and legitimately reaches
		// the handler, which is why those hits are drained before the assertion.
		require.NoError(t, track.Close())
		require.Eventually(t, func() bool {
			_, err := client.FetchRelative(ctx, track, 1)
			var e *RequestError
			return errors.As(err, &e) && e.Code == RequestErrorInvalidJoiningRequestID
		}, 2*time.Second, 10*time.Millisecond)
		for len(fetched) > 0 {
			<-fetched
		}

		// Now that the subscription is provably gone, an unresolvable joining
		// fetch must be rejected by the library without reaching the handler.
		_, err = client.FetchRelative(ctx, track, 1)
		require.ErrorAs(t, err, &reqErr)
		assert.Equal(t, RequestErrorInvalidJoiningRequestID, reqErr.Code)
		select {
		case <-fetched:
			t.Fatal("an unresolvable joining fetch reached the handler")
		default:
		}
	})
}

// The subscriber cancelling a fetch is a STOP_SENDING on its request stream,
// which is what lets the publisher destroy its state.
func TestFetchCancel(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		gone := make(chan struct{}, 1)
		server := &Session{
			FetchHandler: FetchHandlerFunc(func(r *FetchRequest) {
				if _, err := r.Accept(); err != nil {
					return
				}
				<-r.Context().Done()
				gone <- struct{}{}
			}),
		}
		client := &Session{}
		runSessions(t, client, server)

		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		fetch, err := client.Fetch(ctx, []string{"ns"}, "track", Location{}, Location{Group: 9})
		require.NoError(t, err)
		require.NoError(t, fetch.Close())

		select {
		case <-gone:
		case <-ctx.Done():
			t.Fatal("the publisher never noticed the fetch being cancelled")
		}

		// A cancelled fetch did not complete, so it does not end that way.
		_, err = fetch.ReadObject(ctx)
		assert.Error(t, err)
		assert.NotErrorIs(t, err, ErrFetchComplete)
	})
}
