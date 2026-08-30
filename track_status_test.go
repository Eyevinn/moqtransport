package moqtransport

import (
	"context"
	"testing"
	"time"

	"github.com/Eyevinn/moqtransport/internal/wire2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TRACK_STATUS is answered with the same parameters and Track Properties a
// SUBSCRIBE_OK would have carried, minus the Track Alias -- a status request
// creates no subscription for one to name.
func TestTrackStatusEndToEnd(t *testing.T) {
	server := &Session{
		TrackStatusHandler: TrackStatusHandlerFunc(func(r *TrackStatusRequest) {
			if r.Track() != "video0" {
				_ = r.Reject(RequestErrorDoesNotExist, "no such track")
				return
			}
			_ = r.Accept(TrackStatus{
				Parameters: Parameters{
					wire2.LocationParameter(wire2.ParamLargestObject, Location{Group: 12, Object: 4}),
				},
				Properties: KVPList{{Type: wire2.PropertyDefaultPublisherPriority, ValueVarInt: 7}},
			})
		}),
	}
	client := &Session{}
	runSessions(t, client, server)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	status, err := client.TrackStatus(ctx, []string{"example.com", "live"}, "video0")
	require.NoError(t, err)

	largest, present := status.LargestObject()
	require.True(t, present)
	assert.Equal(t, uint64(12), largest.Group)
	assert.Equal(t, uint64(4), largest.Object)

	priority, err := status.Properties.DefaultPublisherPriority()
	require.NoError(t, err)
	assert.Equal(t, uint8(7), priority)
}

func TestTrackStatusRejected(t *testing.T) {
	server := &Session{
		TrackStatusHandler: TrackStatusHandlerFunc(func(r *TrackStatusRequest) {
			_ = r.Reject(RequestErrorDoesNotExist, "no such track")
		}),
	}
	client := &Session{}
	runSessions(t, client, server)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, err := client.TrackStatus(ctx, []string{"ns"}, "missing")
	var reqErr *RequestError
	require.ErrorAs(t, err, &reqErr)
	assert.Equal(t, RequestErrorDoesNotExist, reqErr.Code)
}

func TestTrackStatusWithoutHandler(t *testing.T) {
	client, server := &Session{}, &Session{}
	runSessions(t, client, server)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, err := client.TrackStatus(ctx, []string{"ns"}, "track")
	var reqErr *RequestError
	require.ErrorAs(t, err, &reqErr)
	assert.Equal(t, RequestErrorNotSupported, reqErr.Code)
}

// A status request is one message and one answer, so a second answer has
// nowhere to go.
func TestTrackStatusAnsweredOnce(t *testing.T) {
	second := make(chan error, 1)
	server := &Session{
		TrackStatusHandler: TrackStatusHandlerFunc(func(r *TrackStatusRequest) {
			_ = r.Accept(TrackStatus{})
			second <- r.Accept(TrackStatus{})
		}),
	}
	client := &Session{}
	runSessions(t, client, server)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, err := client.TrackStatus(ctx, []string{"ns"}, "track")
	require.NoError(t, err)
	assert.ErrorIs(t, <-second, errRequestAlreadyAnswered)
}
