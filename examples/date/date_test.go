package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Eyevinn/moqtransport"
)

// These run the example against itself over a real QUIC stack, which is the
// only place the transport adapters, the TLS ALPN and the WebTransport
// subprotocol are exercised at all. The date track publishes once a second, so
// they take a few seconds by construction.

func TestMain(m *testing.M) {
	// The example logs freely; a test run should not.
	log.SetOutput(io.Discard)
	m.Run()
}

// freePort returns a UDP port that is free right now. Nothing can reserve one
// across the gap before the server binds it, but on a loopback test host the
// window is not worth more machinery.
//
// The probe binds the wildcard address rather than a loopback one: a runner
// without IPv6 cannot bind ::1, and one without IPv4 cannot bind 127.0.0.1.
func freePort(t *testing.T) string {
	t.Helper()
	conn, err := net.ListenUDP("udp", &net.UDPAddr{})
	if err != nil {
		t.Fatalf("finding a free port: %v", err)
	}
	port := conn.LocalAddr().(*net.UDPAddr).Port
	_ = conn.Close()
	return fmt.Sprintf("localhost:%d", port)
}

// startServer runs a publishing server and returns the address it listens on.
func startServer(t *testing.T, ctx context.Context) string {
	t.Helper()
	addr := freePort(t)

	tlsConfig, err := serverTLSConfig("", "")
	if err != nil {
		t.Fatalf("building the server TLS config: %v", err)
	}
	server := &moqHandler{
		namespace: []string{"clock"},
		trackname: "second",
		publish:   true,
	}

	done := make(chan error, 1)
	go func() { done <- server.runServer(ctx, addr, tlsConfig) }()
	t.Cleanup(func() { <-done })

	// The listener is up within a moment; a failed dial below would report it
	// anyway, so this only avoids a first-attempt failure.
	time.Sleep(100 * time.Millisecond)
	return addr
}

// collector gathers what a subscriber receives.
type collector struct {
	mu      sync.Mutex
	objects []*moqtransport.Object
	enough  chan struct{}
	want    int
}

func newCollector(want int) *collector {
	return &collector{enough: make(chan struct{}), want: want}
}

func (c *collector) add(o *moqtransport.Object) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.objects = append(c.objects, o)
	if len(c.objects) == c.want {
		close(c.enough)
	}
}

func (c *collector) collected() []*moqtransport.Object {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]*moqtransport.Object(nil), c.objects...)
}

// wait blocks until enough objects have arrived, failing the test otherwise.
func (c *collector) wait(t *testing.T, ctx context.Context) {
	t.Helper()
	select {
	case <-c.enough:
	case <-ctx.Done():
		t.Fatalf("only %d of %d objects arrived before the deadline", len(c.collected()), c.want)
	}
}

// runSubscriber runs a subscribing client until ctx ends.
func runSubscriber(t *testing.T, ctx context.Context, addr string, h *moqHandler, useWebTransport bool) {
	t.Helper()
	h.namespace = []string{"clock"}
	h.trackname = "second"
	h.subscribe = true

	done := make(chan error, 1)
	go func() { done <- h.runClient(ctx, addr, useWebTransport) }()
	t.Cleanup(func() {
		if err := <-done; err != nil {
			t.Errorf("client failed: %v", err)
		}
	})
}

// checkTimestamps verifies that each Object is the timestamp its Group ID
// names. The track is a clock, so the two have to agree exactly.
func checkTimestamps(t *testing.T, objects []*moqtransport.Object) {
	t.Helper()
	for _, o := range objects {
		want := formatSecond(time.Unix(int64(o.GroupID), 0))
		if string(o.Payload) != want {
			t.Errorf("group %d carried %q, want %q", o.GroupID, o.Payload, want)
		}
		if o.ObjectID != 0 {
			t.Errorf("group %d had object ID %d, want 0", o.GroupID, o.ObjectID)
		}
	}
}

