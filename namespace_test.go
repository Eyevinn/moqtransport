package moqtransport

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPublishNamespaceEndToEnd(t *testing.T) {
	announced := make(chan *PublishNamespaceRequest, 1)
	server := &Session{
		PublishNamespaceHandler: PublishNamespaceHandlerFunc(func(r *PublishNamespaceRequest) {
			if err := r.Accept(); err != nil {
				return
			}
			announced <- r
		}),
	}
	client := &Session{}
	runSessions(t, client, server)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	publication, err := client.PublishNamespace(ctx, []string{"example.com", "live"})
	require.NoError(t, err)
	assert.Equal(t, []string{"example.com", "live"}, publication.Namespace())

	incoming := <-announced
	assert.Equal(t, []string{"example.com", "live"}, incoming.Namespace())

	// There is no UNANNOUNCE. Withdrawing the announcement ends its stream,
	// and the receiver's only notice is the request context ending.
	require.NoError(t, publication.Close())
	select {
	case <-incoming.Context().Done():
	case <-ctx.Done():
		t.Fatal("the receiver never noticed the announcement being withdrawn")
	}
}

func TestPublishNamespaceRejected(t *testing.T) {
	server := &Session{
		PublishNamespaceHandler: PublishNamespaceHandlerFunc(func(r *PublishNamespaceRequest) {
			r.Reject(RequestErrorUninterested, "not interested")
		}),
	}
	client := &Session{}
	runSessions(t, client, server)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, err := client.PublishNamespace(ctx, []string{"ns"})
	var reqErr *RequestError
	require.ErrorAs(t, err, &reqErr)
	assert.Equal(t, RequestErrorUninterested, reqErr.Code)
}

func TestPublishNamespaceWithoutHandler(t *testing.T) {
	client, server := &Session{}, &Session{}
	runSessions(t, client, server)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, err := client.PublishNamespace(ctx, []string{"ns"})
	var reqErr *RequestError
	require.ErrorAs(t, err, &reqErr)
	assert.Equal(t, RequestErrorNotSupported, reqErr.Code)
}

// The wire carries only the part of a namespace after the subscription's
// prefix, so the two ends have to agree on where the prefix stops.
func TestSubscribeNamespaceEndToEnd(t *testing.T) {
	announcers := make(chan *NamespaceAnnouncer, 1)
	server := &Session{
		SubscribeNamespaceHandler: SubscribeNamespaceHandlerFunc(func(r *SubscribeNamespaceRequest) {
			announcer, err := r.Accept()
			if err != nil {
				return
			}
			announcers <- announcer
		}),
	}
	client := &Session{}
	runSessions(t, client, server)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	prefix := []string{"example.com", "meeting=123"}
	subscription, err := client.SubscribeNamespace(ctx, prefix)
	require.NoError(t, err)
	assert.Equal(t, prefix, subscription.Prefix())

	announcer := <-announcers
	require.NoError(t, announcer.Announce([]string{"example.com", "meeting=123", "participant=100"}))
	require.NoError(t, announcer.Announce([]string{"example.com", "meeting=123", "participant=200"}))

	first := <-subscription.Namespaces()
	assert.True(t, first.Available)
	assert.Equal(t, []string{"example.com", "meeting=123", "participant=100"}, first.Namespace)

	second := <-subscription.Namespaces()
	assert.Equal(t, []string{"example.com", "meeting=123", "participant=200"}, second.Namespace)

	require.NoError(t, announcer.Done([]string{"example.com", "meeting=123", "participant=100"}))
	gone := <-subscription.Namespaces()
	assert.False(t, gone.Available)
	assert.Equal(t, []string{"example.com", "meeting=123", "participant=100"}, gone.Namespace)

	assert.Equal(t, [][]string{{"example.com", "meeting=123", "participant=200"}}, subscription.Active())

	// Ending the subscription is a NAMESPACE_DONE for everything still active,
	// which the subscriber should not have to work out for itself.
	require.NoError(t, announcer.Close())
	last, open := <-subscription.Namespaces()
	require.True(t, open)
	assert.False(t, last.Available)
	assert.Equal(t, []string{"example.com", "meeting=123", "participant=200"}, last.Namespace)

	_, open = <-subscription.Namespaces()
	assert.False(t, open, "the channel closes when the subscription ends")
}

