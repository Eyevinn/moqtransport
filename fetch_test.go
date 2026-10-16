package moqtransport

import (
	"context"
	"errors"
	"testing"
	"testing/synctest"
	"time"

	"github.com/Eyevinn/moqtransport/internal/wire2"
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

// withFetchParameter adds a raw parameter to an outgoing FETCH, for the
// parameters the public options do not cover.
func withFetchParameter(p wire2.Parameter) FetchOption {
	return func(f *wire2.Fetch) error {
		f.Parameters = append(f.Parameters, p)
		return nil
	}
}

// The same request and the same answer in both drafts, though draft-18 sends
// the range as Start and End Locations, the End one past the last Object, and
// draft-22 sends it as a LOCATION_FILTER whose end is inclusive.
func TestFetchEndToEnd(t *testing.T) {
	forEachVersion(t, func(t *testing.T, version wire2.Version) {
		server := &Session{
			FetchHandler: FetchHandlerFunc(func(r *FetchRequest) {
				if r.Track() != "video0" {
					_ = r.Reject(RequestErrorDoesNotExist, "no such track")
					return
				}
				assert.Equal(t, AbsoluteRange(Location{Group: 2}, Location{Group: 3, Object: MaxObjectID}), r.Filter())
				rng, ok := r.Range(Location{Group: 9, Object: 4}, true)
				require.True(t, ok)
				assert.Equal(t, LocationRange{Start: Location{Group: 2}, End: Location{Group: 3, Object: MaxObjectID}}, rng)

				response, err := r.Accept(WithEndOfTrack(), WithFetchEndLocation(Location{Group: 3, Object: 1}))
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
		runSessionsVersion(t, version, client, server)

		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		fetch, err := client.Fetch(ctx, []string{"example.com"}, "video0",
			AbsoluteRange(Location{Group: 2}, Location{Group: 3, Object: MaxObjectID}))
		require.NoError(t, err)
		assert.True(t, fetch.EndOfTrack())
		assert.Equal(t, Location{Group: 3, Object: 1}, fetch.EndLocation(), "the End Location is inclusive in both drafts")

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

// A range with an open end, or none at all, is resolved by the publisher
// against its Largest Object. Until it is, there is no End Location for
// FETCH_OK, and the draft forbids sending one before it is known.
func TestFetchOpenEndIsResolvedByThePublisher(t *testing.T) {
	forEachVersion(t, func(t *testing.T, version wire2.Version) {
		accepts := make(chan error, 2)
		server := &Session{
			FetchHandler: FetchHandlerFunc(func(r *FetchRequest) {
				// Draft-18 has no open end on the wire; an open-ended request
				// arrives reaching the last Group there could be, and
				// accepting it unresolved would echo that.
				if version == wire2.Version22 {
					_, err := r.Accept()
					accepts <- err
				}
				if _, ok := r.Range(Location{Group: 7, Object: 2}, true); !ok {
					_ = r.Reject(RequestErrorInvalidRange, "empty")
					return
				}
				response, err := r.Accept()
				accepts <- err
				if err == nil {
					_ = response.Close()
				}
			}),
		}
		client := &Session{}
		runSessionsVersion(t, version, client, server)

		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		fetch, err := client.Fetch(ctx, []string{"ns"}, "track", AbsoluteStart(Location{Group: 5}))
		require.NoError(t, err)
		if version == wire2.Version22 {
			assert.ErrorIs(t, <-accepts, errFetchEndLocationUnknown)
		}
		require.NoError(t, <-accepts)
		assert.Equal(t, Location{Group: 7, Object: 2}, fetch.EndLocation(), "clamped to the Largest Object")
	})
}

// Draft-22's FETCH can be relative to the Largest Object, which only the
// publisher knows. Draft-18's cannot, and the request fails before it is
// sent rather than asking for something else.
func TestFetchRelativeStart(t *testing.T) {
	forEachVersion(t, func(t *testing.T, version wire2.Version) {
		ranges := make(chan LocationRange, 1)
		server := &Session{
			FetchHandler: FetchHandlerFunc(func(r *FetchRequest) {
				rng, ok := r.Range(Location{Group: 10, Object: 3}, true)
				require.True(t, ok)
				ranges <- rng
				response, err := r.Accept()
				if err == nil {
					_ = response.Close()
				}
			}),
		}
		client := &Session{}
		runSessionsVersion(t, version, client, server)

		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		fetch, err := client.Fetch(ctx, []string{"ns"}, "track", RelativeStart(2))
		if version == wire2.Version18 {
			assert.Error(t, err)
			return
		}
		require.NoError(t, err)
		assert.Equal(t, LocationRange{Start: Location{Group: 9}, End: Location{Group: 10, Object: 3}}, <-ranges)
		assert.Equal(t, Location{Group: 10, Object: 3}, fetch.EndLocation())
	})
}

// A response with no Objects is a header and a FIN, which is a meaningful
// answer rather than an empty one: it says the range is genuinely empty.
func TestFetchEmptyRange(t *testing.T) {
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

		fetch, err := client.Fetch(ctx, []string{"ns"}, "track", AbsoluteRange(Location{}, Location{Object: MaxObjectID}))
		require.NoError(t, err)
		assert.Empty(t, drainFetch(t, ctx, fetch))
	})
}

// An End of Range marker stands in for a run of Objects the publisher did not
// serialize, saying whether they are known absent, of unknown status, or --
// in draft-22 -- abandoned when the Fill Timeout ran out.
func TestFetchEndOfRangeMarkers(t *testing.T) {
	forEachVersion(t, func(t *testing.T, version wire2.Version) {
		timedOut := make(chan error, 1)
		server := &Session{
			FetchHandler: FetchHandlerFunc(func(r *FetchRequest) {
				response, err := r.Accept()
				if err != nil {
					return
				}
				_ = response.WriteObject(Object{GroupID: 1, ObjectID: 0, Priority: 1, Payload: []byte("a")})
				_ = response.WriteEndOfRange(EndOfRangeNonExistent, Location{Group: 1, Object: 5})
				_ = response.WriteEndOfRange(EndOfRangeUnknown, Location{Group: 1, Object: 9})
				timedOut <- response.WriteEndOfRange(EndOfRangeTimedOut, Location{Group: 1, Object: 9})
				_ = response.WriteObject(Object{GroupID: 1, ObjectID: 10, Priority: 1, Payload: []byte("b")})
				_ = response.Close()
			}),
		}
		client := &Session{}
		runSessionsVersion(t, version, client, server)

		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		fetch, err := client.Fetch(ctx, []string{"ns"}, "track", AbsoluteRange(Location{Group: 1}, Location{Group: 1, Object: MaxObjectID}))
		require.NoError(t, err)

		records := drainFetch(t, ctx, fetch)
		if version == wire2.Version18 {
			assert.Error(t, <-timedOut, "draft-18 has no End of Timed-Out Range")
		} else {
			require.NoError(t, <-timedOut)
		}
		var kinds []EndOfRange
		for _, r := range records {
			kinds = append(kinds, r.EndOfRange)
		}
		want := []EndOfRange{0, EndOfRangeNonExistent, EndOfRangeUnknown, 0}
		if version == wire2.Version22 {
			want = []EndOfRange{0, EndOfRangeNonExistent, EndOfRangeUnknown, EndOfRangeTimedOut, 0}
		}
		require.Equal(t, want, kinds)
		assert.Equal(t, uint64(5), records[1].ObjectID)
		assert.Equal(t, uint64(9), records[2].ObjectID)
		last := records[len(records)-1]
		assert.Equal(t, "b", string(last.Payload))
		assert.Equal(t, uint64(10), last.ObjectID)
	})
}

func TestFetchRejected(t *testing.T) {
	forEachVersion(t, func(t *testing.T, version wire2.Version) {
		server := &Session{
			FetchHandler: FetchHandlerFunc(func(r *FetchRequest) {
				_ = r.Reject(RequestErrorInvalidRange, "out of range")
			}),
		}
		client := &Session{}
		runSessionsVersion(t, version, client, server)

		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		_, err := client.Fetch(ctx, []string{"ns"}, "track", LocationFilter{})
		var reqErr *RequestError
		require.ErrorAs(t, err, &reqErr)
		assert.Equal(t, RequestErrorInvalidRange, reqErr.Code)
	})
}

func TestFetchWithoutHandler(t *testing.T) {
	forEachVersion(t, func(t *testing.T, version wire2.Version) {
		client, server := &Session{}, &Session{}
		runSessionsVersion(t, version, client, server)

		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		_, err := client.Fetch(ctx, []string{"ns"}, "track", LocationFilter{})
		var reqErr *RequestError
		require.ErrorAs(t, err, &reqErr)
		assert.Equal(t, RequestErrorNotSupported, reqErr.Code)
	})
}

// This implementation advertises no MAX_FILTER_RANGES, so any Range Filter
// exceeds the limit, which draft-22 answers with INVALID_FILTER before the
// handler sees the request.
func TestFetchRangeFilterRejected(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		server := &Session{
			FetchHandler: FetchHandlerFunc(func(r *FetchRequest) {
				t.Error("a range-filtered fetch reached the handler")
				_ = r.Reject(RequestErrorInternal, "unreachable")
			}),
		}
		client := &Session{}
		runSessionsVersion(t, wire2.Version22, client, server)

		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		_, err := client.Fetch(ctx, []string{"ns"}, "track", LocationFilter{},
			withFetchParameter(wire2.BytesParameter(wire2.ParamObjectIDFilter, []byte{0x00, 0x01})))
		var reqErr *RequestError
		require.ErrorAs(t, err, &reqErr)
		assert.Equal(t, RequestErrorInvalidFilter, reqErr.Code)
		assert.NoError(t, server.Context().Err(), "a request error, not a session error")
	})
}

// INCLUDE_PROPERTIES 0 asks for an empty Track Properties block in FETCH_OK.
func TestFetchIncludeProperties(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		props := KVPList{{Type: wire2.PropertyDefaultPublisherPriority, ValueVarInt: 42}}
		server := &Session{
			FetchHandler: FetchHandlerFunc(func(r *FetchRequest) {
				response, err := r.Accept(WithFetchTrackProperties(props), WithFetchEndLocation(Location{}))
				if err == nil {
					_ = response.Close()
				}
			}),
		}
		client := &Session{}
		runSessionsVersion(t, wire2.Version22, client, server)

		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		with, err := client.Fetch(ctx, []string{"ns"}, "track", LocationFilter{})
		require.NoError(t, err)
		assert.Equal(t, props, with.TrackProperties())

		without, err := client.Fetch(ctx, []string{"ns"}, "track", LocationFilter{},
			withFetchParameter(wire2.Uint8Parameter(wire2.ParamIncludeProperties, 0)))
		require.NoError(t, err)
		assert.Empty(t, without.TrackProperties())
	})
}

// A Joining FETCH names a subscription rather than a range, and the publisher
// works the range out so that what the fetch returns and what the subscription
// delivers are contiguous and do not overlap. Draft-18 only.
func TestJoiningFetchResolvesTheRange(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		filters := make(chan LocationFilter, 2)
		server := &Session{
			SubscribeHandler: SubscribeHandlerFunc(func(r *SubscribeRequest) {
				// The Largest Location reported here is the Joining Location the
				// fetch will end at.
				_, _ = r.Accept(WithLargestObject(Location{Group: 10, Object: 3}))
			}),
			FetchHandler: FetchHandlerFunc(func(r *FetchRequest) {
				filters <- r.Filter()
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

		// Two groups back from the joining location, ending at it, so the two
		// are contiguous.
		fetch, err := client.FetchRelative(ctx, track, 2)
		require.NoError(t, err)
		assert.Equal(t, AbsoluteRange(Location{Group: 8}, Location{Group: 10, Object: 3}), <-filters)
		assert.Equal(t, Location{Group: 10, Object: 3}, fetch.EndLocation())

		// From an explicit group.
		_, err = client.FetchAbsolute(ctx, track, 3)
		require.NoError(t, err)
		assert.Equal(t, AbsoluteRange(Location{Group: 3}, Location{Group: 10, Object: 3}), <-filters)
	})
}

// Draft-20 deleted the Joining FETCH: a draft-22 subscription asks for its
// own fill instead.
func TestJoiningFetchIsDraft18Only(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		server := &Session{
			SubscribeHandler: SubscribeHandlerFunc(func(r *SubscribeRequest) {
				_, _ = r.Accept(WithLargestObject(Location{Group: 10}))
			}),
		}
		client := &Session{}
		runSessionsVersion(t, wire2.Version22, client, server)

		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		track, err := client.Subscribe(ctx, []string{"ns"}, "video0")
		require.NoError(t, err)
		_, err = client.FetchRelative(ctx, track, 1)
		assert.ErrorIs(t, err, ErrNotInThisDraft)
		_, err = client.FetchAbsolute(ctx, track, 1)
		assert.ErrorIs(t, err, ErrNotInThisDraft)
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
	forEachVersion(t, func(t *testing.T, version wire2.Version) {
		gone := make(chan struct{}, 1)
		server := &Session{
			FetchHandler: FetchHandlerFunc(func(r *FetchRequest) {
				if _, err := r.Accept(WithFetchEndLocation(Location{Group: 9})); err != nil {
					return
				}
				<-r.Context().Done()
				gone <- struct{}{}
			}),
		}
		client := &Session{}
		runSessionsVersion(t, version, client, server)

		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		fetch, err := client.Fetch(ctx, []string{"ns"}, "track", LocationFilter{})
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
