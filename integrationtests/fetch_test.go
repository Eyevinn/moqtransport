package integrationtests

import (
	"context"
	"testing"
	"time"

	"github.com/Eyevinn/moqtransport"
	"github.com/stretchr/testify/assert"
)

func TestFetch(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		sConn, cConn, cancel := connect(t)
		defer cancel()

		handler := moqtransport.HandlerFunc(func(w moqtransport.ResponseWriter, m *moqtransport.Message) {
			assert.Equal(t, moqtransport.MessageFetch, m.Method)
			assert.NotNil(t, w)
			assert.NoError(t, w.Accept())
		})
		_, ct, cancel := setup(t, sConn, cConn, handler)
		defer cancel()

		rt, err := ct.Fetch(context.Background(), []string{"namespace"}, "track")
		assert.NoError(t, err)
		assert.NotNil(t, rt)
	})
	t.Run("auth_error", func(t *testing.T) {
		sConn, cConn, cancel := connect(t)
		defer cancel()

		handler := moqtransport.HandlerFunc(func(w moqtransport.ResponseWriter, m *moqtransport.Message) {
			assert.Equal(t, moqtransport.MessageFetch, m.Method)
			assert.NotNil(t, w)
			assert.NoError(t, w.Reject(uint64(moqtransport.ErrorCodeFetchUnauthorized), "unauthorized"))
		})
		_, ct, cancel := setup(t, sConn, cConn, handler)
		defer cancel()

		rt, err := ct.Fetch(context.Background(), []string{"namespace"}, "track")
		assert.Error(t, err)
		assert.ErrorContains(t, err, "unauthorized")
		assert.Nil(t, rt)
	})

	t.Run("receive_objects", func(t *testing.T) {
		sConn, cConn, cancel := connect(t)
		defer cancel()

		publisherCh := make(chan moqtransport.FetchPublisher, 1)

		handler := moqtransport.HandlerFunc(func(w moqtransport.ResponseWriter, m *moqtransport.Message) {
			assert.Equal(t, moqtransport.MessageFetch, m.Method)
			assert.NotNil(t, w)
			assert.NoError(t, w.Accept())
			publisher, ok := w.(moqtransport.FetchPublisher)
			assert.True(t, ok)
			publisherCh <- publisher
		})
		_, ct, cancel := setup(t, sConn, cConn, handler)
		defer cancel()

		rt, err := ct.Fetch(context.Background(), []string{"namespace"}, "track")
		assert.NoError(t, err)
		assert.NotNil(t, rt)

		var publisher moqtransport.FetchPublisher
		select {
		case publisher = <-publisherCh:
		case <-time.After(time.Second):
			assert.FailNow(t, "timeout while waiting for publisher")
		}

		fs, err := publisher.FetchStream()
		assert.NoError(t, err)
		n, err := fs.WriteObject(1, 2, 3, 0, []byte("hello fetch"))
		assert.NoError(t, err)
		assert.Equal(t, 11, n)
		assert.NoError(t, fs.Close())

		ctx2, cancelCtx2 := context.WithTimeout(context.Background(), time.Second)
		defer cancelCtx2()

		o, err := rt.ReadObject(ctx2)
		assert.NoError(t, err)
		assert.Equal(t, &moqtransport.Object{
			GroupID:    1,
			SubGroupID: 2,
			ObjectID:   3,
			Payload:    []byte("hello fetch"),
		}, o)
	})

	t.Run("standalone_with_options", func(t *testing.T) {
		sConn, cConn, cancel := connect(t)
		defer cancel()

		fetchMsgCh := make(chan *moqtransport.FetchMessage, 1)

		fetchHandler := moqtransport.FetchHandlerFunc(func(w *moqtransport.FetchResponseWriter, m *moqtransport.FetchMessage) {
			fetchMsgCh <- m
			assert.NoError(t, w.Accept())
		})
		_, ct, cancel := setupWithAllHandlers(t, sConn, cConn, sessionOptions{
			handler:      moqtransport.HandlerFunc(func(w moqtransport.ResponseWriter, m *moqtransport.Message) {}),
			fetchHandler: fetchHandler,
		})
		defer cancel()

		rt, err := ct.Fetch(context.Background(), []string{"ns"}, "track",
			moqtransport.WithFetchPriority(42),
			moqtransport.WithFetchGroupOrder(moqtransport.GroupOrderDescending),
			moqtransport.WithFetchStartLocation(moqtransport.Location{Group: 5, Object: 0}),
			moqtransport.WithFetchEndLocation(moqtransport.Location{Group: 10, Object: 0}),
		)
		assert.NoError(t, err)
		assert.NotNil(t, rt)

		var fm *moqtransport.FetchMessage
		select {
		case fm = <-fetchMsgCh:
		case <-time.After(time.Second):
			assert.FailNow(t, "timeout waiting for FetchMessage")
		}
		assert.Equal(t, moqtransport.FetchTypeStandalone, fm.FetchType)
		assert.Equal(t, []string{"ns"}, fm.Namespace)
		assert.Equal(t, "track", fm.Track)
		assert.Equal(t, uint8(42), fm.SubscriberPriority)
		assert.Equal(t, moqtransport.GroupOrderDescending, fm.GroupOrder)
		assert.Equal(t, moqtransport.Location{Group: 5, Object: 0}, fm.StartLocation)
		assert.Equal(t, moqtransport.Location{Group: 10, Object: 0}, fm.EndLocation)
	})

	// acceptingSubscribeHandler accepts every subscription, optionally reporting
	// a largest location so a joining fetch can be resolved against it.
	acceptingSubscribeHandler := func(largest *moqtransport.Location) moqtransport.SubscribeHandler {
		return moqtransport.SubscribeHandlerFunc(func(w *moqtransport.SubscribeResponseWriter, m *moqtransport.SubscribeMessage) {
			if largest != nil {
				assert.NoError(t, w.Accept(moqtransport.WithLargestLocation(largest)))
			} else {
				assert.NoError(t, w.Accept())
			}
		})
	}

	t.Run("relative_joining_fetch", func(t *testing.T) {
		sConn, cConn, cancel := connect(t)
		defer cancel()

		fetchMsgCh := make(chan *moqtransport.FetchMessage, 1)
		publisherCh := make(chan *moqtransport.FetchResponseWriter, 1)

		fetchHandler := moqtransport.FetchHandlerFunc(func(w *moqtransport.FetchResponseWriter, m *moqtransport.FetchMessage) {
			fetchMsgCh <- m
			assert.NoError(t, w.Accept())
			publisherCh <- w
		})
		_, ct, cancel := setupWithAllHandlers(t, sConn, cConn, sessionOptions{
			subscribeHandler: acceptingSubscribeHandler(&moqtransport.Location{Group: 4, Object: 2}),
			fetchHandler:     fetchHandler,
		})
		defer cancel()

		// Establish a Largest Object subscription first; the joining fetch is
		// resolved relative to its largest location.
		sub, err := ct.Subscribe(context.Background(), []string{"ns"}, "catalog")
		assert.NoError(t, err)
		largest, ok := sub.LargestLocation()
		assert.True(t, ok)
		assert.Equal(t, moqtransport.Location{Group: 4, Object: 2}, largest)

		rt, err := ct.Fetch(context.Background(), nil, "",
			moqtransport.WithJoiningFetchRelative(sub.RequestID(), 0),
		)
		assert.NoError(t, err)
		assert.NotNil(t, rt)

		var fm *moqtransport.FetchMessage
		select {
		case fm = <-fetchMsgCh:
		case <-time.After(time.Second):
			assert.FailNow(t, "timeout waiting for FetchMessage")
		}
		// The publisher resolved namespace, track and the [start, end) range.
		assert.Equal(t, moqtransport.FetchTypeRelativeJoining, fm.FetchType)
		assert.Equal(t, sub.RequestID(), fm.JoiningSubscribeID)
		assert.Equal(t, []string{"ns"}, fm.Namespace)
		assert.Equal(t, "catalog", fm.Track)
		assert.Equal(t, moqtransport.Location{Group: 4, Object: 0}, fm.StartLocation)
		assert.Equal(t, moqtransport.Location{Group: 4, Object: 3}, fm.EndLocation) // largest.Object + 1

		// Verify objects can be sent back
		var publisher *moqtransport.FetchResponseWriter
		select {
		case publisher = <-publisherCh:
		case <-time.After(time.Second):
			assert.FailNow(t, "timeout waiting for publisher")
		}

		fs, err := publisher.FetchStream()
		assert.NoError(t, err)
		_, err = fs.WriteObject(4, 0, 0, 0, []byte("joining-data"))
		assert.NoError(t, err)
		assert.NoError(t, fs.Close())

		ctx2, cancelCtx2 := context.WithTimeout(context.Background(), time.Second)
		defer cancelCtx2()

		o, err := rt.ReadObject(ctx2)
		assert.NoError(t, err)
		assert.Equal(t, uint64(4), o.GroupID)
		assert.Equal(t, []byte("joining-data"), o.Payload)
	})

	t.Run("relative_joining_fetch_draft16", func(t *testing.T) {
		// Draft-16 carries the SUBSCRIBE_OK largest location as a parameter
		// rather than inline (see subscribe_ok_message.go); verify joining-fetch
		// resolution works identically over draft-16.
		sConn, cConn, cancel := connectALPN(t, "moqt-16")
		defer cancel()

		fetchMsgCh := make(chan *moqtransport.FetchMessage, 1)
		publisherCh := make(chan *moqtransport.FetchResponseWriter, 1)
		fetchHandler := moqtransport.FetchHandlerFunc(func(w *moqtransport.FetchResponseWriter, m *moqtransport.FetchMessage) {
			fetchMsgCh <- m
			assert.NoError(t, w.Accept())
			publisherCh <- w
		})
		_, ct, cancel := setupWithAllHandlers(t, sConn, cConn, sessionOptions{
			subscribeHandler: acceptingSubscribeHandler(&moqtransport.Location{Group: 4, Object: 2}),
			fetchHandler:     fetchHandler,
		})
		defer cancel()

		sub, err := ct.Subscribe(context.Background(), []string{"ns"}, "catalog")
		assert.NoError(t, err)
		largest, ok := sub.LargestLocation()
		assert.True(t, ok)
		assert.Equal(t, moqtransport.Location{Group: 4, Object: 2}, largest)

		rt, err := ct.Fetch(context.Background(), nil, "",
			moqtransport.WithJoiningFetchRelative(sub.RequestID(), 0),
		)
		assert.NoError(t, err)
		assert.NotNil(t, rt)

		var fm *moqtransport.FetchMessage
		select {
		case fm = <-fetchMsgCh:
		case <-time.After(time.Second):
			assert.FailNow(t, "timeout waiting for FetchMessage")
		}
		assert.Equal(t, moqtransport.FetchTypeRelativeJoining, fm.FetchType)
		assert.Equal(t, []string{"ns"}, fm.Namespace)
		assert.Equal(t, "catalog", fm.Track)
		assert.Equal(t, moqtransport.Location{Group: 4, Object: 0}, fm.StartLocation)
		assert.Equal(t, moqtransport.Location{Group: 4, Object: 3}, fm.EndLocation)

		// Deliver and read an object back over the draft-16 fetch stream to
		// verify object delivery (not just control-message resolution) works.
		var publisher *moqtransport.FetchResponseWriter
		select {
		case publisher = <-publisherCh:
		case <-time.After(time.Second):
			assert.FailNow(t, "timeout waiting for publisher")
		}
		fs, err := publisher.FetchStream()
		assert.NoError(t, err)
		_, err = fs.WriteObject(4, 0, 0, 0, []byte("joining-data-16"))
		assert.NoError(t, err)
		assert.NoError(t, fs.Close())

		ctx2, cancelCtx2 := context.WithTimeout(context.Background(), time.Second)
		defer cancelCtx2()
		o, err := rt.ReadObject(ctx2)
		assert.NoError(t, err)
		assert.Equal(t, uint64(4), o.GroupID)
		assert.Equal(t, []byte("joining-data-16"), o.Payload)
	})

	t.Run("absolute_joining_fetch", func(t *testing.T) {
		sConn, cConn, cancel := connect(t)
		defer cancel()

		fetchMsgCh := make(chan *moqtransport.FetchMessage, 1)

		fetchHandler := moqtransport.FetchHandlerFunc(func(w *moqtransport.FetchResponseWriter, m *moqtransport.FetchMessage) {
			fetchMsgCh <- m
			assert.NoError(t, w.Accept())
		})
		_, ct, cancel := setupWithAllHandlers(t, sConn, cConn, sessionOptions{
			subscribeHandler: acceptingSubscribeHandler(&moqtransport.Location{Group: 4, Object: 2}),
			fetchHandler:     fetchHandler,
		})
		defer cancel()

		sub, err := ct.Subscribe(context.Background(), []string{"ns"}, "catalog")
		assert.NoError(t, err)

		rt, err := ct.Fetch(context.Background(), nil, "",
			moqtransport.WithJoiningFetchAbsolute(sub.RequestID(), 2),
		)
		assert.NoError(t, err)
		assert.NotNil(t, rt)

		var fm *moqtransport.FetchMessage
		select {
		case fm = <-fetchMsgCh:
		case <-time.After(time.Second):
			assert.FailNow(t, "timeout waiting for FetchMessage")
		}
		assert.Equal(t, moqtransport.FetchTypeAbsoluteJoining, fm.FetchType)
		assert.Equal(t, sub.RequestID(), fm.JoiningSubscribeID)
		assert.Equal(t, []string{"ns"}, fm.Namespace)
		assert.Equal(t, "catalog", fm.Track)
		assert.Equal(t, moqtransport.Location{Group: 2, Object: 0}, fm.StartLocation)
		assert.Equal(t, moqtransport.Location{Group: 4, Object: 3}, fm.EndLocation)
	})

	t.Run("joining_fetch_unknown_request_id", func(t *testing.T) {
		sConn, cConn, cancel := connect(t)
		defer cancel()

		fetchHandler := moqtransport.FetchHandlerFunc(func(w *moqtransport.FetchResponseWriter, m *moqtransport.FetchMessage) {
			assert.FailNow(t, "handler must not be called for an invalid joining fetch")
		})
		_, ct, cancel := setupWithAllHandlers(t, sConn, cConn, sessionOptions{
			subscribeHandler: acceptingSubscribeHandler(&moqtransport.Location{Group: 4, Object: 2}),
			fetchHandler:     fetchHandler,
		})
		defer cancel()

		// No subscription with this request ID exists.
		_, err := ct.Fetch(context.Background(), nil, "",
			moqtransport.WithJoiningFetchRelative(99999, 0),
		)
		assert.Error(t, err)
		assert.ErrorContains(t, err, "unknown joining request id")
	})

	t.Run("joining_fetch_no_content_invalid_range", func(t *testing.T) {
		sConn, cConn, cancel := connect(t)
		defer cancel()

		fetchHandler := moqtransport.FetchHandlerFunc(func(w *moqtransport.FetchResponseWriter, m *moqtransport.FetchMessage) {
			assert.FailNow(t, "handler must not be called when there is no content")
		})
		_, ct, cancel := setupWithAllHandlers(t, sConn, cConn, sessionOptions{
			// Accept without a largest location → no content published.
			subscribeHandler: acceptingSubscribeHandler(nil),
			fetchHandler:     fetchHandler,
		})
		defer cancel()

		sub, err := ct.Subscribe(context.Background(), []string{"ns"}, "catalog")
		assert.NoError(t, err)

		_, err = ct.Fetch(context.Background(), nil, "",
			moqtransport.WithJoiningFetchRelative(sub.RequestID(), 0),
		)
		assert.Error(t, err)
		assert.ErrorContains(t, err, "no content")
	})

	t.Run("joining_fetch_wrong_filter_is_protocol_violation", func(t *testing.T) {
		sConn, cConn, cancel := connect(t)
		defer cancel()

		fetchHandler := moqtransport.FetchHandlerFunc(func(w *moqtransport.FetchResponseWriter, m *moqtransport.FetchMessage) {
			assert.FailNow(t, "handler must not be called on a protocol violation")
		})
		srv, ct, cancel := setupWithAllHandlers(t, sConn, cConn, sessionOptions{
			subscribeHandler: acceptingSubscribeHandler(&moqtransport.Location{Group: 4, Object: 2}),
			fetchHandler:     fetchHandler,
		})
		defer cancel()

		// Subscribe with a non-Largest-Object filter; a joining fetch against it
		// is a session-fatal protocol violation.
		sub, err := ct.Subscribe(context.Background(), []string{"ns"}, "catalog",
			moqtransport.WithFilterType(moqtransport.FilterTypeNextGroupStart))
		assert.NoError(t, err)

		// The publisher closes the session instead of responding, so the fetch
		// does not complete; bound it with a timeout.
		fctx, fcancel := context.WithTimeout(context.Background(), time.Second)
		defer fcancel()
		_, err = ct.Fetch(fctx, nil, "",
			moqtransport.WithJoiningFetchRelative(sub.RequestID(), 0))
		assert.Error(t, err)

		// The server session terminates with a protocol violation, surfaced via
		// its background errgroup when closed.
		assert.ErrorContains(t, srv.Close(), "filter type Largest Object")
	})
}
