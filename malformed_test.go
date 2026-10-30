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

// awaitViolation waits for s to close with a PROTOCOL_VIOLATION naming want.
func awaitViolation(t *testing.T, s *Session, want string) {
	t.Helper()
	select {
	case <-s.Context().Done():
	case <-time.After(5 * time.Second):
		t.Fatal("the session did not close")
	}
	cause := context.Cause(s.Context())
	assert.ErrorContains(t, cause, "PROTOCOL_VIOLATION")
	assert.ErrorContains(t, cause, want)
}

// A peer may cancel a request at any time, even before the request has
// arrived (Section 3.3.2): a stream reset before its first message is not a
// protocol violation.
func TestRequestStreamResetBeforeTheRequest(t *testing.T) {
	forEachVersion(t, func(t *testing.T, version wire2.Version) {
		client, server := &Session{}, &Session{}
		runSessionsVersion(t, version, client, server)

		stream, err := client.conn.OpenStreamSync(context.Background())
		require.NoError(t, err)
		stream.Reset(uint32(StreamErrorCancelled))
		stream.Stop(uint32(StreamErrorCancelled))

		// A complete request on a stream reset part-way through it is the
		// same thing: the request went away, not the protocol.
		partial, err := client.conn.OpenStreamSync(context.Background())
		require.NoError(t, err)
		buf, err := wire2.AppendControlMessage(nil, &wire2.Subscribe{
			RequestID: client.nextRequestID(), TrackNamespace: [][]byte{[]byte("ns")}, TrackName: []byte("t"),
			Parameters: wire2.Parameters{},
		}, version)
		require.NoError(t, err)
		_, err = partial.Write(buf[:len(buf)-2])
		require.NoError(t, err)
		partial.Reset(uint32(StreamErrorCancelled))

		synctest.Wait()
		assert.NoError(t, server.Context().Err())
		assert.NoError(t, client.Context().Err())
	})
}

// A malformed message closes the session wherever it arrives, not only when
// it opens the stream: on an established subscription too.
func TestMalformedMessageOnAnEstablishedRequest(t *testing.T) {
	for name, raw := range map[string][]byte{
		// Type 0x60 is no message in either draft.
		"unknown type": {0x60, 0x00, 0x00},
		// A REQUEST_UPDATE whose Message Length leaves a byte over.
		"trailing bytes": {0x02, 0x00, 0x03, 0x02, 0x00, 0x00},
	} {
		t.Run(name, func(t *testing.T) {
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

				_, err = track.stream.Write(raw)
				require.NoError(t, err)
				awaitViolation(t, server, "malformed control message")
			})
		})
	}
}

// A request stream that ends part-way through a message is malformed too.
func TestTruncatedRequestClosesTheSession(t *testing.T) {
	forEachVersion(t, func(t *testing.T, version wire2.Version) {
		client, server := &Session{}, &Session{}
		runSessionsVersion(t, version, client, server)

		stream, err := client.conn.OpenStreamSync(context.Background())
		require.NoError(t, err)
		buf, err := wire2.AppendControlMessage(nil, &wire2.Subscribe{
			RequestID: client.nextRequestID(), TrackNamespace: [][]byte{[]byte("ns")}, TrackName: []byte("t"),
			Parameters: wire2.Parameters{},
		}, version)
		require.NoError(t, err)
		_, err = stream.Write(buf[:len(buf)-2])
		require.NoError(t, err)
		require.NoError(t, stream.Close())

		awaitViolation(t, server, "malformed control message")
	})
}

// partialHeaders are data stream headers cut short, each by one byte.
func partialHeaders(t *testing.T) map[string][]byte {
	t.Helper()
	subgroup, err := wire2.AppendSubgroupHeader(nil, &wire2.SubgroupHeader{
		TrackAlias:     99,
		GroupID:        1,
		SubgroupIDMode: wire2.SubgroupIDExplicit,
		Priority:       128,
	})
	require.NoError(t, err)
	fetch := wire2.AppendFetchHeader(nil, 1000)
	return map[string][]byte{
		// 0x80 opens a two-byte varint.
		"stream type":     {0x80},
		"subgroup header": subgroup[:len(subgroup)-1],
		"fetch header":    fetch[:len(fetch)-1],
	}
}

// A publisher may reset a data stream before its header has arrived -- a
// draft-22 publisher refusing a fill opens the stream and resets it straight
// away -- and that costs only the stream.
func TestDataStreamResetBeforeItsHeader(t *testing.T) {
	headers := partialHeaders(t)
	headers["nothing"] = nil
	for name, header := range headers {
		t.Run(name, func(t *testing.T) {
			forEachVersion(t, func(t *testing.T, version wire2.Version) {
				client, server := &Session{}, &Session{}
				runSessionsVersion(t, version, client, server)

				stream, err := client.conn.OpenUniStreamSync(context.Background())
				require.NoError(t, err)
				if len(header) > 0 {
					_, err = stream.Write(header)
					require.NoError(t, err)
				}
				stream.Reset(uint32(StreamErrorCancelled))

				synctest.Wait()
				assert.NoError(t, server.Context().Err())
				assert.NoError(t, client.Context().Err())
			})
		})
	}
}

// A data stream that ends part-way through its header is malformed.
func TestTruncatedDataStreamHeaderClosesTheSession(t *testing.T) {
	for name, header := range partialHeaders(t) {
		t.Run(name, func(t *testing.T) {
			forEachVersion(t, func(t *testing.T, version wire2.Version) {
				client, server := &Session{}, &Session{}
				runSessionsVersion(t, version, client, server)

				stream, err := client.conn.OpenUniStreamSync(context.Background())
				require.NoError(t, err)
				_, err = stream.Write(header)
				require.NoError(t, err)
				require.NoError(t, stream.Close())

				awaitViolation(t, server, "malformed stream header")
			})
		})
	}
}
