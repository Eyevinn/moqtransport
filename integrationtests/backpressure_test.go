package integrationtests

import (
	"bytes"
	"context"
	"fmt"
	"runtime"
	"testing"
	"time"

	"github.com/Eyevinn/moqtransport"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// subscribeWithPublisher establishes a subscription and hands back the
// client-side RemoteTrack together with the server-side Publisher.
func subscribeWithPublisher(t *testing.T) (rt *moqtransport.RemoteTrack, publisher moqtransport.Publisher, closeSessions func()) {
	sConn, cConn, cancelConns := connect(t)

	publisherCh := make(chan moqtransport.Publisher, 1)
	subscribeHandler := moqtransport.SubscribeHandlerFunc(func(w *moqtransport.SubscribeResponseWriter, m *moqtransport.SubscribeMessage) {
		assert.NoError(t, w.Accept())
		publisherCh <- w
	})
	_, ct, cancelSessions := setupWithHandlers(t, sConn, cConn, nil, subscribeHandler)

	rt, err := ct.Subscribe(context.Background(), []string{"namespace"}, "track")
	require.NoError(t, err)
	require.NotNil(t, rt)

	select {
	case publisher = <-publisherCh:
	case <-time.After(time.Second):
		require.FailNow(t, "timeout while waiting for publisher")
	}
	return rt, publisher, func() {
		cancelSessions()
		cancelConns()
	}
}

// writeSubgroupObjects writes n objects to a single subgroup from a
// goroutine, so a stalled reader can never deadlock the test itself.
func writeSubgroupObjects(t *testing.T, publisher moqtransport.Publisher, n int) {
	sg, err := publisher.OpenSubgroup(0, 0, 0)
	require.NoError(t, err)
	go func() {
		for i := 0; i < n; i++ {
			if _, err := sg.WriteObject(uint64(i), fmt.Appendf(nil, "object %d", i)); err != nil {
				return
			}
		}
		_ = sg.Close()
	}()
}

// goroutineParkedIn reports whether any goroutine's stack mentions fn. The
// buffer grows until the whole dump fits: runtime.Stack truncates silently,
// and a truncated dump would let the assertions below pass without ever
// having seen the goroutine they are about.
func goroutineParkedIn(fn string) bool {
	buf := make([]byte, 1<<20)
	for {
		n := runtime.Stack(buf, true)
		if n < len(buf) {
			return bytes.Contains(buf[:n], []byte(fn))
		}
		buf = make([]byte, 2*len(buf))
	}
}

func TestSubgroupBackpressure(t *testing.T) {
	// Well over the RemoteTrack delivery buffer (100), so delivery must
	// block on the consumer rather than drop.
	const count = 300

	t.Run("slow_reader_loses_nothing", func(t *testing.T) {
		rt, publisher, closeSessions := subscribeWithPublisher(t)
		defer closeSessions()

		writeSubgroupObjects(t, publisher, count)

		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		for i := 0; i < count; i++ {
			// Slower than the sender, so the buffer fills and the stream
			// reader has to wait for us.
			time.Sleep(500 * time.Microsecond)
			o, err := rt.ReadObject(ctx)
			require.NoError(t, err, "object %d", i)
			assert.Equal(t, uint64(i), o.ObjectID, "objects must arrive in order with none dropped")
			assert.Equal(t, fmt.Sprintf("object %d", i), string(o.Payload))
		}
	})

	t.Run("session_close_releases_blocked_reader", func(t *testing.T) {
		rt, publisher, closeSessions := subscribeWithPublisher(t)
		_ = rt // never drained

		writeSubgroupObjects(t, publisher, count)

		// Wait for the stream reader to fill the buffer and park in the
		// blocking delivery path.
		assert.Eventually(t, func() bool {
			return goroutineParkedIn("pushBlocking")
		}, 5*time.Second, 10*time.Millisecond, "stream reader never blocked on delivery")

		// No SUBSCRIBE_DONE ever arrives; closing the sessions must still
		// release the reader (this leaked before the session context was
		// threaded into the stream readers).
		closeSessions()

		assert.Eventually(t, func() bool {
			return !goroutineParkedIn("pushBlocking")
		}, 5*time.Second, 10*time.Millisecond, "stream reader goroutine survived Session.Close")
	})
}
