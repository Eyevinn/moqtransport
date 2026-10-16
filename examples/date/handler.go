package main

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/Eyevinn/moqtransport"
	"github.com/Eyevinn/moqtransport/quicmoq"
	"github.com/Eyevinn/moqtransport/webtransportmoq"
	"github.com/quic-go/quic-go"
	"github.com/quic-go/quic-go/http3"
	"github.com/quic-go/webtransport-go"
)

// maxFetchGroups bounds what one FETCH can ask for. The track is infinite in
// both directions, so without a bound a single request could ask this process
// to serialize the whole of Unix time.
const maxFetchGroups = 3600

type moqHandler struct {
	namespace []string
	trackname string
	publish   bool
	subscribe bool
	fetch     uint64
	join      uint64
	pause     time.Duration

	// alpns, when set, are the only drafts a client offers.
	alpns []string

	// onObject, when set, is called for every Object received. Nothing in the
	// command sets it; it is the seam the tests watch delivery through.
	onObject func(*moqtransport.Object)
}

// runServer listens for both native QUIC and WebTransport on one address. The
// two are told apart by the TLS ALPN: h3 is a WebTransport session to upgrade,
// anything else is MOQT straight over QUIC.
func (h *moqHandler) runServer(ctx context.Context, addr string, tlsConfig *tls.Config) error {
	listener, err := quic.ListenAddr(addr, tlsConfig, &quic.Config{
		EnableDatagrams: true,
		// Required by webtransport-go v0.12.0 (draft-ietf-webtrans-http3-16):
		// Server.ServeQUICConn rejects connections without it.
		EnableStreamResetPartialDelivery: true,
	})
	if err != nil {
		return err
	}
	defer func() { _ = listener.Close() }()

	wt := webtransport.Server{
		H3:                   &http3.Server{Addr: addr, TLSConfig: tlsConfig},
		ApplicationProtocols: moqtransport.SupportedALPNs(),
	}
	defer func() { _ = wt.Close() }()

	mux := http.NewServeMux()
	mux.HandleFunc("/moq", func(w http.ResponseWriter, r *http.Request) {
		session, err := wt.Upgrade(w, r)
		if err != nil {
			log.Printf("upgrade to webtransport failed: %v", err)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		h.handle(ctx, webtransportmoq.NewServer(session))
	})
	wt.H3.Handler = mux

	log.Printf("listening on %v (quic: %v, webtransport: https://%v/moq)",
		addr, moqtransport.SupportedALPNs(), addr)

	for {
		conn, err := listener.Accept(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		alpn := conn.ConnectionState().TLS.NegotiatedProtocol
		if alpn == "h3" {
			go func() {
				if err := wt.ServeQUICConn(conn); err != nil {
					log.Printf("serving webtransport connection failed: %v", err)
				}
			}()
			continue
		}
		go h.handle(ctx, quicmoq.NewServer(conn))
	}
}

func (h *moqHandler) runClient(ctx context.Context, addr string, useWebTransport bool) error {
	var conn moqtransport.Connection
	var err error
	if useWebTransport {
		conn, err = dialWebTransport(ctx, addr, h.alpns)
	} else {
		conn, err = dialQUIC(ctx, addr, h.alpns)
	}
	if err != nil {
		return err
	}
	h.handle(ctx, conn)
	return nil
}

// handle runs one session to completion.
func (h *moqHandler) handle(ctx context.Context, conn moqtransport.Connection) {
	session := &moqtransport.Session{
		Implementation: "Eyevinn/moqtransport date example",
	}
	// Handlers are only wired up for what this endpoint actually does. The
	// ones left nil answer with NOT_SUPPORTED, which is a real answer: a peer
	// that subscribes to a subscriber finds out immediately.
	if h.publish {
		session.SubscribeHandler = moqtransport.SubscribeHandlerFunc(h.handleSubscribe)
		session.FetchHandler = moqtransport.FetchHandlerFunc(h.handleFetch)
		session.FillHandler = moqtransport.FillHandlerFunc(h.handleFill)
	}
	if h.subscribe {
		session.PublishNamespaceHandler = moqtransport.PublishNamespaceHandlerFunc(h.handlePublishNamespace)
	}

	if err := session.Run(ctx, conn); err != nil {
		log.Printf("session setup failed: %v", err)
		return
	}
	log.Printf("session established, speaking %v", session.Version())

	if h.publish {
		publication, err := session.PublishNamespace(ctx, h.namespace)
		if err != nil {
			log.Printf("announcing namespace %v failed: %v", h.namespace, err)
		} else {
			// Closing this withdraws the announcement. There is no UNANNOUNCE
			// in draft-18; the stream ending is the message.
			defer func() { _ = publication.Close() }()
			log.Printf("announced namespace %v", h.namespace)
		}
	}
	if h.subscribe {
		if h.fetch > 0 {
			h.fetchHistory(ctx, session)
		}
		if err := h.subscribeAndRead(ctx, session); err != nil {
			// A refused subscription is the peer's answer, not a failure of
			// the session, so this leaves with NO_ERROR. Closing with
			// INTERNAL_ERROR here would put this endpoint's own reason string
			// on the peer's connection error, which reads as if the peer had
			// done something wrong.
			log.Printf("subscribing failed: %v", err)
			_ = session.Close(moqtransport.SessionErrorNoError, "nothing to subscribe to")
			return
		}
	}

	// The session outlives this function only through the goroutines it
	// started; wait here so that Ctrl-C reaches them.
	<-session.Context().Done()
	log.Printf("session ended: %v", context.Cause(session.Context()))
}

// handleSubscribe answers a SUBSCRIBE for the date track.
func (h *moqHandler) handleSubscribe(r *moqtransport.SubscribeRequest) {
	if !slices.Equal(r.Namespace(), h.namespace) || r.Track() != h.trackname {
		log.Printf("rejecting subscribe for %v/%v", r.Namespace(), r.Track())
		_ = r.Reject(moqtransport.RequestErrorDoesNotExist, "unknown track")
		return
	}

	// The largest Object the track has right now, so the subscriber knows
	// where the live edge is.
	subscription, err := r.Accept(moqtransport.WithLargestObject(moqtransport.Location{
		Group: uint64(time.Now().Unix()),
	}))
	if err != nil {
		log.Printf("accepting subscribe failed: %v", err)
		return
	}
	log.Printf("subscription established for %v/%v", r.Namespace(), r.Track())
	go h.publishDate(subscription)
}

// publishDate sends one Object per second until the subscriber goes away.
//
// Nothing tells it to stop. draft-18 has no UNSUBSCRIBE: a subscriber that
// loses interest resets its request stream, and the only sign of that is the
// subscription's context ending.
func (h *moqHandler) publishDate(subscription *moqtransport.Subscription) {
	// Ask for updates before publishing anything, and drain them: an update
	// that nobody reads eventually blocks this subscription's stream, and with
	// it the notice that the subscriber has gone.
	updates := subscription.Updates()

	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-subscription.Context().Done():
			log.Printf("subscriber went away: %v", context.Cause(subscription.Context()))
			return

		case update := <-updates:
			log.Printf("subscription updated: priority=%d forward=%v",
				update.SubscriberPriority, update.Forward)

		case now := <-ticker.C:
			if !subscription.Forward() {
				// Established but idle. A REQUEST_UPDATE can turn it back on.
				continue
			}
			if err := writeSecond(subscription, now); err != nil {
				log.Printf("publishing failed: %v", err)
				_ = subscription.Close(moqtransport.PublishDoneInternalError, err.Error())
				return
			}
		}
	}
}

