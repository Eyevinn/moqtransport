package moqtransport

import (
	"context"
	"testing"
	"time"

	"github.com/Eyevinn/moqtransport/internal/wire2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// sendRaw opens a request stream from s and writes msg on it as it is,
// past the checks a request would make on the way out: the tests here are
// about what the receiving side does with a peer that does not make them.
func sendRaw(t *testing.T, s *Session, msg wire2.ControlMessage) Stream {
	t.Helper()
	stream, err := s.conn.OpenStreamSync(context.Background())
	require.NoError(t, err)
	writeRaw(t, stream, s.Version(), msg)
	return stream
}

// writeRaw writes msg on stream as it is, past the checks a request makes on
// the way out.
func writeRaw(t *testing.T, stream SendStream, version wire2.Version, msg wire2.ControlMessage) {
	t.Helper()
	buf, err := wire2.AppendControlMessage(nil, msg, version)
	require.NoError(t, err)
	_, err = stream.Write(buf)
	require.NoError(t, err)
}

// A request carrying a parameter its message does not allow closes the
// session, before any handler sees it.
func TestRequestWithDisallowedParameter(t *testing.T) {
	forEachVersion(t, func(t *testing.T, version wire2.Version) {
		server := &Session{
			SubscribeHandler: SubscribeHandlerFunc(func(r *SubscribeRequest) {
				t.Error("a SUBSCRIBE with a parameter it may not carry reached the handler")
			}),
		}
		client := &Session{}
		runSessionsVersion(t, version, client, server)

		// EXPIRES belongs in the answer, not the request.
		sendRaw(t, client, &wire2.Subscribe{
			RequestID:      client.nextRequestID(),
			TrackNamespace: [][]byte{[]byte("ns")},
			TrackName:      []byte("track"),
			Parameters:     wire2.Parameters{wire2.VarintParameter(wire2.ParamExpires, 1000)},
		})
		awaitViolation(t, server, "EXPIRES is not allowed in SUBSCRIBE")
	})
}

// A REQUEST_UPDATE is checked against what can be updated on that request:
// a subscription's Group Order cannot.
func TestUpdateWithDisallowedParameter(t *testing.T) {
	forEachVersion(t, func(t *testing.T, version wire2.Version) {
		server := &Session{
			SubscribeHandler: SubscribeHandlerFunc(func(r *SubscribeRequest) { _, _ = r.Accept() }),
		}
		client := &Session{}
		runSessionsVersion(t, version, client, server)

		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		track, err := client.Subscribe(ctx, []string{"ns"}, "track")
		require.NoError(t, err)

		writeRaw(t, track.stream, version, &wire2.RequestUpdate{
			RequestID:  client.nextRequestID(),
			Parameters: wire2.Parameters{wire2.Uint8Parameter(wire2.ParamGroupOrder, 1)},
		})
		awaitViolation(t, server, "GROUP_ORDER is not allowed in REQUEST_UPDATE for a subscription")
	})
}

// Answers are checked too: the subscriber closes the session over a
// SUBSCRIBE_OK carrying something only a request may.
func TestAnswerWithDisallowedParameter(t *testing.T) {
	forEachVersion(t, func(t *testing.T, version wire2.Version) {
		server := &Session{
			SubscribeHandler: SubscribeHandlerFunc(func(r *SubscribeRequest) {
				writeRaw(t, r.stream, version, &wire2.SubscribeOk{
					TrackAlias: 1,
					Parameters: wire2.Parameters{wire2.Uint8Parameter(wire2.ParamForward, 1)},
				})
			}),
		}
		client := &Session{}
		runSessionsVersion(t, version, client, server)

		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, err := client.Subscribe(ctx, []string{"ns"}, "track")
		require.Error(t, err)
		awaitViolation(t, client, "FORWARD is not allowed in SUBSCRIBE_OK")
	})
}

// This endpoint never sends what its peer would have to close the session
// over: the write fails, and the session carries on.
func TestDisallowedParameterIsNotSent(t *testing.T) {
	forEachVersion(t, func(t *testing.T, version wire2.Version) {
		statusErr := make(chan error, 1)
		server := &Session{
			TrackStatusHandler: TrackStatusHandlerFunc(func(r *TrackStatusRequest) {
				statusErr <- r.Accept(TrackStatus{Parameters: Parameters{wire2.Uint8Parameter(wire2.ParamForward, 1)}})
			}),
		}
		client := &Session{}
		runSessionsVersion(t, version, client, server)

		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		_, err := client.Subscribe(ctx, []string{"ns"}, "track",
			withSubscribeParameter(wire2.VarintParameter(wire2.ParamExpires, 1)))
		assert.ErrorContains(t, err, "EXPIRES is not allowed in SUBSCRIBE")

		go func() { _, _ = client.TrackStatus(ctx, []string{"ns"}, "track") }()
		assert.ErrorContains(t, <-statusErr, "FORWARD is not allowed in TRACK_STATUS_OK")

		assert.NoError(t, client.Context().Err())
		assert.NoError(t, server.Context().Err())
	})
}

// What a REQUEST_OK may carry depends on what it answers, which only the
// stream's history says.
func TestParameterScopeTracksTheExchange(t *testing.T) {
	expires := &wire2.RequestOk{Parameters: wire2.Parameters{wire2.VarintParameter(wire2.ParamExpires, 1)}}
	largest := &wire2.RequestOk{Parameters: wire2.Parameters{wire2.LocationParameter(wire2.ParamLargestObject, Location{})}}

	t.Run("a namespace subscription's answer, then an update's", func(t *testing.T) {
		for _, tc := range []struct {
			version        wire2.Version
			expiresInFirst bool
		}{
			// EXPIRES in SUBSCRIBE_NAMESPACE_OK is draft-22's.
			{wire2.Version18, false},
			{wire2.Version22, true},
		} {
			var scope parameterScope
			require.NoError(t, scope.check(tc.version, &wire2.SubscribeNamespace{}, true))
			err := scope.check(tc.version, expires, false)
			if tc.expiresInFirst {
				assert.NoError(t, err, "%v", tc.version)
			} else {
				assert.ErrorContains(t, err, "EXPIRES is not allowed in SUBSCRIBE_NAMESPACE_OK", "%v", tc.version)
			}
			// Answered now: what follows answers a REQUEST_UPDATE, which may
			// report the Largest Object.
			assert.NoError(t, scope.check(tc.version, largest, false), "%v", tc.version)
		}
	})

	t.Run("a subscription is answered with SUBSCRIBE_OK", func(t *testing.T) {
		var scope parameterScope
		require.NoError(t, scope.check(wire2.Version22, &wire2.Subscribe{}, true))
		assert.NoError(t, scope.check(wire2.Version22, largest, false), "a REQUEST_OK on it answers an update")
	})

	t.Run("a TRACK_STATUS is answered with REQUEST_OK", func(t *testing.T) {
		var scope parameterScope
		scope.opened(wire2.ControlMessageTypeTrackStatus, false)
		assert.NoError(t, scope.check(wire2.Version22, largest, true))
	})

	t.Run("the publisher answers an update on a PUBLISH", func(t *testing.T) {
		var scope parameterScope
		require.NoError(t, scope.check(wire2.Version22, &wire2.Publish{}, false))
		// We answer the PUBLISH; draft-22's PUBLISH_OK carries only EXPIRES.
		assert.ErrorContains(t, scope.check(wire2.Version22, largest, true), "not allowed in PUBLISH_OK")
		require.NoError(t, scope.check(wire2.Version22, expires, true))
		// The publisher's REQUEST_OK can only answer our REQUEST_UPDATE.
		assert.NoError(t, scope.check(wire2.Version22, largest, false))
	})

	t.Run("updates depend on what is updated", func(t *testing.T) {
		prefix := &wire2.RequestUpdate{Parameters: wire2.Parameters{wire2.NamespaceParameter(wire2.ParamTrackNamespacePrefix, nil)}}
		var namespaces, subscription parameterScope
		namespaces.opened(wire2.ControlMessageTypeSubscribeNamespace, false)
		subscription.opened(wire2.ControlMessageTypeSubscribe, false)
		assert.NoError(t, namespaces.check(wire2.Version22, prefix, false))
		assert.ErrorContains(t, subscription.check(wire2.Version22, prefix, false),
			"TRACK_NAMESPACE_PREFIX is not allowed in REQUEST_UPDATE for a subscription")
	})
}
