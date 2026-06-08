package integrationtests

import (
	"context"
	"testing"
	"time"

	"github.com/Eyevinn/moqtransport"
	"github.com/stretchr/testify/assert"
)

func TestAnnounce(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		sConn, cConn, cancel := connect(t)
		defer cancel()

		handler := moqtransport.HandlerFunc(func(w moqtransport.ResponseWriter, m *moqtransport.Message) {
			assert.Equal(t, moqtransport.MessageAnnounce, m.Method)
			assert.NotNil(t, w)
			assert.NoError(t, w.Accept())
		})
		_, ct, cancel := setup(t, sConn, cConn, handler)
		defer cancel()

		err := ct.Announce(context.Background(), []string{"namespace"})
		assert.NoError(t, err)
	})
	t.Run("error", func(t *testing.T) {
		sConn, cConn, cancel := connect(t)
		defer cancel()

		handler := moqtransport.HandlerFunc(func(w moqtransport.ResponseWriter, m *moqtransport.Message) {
			assert.Equal(t, moqtransport.MessageAnnounce, m.Method)
			assert.NotNil(t, w)
			assert.NoError(t, w.Reject(uint64(moqtransport.ErrorCodeAnnounceInternal), "expected error"))
		})
		_, ct, cancel := setup(t, sConn, cConn, handler)
		defer cancel()

		err := ct.Announce(context.Background(), []string{"namespace"})
		assert.Error(t, err)
	})

	t.Run("success_draft16", func(t *testing.T) {
		sConn, cConn, cancel := connectALPN(t, "moqt-16")
		defer cancel()

		handler := moqtransport.HandlerFunc(func(w moqtransport.ResponseWriter, m *moqtransport.Message) {
			assert.Equal(t, moqtransport.MessageAnnounce, m.Method)
			assert.NoError(t, w.Accept())
		})
		_, ct, cancel := setup(t, sConn, cConn, handler)
		defer cancel()

		ctx, cancelCtx := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancelCtx()
		assert.NoError(t, ct.Announce(ctx, []string{"namespace"}))
	})

	// Regression: on draft-16 a rejected announce must surface as a REQUEST_ERROR
	// the publisher can route back, not hang. ANNOUNCE_ERROR's draft-14 code
	// (0x08) is NAMESPACE in draft-16 and is unparseable as an announce response.
	t.Run("error_draft16", func(t *testing.T) {
		sConn, cConn, cancel := connectALPN(t, "moqt-16")
		defer cancel()

		handler := moqtransport.HandlerFunc(func(w moqtransport.ResponseWriter, m *moqtransport.Message) {
			assert.NoError(t, w.Reject(uint64(moqtransport.ErrorCodeAnnounceInternal), "expected error"))
		})
		_, ct, cancel := setup(t, sConn, cConn, handler)
		defer cancel()

		ctx, cancelCtx := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancelCtx()
		err := ct.Announce(ctx, []string{"namespace"})
		assert.Error(t, err)
		assert.NotErrorIs(t, err, context.DeadlineExceeded, "announce reject must not hang on draft-16")
		assert.ErrorContains(t, err, "expected error")
	})
}