// writeSecond publishes one timestamp as a Group of its own.
func writeSecond(subscription *moqtransport.Subscription, ts time.Time) error {
	// One Object per Group, so this subgroup is complete as soon as it is
	// written: EndOfGroup says a FIN means the Group is finished.
	subgroup, err := subscription.OpenSubgroup(uint64(ts.Unix()), 0, 128, moqtransport.WithEndOfGroup())
	if err != nil {
		return fmt.Errorf("opening subgroup: %w", err)
	}
	if _, err := subgroup.WriteObject(0, []byte(formatSecond(ts))); err != nil {
		subgroup.Reset(moqtransport.StreamErrorInternal)
		return fmt.Errorf("writing object: %w", err)
	}
	return subgroup.Close()
}

// handleFetch answers a FETCH for a range of seconds.
//
// The track is a clock, so every Group in the past can be reconstructed
// exactly from its Group ID; nothing has to be kept.
func (h *moqHandler) handleFetch(r *moqtransport.FetchRequest) {
	if !slices.Equal(r.Namespace(), h.namespace) || r.Track() != h.trackname {
		_ = r.Reject(moqtransport.RequestErrorDoesNotExist, "unknown track")
		return
	}
	// The clock's Largest Object is this second's, the only Object in its
	// Group. Resolving against it is what turns a relative or open-ended
	// request into Groups, and it clamps the end to now.
	now := uint64(time.Now().Unix())
	rng, ok := r.Range(moqtransport.Location{Group: now}, true)
	if !ok {
		_ = r.Reject(moqtransport.RequestErrorInvalidRange, "that range is in the future")
		return
	}
	if rng.End.Group-rng.Start.Group > maxFetchGroups {
		_ = r.Reject(moqtransport.RequestErrorInvalidRange,
			fmt.Sprintf("at most %d seconds can be fetched at once", maxFetchGroups))
		return
	}

	// FETCH_OK reports the resolved range's end, which Range recorded.
	response, err := r.Accept()
	if err != nil {
		log.Printf("accepting fetch failed: %v", err)
		return
	}
	// The handler does not need to know which kind it is: a joining fetch
	// arrives with its range already resolved against the subscription it
	// names, so both look the same from here.
	if joining, ok := r.Joining(); ok {
		log.Printf("serving joining fetch for subscription %d: groups %d..%d",
			joining, rng.Start.Group, rng.End.Group)
	} else {
		log.Printf("serving fetch for groups %d..%d", rng.Start.Group, rng.End.Group)
	}
	writeSeconds(response, rng)
}

