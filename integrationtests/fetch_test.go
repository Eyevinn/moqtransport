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
			handler:      moqtransport.HandlerFunc(func(w moqtransport.ResponseWriter, m *moqtransport.Message) {}),
			fetchHandler: fetchHandler,
		})
		defer cancel()

		// Use subscribeRequestID=7 and precedingGroupOffset=3 for a relative joining fetch
		rt, err := ct.Fetch(context.Background(), nil, "",
			moqtransport.WithJoiningFetchRelative(7, 3),
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
		assert.Equal(t, uint64(7), fm.JoiningSubscribeID)
		assert.Equal(t, uint64(3), fm.JoiningStart)

		// Verify objects can be sent back
		var publisher *moqtransport.FetchResponseWriter
		select {
		case publisher = <-publisherCh:
		case <-time.After(time.Second):
			assert.FailNow(t, "timeout waiting for publisher")
		}

		fs, err := publisher.FetchStream()
		assert.NoError(t, err)
		_, err = fs.WriteObject(10, 0, 0, 0, []byte("joining-data"))
		assert.NoError(t, err)
		assert.NoError(t, fs.Close())

		ctx2, cancelCtx2 := context.WithTimeout(context.Background(), time.Second)
		defer cancelCtx2()

		o, err := rt.ReadObject(ctx2)
		assert.NoError(t, err)
		assert.Equal(t, uint64(10), o.GroupID)
		assert.Equal(t, []byte("joining-data"), o.Payload)
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
			handler:      moqtransport.HandlerFunc(func(w moqtransport.ResponseWriter, m *moqtransport.Message) {}),
			fetchHandler: fetchHandler,
		})
		defer cancel()

		rt, err := ct.Fetch(context.Background(), nil, "",
			moqtransport.WithJoiningFetchAbsolute(12, 100),
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
		assert.Equal(t, uint64(12), fm.JoiningSubscribeID)
		assert.Equal(t, uint64(100), fm.JoiningStart)
	})
}
