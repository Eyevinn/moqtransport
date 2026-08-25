package moqtransport

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sync/atomic"
	"time"
)

var errTooManyFetchStreams = errors.New("got too many fetch streams for remote track")

// ErrSubscribeDone is returned when reading from a RemoteTrack when the
// subscription has ended.
type ErrSubscribeDone struct {
	Status uint64
	Reason string
}

// Error implements error
func (e ErrSubscribeDone) Error() string {
	return fmt.Sprintf("subscribe done: status=%v, reason='%v'", e.Status, e.Reason)
}

// RemoteTrack is a track provided by the remote peer.
type RemoteTrack struct {
	requestID uint64

	// trackAlias is written on SUBSCRIBE_OK, before the pending
	// subscribe resolves — so it is safely readable once Subscribe
	// returns.
	trackAlias    uint64
	hasTrackAlias bool

	// Expires, groupOrder, ..., parameters are returned in the SUBSCRIBE_OK.
	// They are not updated when sending a SUBSCRIBE_UPDATE message.
	expires         time.Duration
	groupOrder      GroupOrder
	contentExists   bool
	largestLocation *Location // Only set iff ContentExists is true
	parameters      KVPList

	logger          *slog.Logger
	unsubscribeFunc func() error
	updateFunc      func(context.Context, ...SubscribeUpdateOption) error
	buffer          chan *Object

	doneCtx       context.Context
	doneCtxCancel context.CancelCauseFunc

	subGroupCount atomic.Uint64
	fetchCount    atomic.Uint64 // should never grow larger than one for now.

	responseChan chan error
}

// RequestID returns the request ID of the subscription request.
func (t *RemoteTrack) RequestID() uint64 {
	return t.requestID
}

// TrackAlias returns the publisher-assigned track alias delivered in
// SUBSCRIBE_OK — the value identifying this track in object datagrams
// and subgroup stream headers. Valid once Subscribe has returned; ok is
// false before the SUBSCRIBE_OK arrived.
func (t *RemoteTrack) TrackAlias() (alias uint64, ok bool) {
	return t.trackAlias, t.hasTrackAlias
}

// Expires returns the duration for which the subscription is valid.
// A value of 0 indicates that the subscription does not expire or expires at an unknown time.
func (t *RemoteTrack) Expires() time.Duration {
	return t.expires
}

// GroupOrder returns the group order for this track.
func (t *RemoteTrack) GroupOrder() GroupOrder {
	return t.groupOrder
}

// LargestLocation returns the largest location for this track if content exists.
// Returns false if ContentExists is false or if no LargestLocation was provided.
func (t *RemoteTrack) LargestLocation() (Location, bool) {
	if t.contentExists && t.largestLocation != nil {
		return *t.largestLocation, true
	}
	return Location{}, false
}

// Parameters returns the key-value parameters for this track.
func (t *RemoteTrack) Parameters() KVPList {
	return t.parameters
}

// UpdateSubscription updates the subscription parameters for this track.
// No response is expected according to draft-14 specification.
func (t *RemoteTrack) UpdateSubscription(ctx context.Context, options ...SubscribeUpdateOption) error {
	if t.updateFunc == nil {
		return errors.New("update function not available")
	}
	return t.updateFunc(ctx, options...)
}

func newRemoteTrack(requestID uint64, unsubscribeFunc func() error, updateFunc func(context.Context, ...SubscribeUpdateOption) error) *RemoteTrack {
	ctx, cancel := context.WithCancelCause(context.Background())
	t := &RemoteTrack{
		requestID:       requestID,
		logger:          defaultLogger,
		unsubscribeFunc: unsubscribeFunc,
		updateFunc:      updateFunc,
		buffer:          make(chan *Object, 100),
		doneCtx:         ctx,
		doneCtxCancel:   cancel,
		subGroupCount:   atomic.Uint64{},
		fetchCount:      atomic.Uint64{},
		responseChan:    make(chan error, 1),
	}
	return t
}

// ReadObject returns the next object received from the peer.
func (t *RemoteTrack) ReadObject(ctx context.Context) (*Object, error) {
	select {
	case <-ctx.Done():
		return nil, context.Cause(ctx)
	case <-t.doneCtx.Done():
		return nil, context.Cause(t.doneCtx)
	case obj := <-t.buffer:
		return obj, t.doneCtx.Err()
	}
}

// Close implements io.Closer. Calling close unsubscribes from the subscription.
func (t *RemoteTrack) Close() error {
	if t.unsubscribeFunc != nil {
		return t.unsubscribeFunc()
	}
	return nil
}

func (t *RemoteTrack) readFetchStream(ctx context.Context, parser objectMessageParser) error {
	if t.fetchCount.Add(1) > 1 {
		return errTooManyFetchStreams
	}
	return t.readStream(ctx, parser)
}

func (t *RemoteTrack) readSubgroupStream(ctx context.Context, parser objectMessageParser) error {
	t.subGroupCount.Add(1)
	return t.readStream(ctx, parser)
}

// readStream reads objects from a subgroup or fetch stream until EOF. ctx
// is the session context: when the session closes, a stream reader blocked
// on delivery must exit rather than wait for a consumer that will never
// come.
func (t *RemoteTrack) readStream(ctx context.Context, parser objectMessageParser) error {
	for m, err := range parser.Messages() {
		if err != nil {
			if err == io.EOF {
				return nil
			}
			return err
		}
		t.logger.Debug("subgroup got new object message", "message", m)
		payload := make([]byte, len(m.ObjectPayload))
		n := copy(payload, m.ObjectPayload)
		if n != len(m.ObjectPayload) {
			// TODO
			return errors.New("failed to copy object payload: copied less bytes than expected")
		}
		if err := t.pushBlocking(ctx, &Object{
			GroupID:          m.GroupID,
			SubGroupID:       m.SubgroupID,
			ObjectID:         m.ObjectID,
			ExtensionHeaders: FromWire(m.ObjectExtensionHeaders),
			Payload:          payload,
		}); err != nil {
			return err
		}
	}
	return nil
}

// pushBlocking delivers an object read from an ordered, reliable stream.
// Blocking (rather than dropping) lets QUIC stream flow control apply
// backpressure to the sender; dropping would silently violate the
// ordered-delivery semantics applications rely on for stream tracks.
// Datagram delivery keeps the dropping push: loss is normal there.
//
// It returns when the object is delivered, the subscription ends
// (SUBSCRIBE_DONE), or the session context is cancelled — the last so a
// stream reader cannot outlive Session.Close when the application has
// stopped draining the track.
func (t *RemoteTrack) pushBlocking(ctx context.Context, o *Object) error {
	select {
	case t.buffer <- o:
		return nil
	case <-t.doneCtx.Done():
		return context.Cause(t.doneCtx)
	case <-ctx.Done():
		return context.Cause(ctx)
	}
}

func (t *RemoteTrack) done(status uint64, reason string) {
	t.doneCtxCancel(&ErrSubscribeDone{
		Status: status,
		Reason: reason,
	})
}

func (t *RemoteTrack) push(o *Object) {
	select {
	case t.buffer <- o:
	default:
		t.logger.Info("buffer overflow: dropping incoming object")
	}
}