// handleFill serves the fill a draft-22 subscriber asked for along with its
// subscription. The library has already resolved the range against the
// Largest Object the subscription reported, and decided there is something to
// send; serving it is the same as serving a FETCH.
func (h *moqHandler) handleFill(r *moqtransport.FillRequest) {
	rng := r.Range()
	if rng.End.Group-rng.Start.Group > maxFetchGroups {
		_ = r.Reject(moqtransport.StreamErrorExcessiveLoad)
		return
	}
	response, err := r.Accept()
	if err != nil {
		log.Printf("accepting fill failed: %v", err)
		return
	}
	log.Printf("serving fill %d for groups %d..%d", r.RequestID(), rng.Start.Group, rng.End.Group)
	writeSeconds(response, rng)
}

// writeSeconds writes the one Object of every second in rng and finishes the
// response.
func writeSeconds(response *moqtransport.FetchResponse, rng moqtransport.LocationRange) {
	for group := rng.Start.Group; group <= rng.End.Group; group++ {
		// A Start Location past the only Object in its Group excludes it.
		if !rng.Contains(moqtransport.Location{Group: group}) {
			continue
		}
		if err := response.WriteObject(moqtransport.Object{
			GroupID:  group,
			ObjectID: 0,
			Priority: 128,
			Payload:  []byte(formatSecond(time.Unix(int64(group), 0))),
		}); err != nil {
			log.Printf("writing fetch response failed: %v", err)
			response.Reset(moqtransport.StreamErrorInternal)
			return
		}
	}
	// The FIN is what says the range is complete: any Group missing from it up
	// to the End Location does not exist.
	if err := response.Close(); err != nil {
		log.Printf("closing fetch response failed: %v", err)
	}
}

// handlePublishNamespace accepts an announcement for the namespace this
// endpoint is interested in.
func (h *moqHandler) handlePublishNamespace(r *moqtransport.PublishNamespaceRequest) {
	if !slices.Equal(r.Namespace(), h.namespace) {
		log.Printf("rejecting announcement of %v", r.Namespace())
		_ = r.Reject(moqtransport.RequestErrorUninterested, "not interested in that namespace")
		return
	}
	if err := r.Accept(); err != nil {
		log.Printf("accepting announcement failed: %v", err)
		return
	}
	log.Printf("peer announced namespace %v", r.Namespace())
}

// fetchHistory retrieves the seconds before now with a standalone FETCH, which
// names an explicit range this endpoint worked out for itself.
//
// It runs before subscribing, so the range has to be guessed: there is no
// subscription yet to be contiguous with, and whatever is published between
// the fetch and the SUBSCRIBE falls in the gap between them. Compare
// joiningFetch, which is the answer to that.
func (h *moqHandler) fetchHistory(ctx context.Context, session *moqtransport.Session) {
	now := time.Now().Unix()
	start := moqtransport.Location{Group: uint64(now - int64(h.fetch))}
	// Through the whole of the previous second's Group.
	end := moqtransport.Location{Group: uint64(now - 1), Object: moqtransport.MaxObjectID}

	fetch, err := session.Fetch(ctx, h.namespace, h.trackname, moqtransport.AbsoluteRange(start, end))
	if err != nil {
		log.Printf("fetch failed: %v", err)
		return
	}
	h.drainFetch(ctx, fetch, "fetched")
}

// joiningFetch retrieves the seconds behind an established draft-18
// subscription, the way a player fills a buffer before playing from the live
// edge.
//
// The range is not this endpoint's to compute. It names the subscription and
// how far back to go, and the publisher works out the rest from that
// subscription's Joining Location, so what the fetch returns and what the
// subscription delivers are contiguous and do not overlap -- no gap at the
// join point, and no Object delivered twice. Draft-22 does the same with a
// fill; see subscribeAndRead.
func (h *moqHandler) joiningFetch(ctx context.Context, session *moqtransport.Session, track *moqtransport.RemoteTrack) {
	fetch, err := session.FetchRelative(ctx, track, h.join)
	if err != nil {
		log.Printf("joining fetch failed: %v", err)
		return
	}
	log.Printf("joining fetch accepted, ends at group %d", fetch.EndLocation().Group)
	h.drainFetch(ctx, fetch, "joined ")
}