// A namespace outside the prefix cannot be expressed on the wire, since only
// the suffix is sent.
func TestNamespaceAnnouncerRejectsForeignNamespace(t *testing.T) {
	announcers := make(chan *NamespaceAnnouncer, 1)
	server := &Session{
		SubscribeNamespaceHandler: SubscribeNamespaceHandlerFunc(func(r *SubscribeNamespaceRequest) {
			announcer, err := r.Accept()
			if err != nil {
				return
			}
			announcers <- announcer
		}),
	}
	client := &Session{}
	runSessions(t, client, server)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, err := client.SubscribeNamespace(ctx, []string{"example.com"})
	require.NoError(t, err)

	announcer := <-announcers
	assert.ErrorIs(t, announcer.Announce([]string{"elsewhere.example", "live"}), errNamespaceOutsidePrefix)
	assert.ErrorIs(t, announcer.Done([]string{"elsewhere.example"}), errNamespaceOutsidePrefix)
}

// Two namespace subscriptions in one session may not share a common prefix,
// and the library enforces it rather than leaving it to the handler.
func TestSubscribeNamespacePrefixOverlap(t *testing.T) {
	server := &Session{
		SubscribeNamespaceHandler: SubscribeNamespaceHandlerFunc(func(r *SubscribeNamespaceRequest) {
			r.Accept()
		}),
	}
	client := &Session{}
	runSessions(t, client, server)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	first, err := client.SubscribeNamespace(ctx, []string{"example.com", "meeting=123"})
	require.NoError(t, err)

	// A longer prefix under the first one overlaps it.
	_, err = client.SubscribeNamespace(ctx, []string{"example.com", "meeting=123", "participant=1"})
	var reqErr *RequestError
	require.ErrorAs(t, err, &reqErr)
	assert.Equal(t, RequestErrorPrefixOverlap, reqErr.Code)

	// So does a shorter one containing it.
	_, err = client.SubscribeNamespace(ctx, []string{"example.com"})
	require.ErrorAs(t, err, &reqErr)
	assert.Equal(t, RequestErrorPrefixOverlap, reqErr.Code)

	// A disjoint prefix is fine.
	_, err = client.SubscribeNamespace(ctx, []string{"elsewhere.example"})
	require.NoError(t, err)

	// And the reservation is released when the subscription ends.
	require.NoError(t, first.Close())
	require.Eventually(t, func() bool {
		_, err := client.SubscribeNamespace(ctx, []string{"example.com", "meeting=123"})
		return err == nil
	}, 2*time.Second, 10*time.Millisecond)
}

func TestOverlappingPrefixes(t *testing.T) {
	for _, tc := range []struct {
		a, b []string
		want bool
	}{
		{nil, []string{"a"}, true},
		{[]string{}, []string{}, true},
		{[]string{"a"}, []string{"a"}, true},
		{[]string{"a"}, []string{"a", "b"}, true},
		{[]string{"a", "b"}, []string{"a"}, true},
		{[]string{"a"}, []string{"b"}, false},
		{[]string{"a", "b"}, []string{"a", "c"}, false},
	} {
		assert.Equal(t, tc.want, overlappingPrefixes(tc.a, tc.b), "%v vs %v", tc.a, tc.b)
	}
}

func TestNamespaceSuffix(t *testing.T) {
	suffix, ok := namespaceSuffix([]string{"a"}, []string{"a", "b", "c"})
	assert.True(t, ok)
	assert.Equal(t, []string{"b", "c"}, suffix)

	suffix, ok = namespaceSuffix(nil, []string{"a"})
	assert.True(t, ok)
	assert.Equal(t, []string{"a"}, suffix)

	_, ok = namespaceSuffix([]string{"a"}, []string{"b"})
	assert.False(t, ok)

	_, ok = namespaceSuffix([]string{"a", "b"}, []string{"a"})
	assert.False(t, ok, "a namespace shorter than the prefix is not under it")
}