func TestSubscribeOverQUIC(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	addr := startServer(t, ctx)
	got := newCollector(3)
	runSubscriber(t, ctx, addr, &moqHandler{onObject: got.add}, false)

	got.wait(t, ctx)
	objects := got.collected()
	checkTimestamps(t, objects)

	// One Object per second, in order.
	for i := 1; i < len(objects); i++ {
		if objects[i].GroupID != objects[i-1].GroupID+1 {
			t.Errorf("groups %d and %d are not consecutive", objects[i-1].GroupID, objects[i].GroupID)
		}
	}
}

func TestSubscribeOverWebTransport(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	addr := startServer(t, ctx)
	got := newCollector(2)
	runSubscriber(t, ctx, "https://"+addr+"/moq", &moqHandler{onObject: got.add}, true)

	got.wait(t, ctx)
	checkTimestamps(t, got.collected())
}

// A FETCH returns seconds that have already passed, which the publisher
// reconstructs from the Group IDs rather than remembering.
func TestFetchHistory(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	addr := startServer(t, ctx)
	const history = 4
	got := newCollector(history)
	runSubscriber(t, ctx, addr, &moqHandler{fetch: history, onObject: got.add}, false)

	got.wait(t, ctx)
	objects := got.collected()
	checkTimestamps(t, objects)

	// The fetch resolves before the subscription starts, so the first objects
	// are all in the past.
	now := uint64(time.Now().Unix())
	for _, o := range objects {
		if o.GroupID >= now {
			t.Errorf("fetched group %d is not in the past (now %d)", o.GroupID, now)
		}
	}
}

// A joining FETCH names the subscription it joins instead of a range, and the
// publisher works the range out from it. The point of that is this: what the
// fetch returns and what the subscription delivers meet exactly, with no gap
// at the join point and no Object delivered twice.
//
// A standalone FETCH cannot promise it. Issued before the SUBSCRIBE, its range
// is a guess, and whatever is published between the two falls in between them.
func TestJoiningFetchIsContiguousWithTheSubscription(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	addr := startServer(t, ctx)
	const behind = 4
	// Everything the fetch returns, and then two Objects from the live edge.
	got := newCollector(behind + 3)
	runSubscriber(t, ctx, addr, &moqHandler{join: behind, onObject: got.add}, false)

	got.wait(t, ctx)
	objects := got.collected()
	checkTimestamps(t, objects)

	for i := 1; i < len(objects); i++ {
		previous, current := objects[i-1].GroupID, objects[i].GroupID
		if current != previous+1 {
			t.Fatalf("groups %d and %d are not consecutive: the fetch and the subscription %s",
				previous, current,
				map[bool]string{true: "overlap", false: "leave a gap"}[current <= previous])
		}
	}

	// The fetch really did reach back: the run starts behind where the
	// subscription alone would have.
	if len(objects) < behind+1 {
		t.Fatalf("got %d objects, want at least %d", len(objects), behind+1)
	}
}

// A subscribe for a track the publisher does not have is refused, and the
// refusal reaches the caller as a RequestError rather than a dropped session.
func TestSubscribeUnknownTrack(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	addr := startServer(t, ctx)
	conn, err := dialQUIC(ctx, addr)
	if err != nil {
		t.Fatalf("dialling: %v", err)
	}
	session := &moqtransport.Session{}
	if err := session.Run(ctx, conn); err != nil {
		t.Fatalf("running the session: %v", err)
	}
	defer func() { _ = session.Close(moqtransport.SessionErrorNoError, "test over") }()

	_, err = session.Subscribe(ctx, []string{"clock"}, "minute")
	if err == nil {
		t.Fatal("subscribing to a track that does not exist succeeded")
	}
	var requestErr *moqtransport.RequestError
	if !errors.As(err, &requestErr) {
		t.Fatalf("got %v, want a *moqtransport.RequestError", err)
	}
	if requestErr.Code != moqtransport.RequestErrorDoesNotExist {
		t.Errorf("got %v, want DOES_NOT_EXIST", requestErr.Code)
	}
	if !strings.Contains(requestErr.Reason, "unknown track") {
		t.Errorf("reason %q does not name the problem", requestErr.Reason)
	}
}