// fetchReader is what a FETCH response and a fill have in common: a finite
// run of records that ends with ErrFetchComplete.
type fetchReader interface {
	ReadObject(ctx context.Context) (*moqtransport.FetchObject, error)
}

// drainFetch reads a whole FETCH response or fill. Reaching the end of one is
// an ending of its own: it is finite, unlike a subscription.
func (h *moqHandler) drainFetch(ctx context.Context, fetch fetchReader, label string) {
	for {
		record, err := fetch.ReadObject(ctx)
		if err != nil {
			if errors.Is(err, moqtransport.ErrFetchComplete) {
				log.Printf("%s complete", strings.TrimSpace(label))
			} else {
				log.Printf("%s ended early: %v", strings.TrimSpace(label), err)
			}
			return
		}
		if record.EndOfRange != 0 {
			// A marker rather than an Object: everything up to here is absent
			// or of unknown status.
			log.Printf("%s nothing up to group %d object %d",
				label, record.GroupID, record.ObjectID)
			continue
		}
		log.Printf("%s group %d object %d: %s", label, record.GroupID, record.ObjectID, record.Payload)
		if h.onObject != nil {
			h.onObject(&record.Object)
		}
	}
}

// subscribeAndRead subscribes to the date track and logs what arrives.
func (h *moqHandler) subscribeAndRead(ctx context.Context, session *moqtransport.Session) error {
	// Draft-22 joins in the SUBSCRIBE itself: the subscription starts at the
	// Next Object and the fill delivers what came before it, back to h.join
	// seconds behind the live edge. RelativeStart counts from the Next
	// Group, so 1 is the current second and h.join+1 reaches h.join behind it.
	fill := h.join > 0 && session.Version() != moqtransport.Version18
	var opts []moqtransport.SubscribeOption
	if fill {
		opts = append(opts,
			moqtransport.WithFilter(moqtransport.NextObject()),
			moqtransport.WithFill(moqtransport.FillRange(moqtransport.RelativeStart(h.join+1))))
	}
	track, err := session.Subscribe(ctx, h.namespace, h.trackname, opts...)
	if err != nil {
		return err
	}
	if largest, ok := track.LargestObject(); ok {
		log.Printf("subscribed, live edge is group %d", largest.Group)
	}
	switch {
	case fill:
		h.drainFetch(ctx, track.Fill(), "filled ")
	case h.join > 0:
		// A joining fetch has to come after the SUBSCRIBE it joins: it names
		// that subscription, and the publisher resolves the range against
		// it.
		h.joiningFetch(ctx, session, track)
	}
	if h.pause > 0 {
		go h.pauseAndResume(ctx, track)
	}

	go func() {
		for {
			object, err := track.ReadObject(ctx)
			if err != nil {
				if done, ok := track.PublishDone(); ok {
					log.Printf("publisher finished: %v: %v", done.Code, done.Reason)
					return
				}
				log.Printf("subscription ended: %v", err)
				return
			}
			log.Printf("received group %d object %d (%d bytes): %s",
				object.GroupID, object.ObjectID, len(object.Payload), object.Payload)
			if h.onObject != nil {
				h.onObject(object)
			}
		}
	}()
	return nil
}

// pauseAndResume turns delivery off and on again, the way a player that has
// been paused would.
//
// A REQUEST_UPDATE travels on the subscription's own stream rather than being
// a session-level message, so there is no correlation ID to pass: the stream
// already says which subscription is meant.
func (h *moqHandler) pauseAndResume(ctx context.Context, track *moqtransport.RemoteTrack) {
	if !sleep(ctx, h.pause) {
		return
	}
	log.Printf("pausing delivery")
	if err := track.Update(moqtransport.WithUpdatedForward(false)); err != nil {
		log.Printf("pausing failed: %v", err)
		return
	}
	if !sleep(ctx, h.pause) {
		return
	}
	log.Printf("resuming delivery")
	if err := track.Update(moqtransport.WithUpdatedForward(true)); err != nil {
		log.Printf("resuming failed: %v", err)
	}
}

// sleep waits for d, reporting whether it got there before ctx ended.
func sleep(ctx context.Context, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-timer.C:
		return true
	case <-ctx.Done():
		return false
	}
}

func formatSecond(ts time.Time) string {
	return ts.UTC().Format(time.RFC3339)
}
