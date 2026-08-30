package moqtransport

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"

	"github.com/Eyevinn/moqtransport/internal/wire2"
)

// PublishNamespaceHandler answers PUBLISH_NAMESPACE requests, by which a
// publisher advertises that it has tracks under a namespace
// (draft-ietf-moq-transport-18, Section 10.15).
//
// The announcement lasts as long as the request stream. There is no
// UNANNOUNCE: the publisher withdrawing it ends the stream, which cancels the
// request's Context.
type PublishNamespaceHandler interface {
	HandlePublishNamespace(*PublishNamespaceRequest)
}

// PublishNamespaceHandlerFunc adapts a function to [PublishNamespaceHandler].
type PublishNamespaceHandlerFunc func(*PublishNamespaceRequest)

func (f PublishNamespaceHandlerFunc) HandlePublishNamespace(r *PublishNamespaceRequest) { f(r) }

// SubscribeNamespaceHandler answers SUBSCRIBE_NAMESPACE requests, by which a
// subscriber asks for the namespaces matching a prefix and for changes to that
// set (Section 10.18).
type SubscribeNamespaceHandler interface {
	HandleSubscribeNamespace(*SubscribeNamespaceRequest)
}

// SubscribeNamespaceHandlerFunc adapts a function to
// [SubscribeNamespaceHandler].
type SubscribeNamespaceHandlerFunc func(*SubscribeNamespaceRequest)

func (f SubscribeNamespaceHandlerFunc) HandleSubscribeNamespace(r *SubscribeNamespaceRequest) { f(r) }

// PublishNamespaceRequest is an incoming PUBLISH_NAMESPACE.
type PublishNamespaceRequest struct {
	*requestStream

	requestID uint64
	namespace []string
	params    wire2.Parameters

	mu       sync.Mutex
	answered bool
}

func newPublishNamespaceRequest(rs *requestStream, msg *wire2.PublishNamespace) *PublishNamespaceRequest {
	return &PublishNamespaceRequest{
		requestStream: rs,
		requestID:     msg.RequestID,
		namespace:     namespaceStrings(msg.TrackNamespace),
		params:        msg.Parameters,
	}
}

// RequestID is the ID the publisher assigned to this PUBLISH_NAMESPACE.
func (r *PublishNamespaceRequest) RequestID() uint64 { return r.requestID }

// Namespace is the Track Namespace being advertised.
func (r *PublishNamespaceRequest) Namespace() []string { return r.namespace }

// Parameters returns the Message Parameters the publisher sent.
func (r *PublishNamespaceRequest) Parameters() Parameters { return r.params }

// Accept answers with REQUEST_OK. The stream stays open afterwards: it is what
// carries the announcement, and its ending is what withdraws it.
func (r *PublishNamespaceRequest) Accept() error {
	if err := r.claim(); err != nil {
		return err
	}
	return r.write(&wire2.RequestOk{Parameters: wire2.Parameters{}})
}

// Reject answers with REQUEST_ERROR and finishes the stream.
func (r *PublishNamespaceRequest) Reject(code RequestErrorCode, reason string) error {
	if err := r.claim(); err != nil {
		return err
	}
	err := r.finish(&wire2.RequestError{ErrorCode: uint64(code), ErrorReason: reason})
	r.cancelCtx(fmt.Errorf("rejected the namespace: %v", code))
	return err
}

func (r *PublishNamespaceRequest) claim() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.answered {
		return errRequestAlreadyAnswered
	}
	r.answered = true
	return nil
}

func (r *PublishNamespaceRequest) serve(h PublishNamespaceHandler) error {
	if h == nil {
		return r.Reject(RequestErrorNotSupported, "publish namespace is not supported")
	}
	go h.HandlePublishNamespace(r)
	return r.run(r.handleMessage)
}

func (r *PublishNamespaceRequest) handleMessage(msg wire2.ControlMessage) error {
	if _, ok := msg.(*wire2.RequestUpdate); !ok {
		return errUnexpectedMessageOnRequestStream
	}
	// Nothing in a PUBLISH_NAMESPACE's parameters changes what it means, but
	// Section 10.9 still requires exactly one answer per update.
	return r.write(&wire2.RequestOk{Parameters: wire2.Parameters{}})
}

// NamespacePublication is an announcement this endpoint made: a
// PUBLISH_NAMESPACE the peer accepted.
type NamespacePublication struct {
	*requestStream

	requestID uint64
	namespace []string

	established   chan struct{}
	establishOnce sync.Once

	mu        sync.Mutex
	answerErr error
}

// RequestID is the ID this endpoint assigned to the PUBLISH_NAMESPACE.
func (p *NamespacePublication) RequestID() uint64 { return p.requestID }

// Namespace is the Track Namespace being advertised.
func (p *NamespacePublication) Namespace() []string { return p.namespace }

// Close withdraws the announcement by finishing its stream, which is what
// draft-18 uses in place of UNANNOUNCE.
func (p *NamespacePublication) Close() error {
	err := p.closeSend()
	p.cancel(StreamErrorCancelled, errNamespaceWithdrawn)
	return err
}

func (p *NamespacePublication) run() error {
	err := p.requestStream.run(p.handleMessage)
	p.establishOnce.Do(func() {
		p.mu.Lock()
		if p.answerErr == nil {
			p.answerErr = context.Cause(p.ctx)
		}
		p.mu.Unlock()
		close(p.established)
	})
	return err
}

func (p *NamespacePublication) handleMessage(msg wire2.ControlMessage) error {
	switch m := msg.(type) {
	case *wire2.RequestOk:
		if len(m.TrackProperties) > 0 {
			return errUnexpectedTrackProperties
		}
		p.establishOnce.Do(func() { close(p.established) })
		return nil
	case *wire2.RequestError:
		p.mu.Lock()
		p.answerErr = &RequestError{Code: RequestErrorCode(m.ErrorCode), Reason: m.ErrorReason}
		p.mu.Unlock()
		p.establishOnce.Do(func() { close(p.established) })
		return nil
	}
	return errUnexpectedMessageOnRequestStream
}

func (p *NamespacePublication) awaitEstablished(ctx context.Context) error {
	select {
	case <-p.established:
	case <-ctx.Done():
		return ctx.Err()
	case <-p.ctx.Done():
		return context.Cause(p.ctx)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.answerErr
}

// PublishNamespace advertises that this endpoint has tracks under a namespace,
// and waits for the peer to accept it.
//
// The announcement lasts until the returned handle is closed or the session
// ends.
func (s *Session) PublishNamespace(ctx context.Context, namespace []string) (*NamespacePublication, error) {
	msg := &wire2.PublishNamespace{
		RequestID:      s.requestIDs.nextID(),
		TrackNamespace: namespaceFields(namespace),
		Parameters:     wire2.Parameters{},
	}

	stream, err := s.conn.OpenStreamSync(ctx)
	if err != nil {
		return nil, err
	}
	rs := newRequestStream(s.ctx, stream)
	publication := &NamespacePublication{
		requestStream: rs,
		requestID:     msg.RequestID,
		namespace:     namespace,
		established:   make(chan struct{}),
	}

	go func() {
		if err := publication.run(); err != nil {
			s.failIfProtocolError(err)
		}
	}()

	if err := rs.write(msg); err != nil {
		rs.cancel(StreamErrorInternal, err)
		return nil, err
	}
	if err := publication.awaitEstablished(ctx); err != nil {
		rs.cancel(StreamErrorCancelled, err)
		return nil, err
	}
	return publication, nil
}

// SubscribeNamespaceRequest is an incoming SUBSCRIBE_NAMESPACE.
type SubscribeNamespaceRequest struct {
	*requestStream

	requestID uint64
	prefix    []string
	params    wire2.Parameters

	mu       sync.Mutex
	answered bool
}

func newSubscribeNamespaceRequest(rs *requestStream, msg *wire2.SubscribeNamespace) *SubscribeNamespaceRequest {
	return &SubscribeNamespaceRequest{
		requestStream: rs,
		requestID:     msg.RequestID,
		prefix:        namespaceStrings(msg.TrackNamespacePrefix),
		params:        msg.Parameters,
	}
}

// RequestID is the ID the subscriber assigned to this SUBSCRIBE_NAMESPACE.
func (r *SubscribeNamespaceRequest) RequestID() uint64 { return r.requestID }

// Prefix is the Track Namespace Prefix to match against. An empty prefix
// matches every namespace.
func (r *SubscribeNamespaceRequest) Prefix() []string { return r.prefix }

// Parameters returns the Message Parameters the subscriber sent.
func (r *SubscribeNamespaceRequest) Parameters() Parameters { return r.params }

// Accept answers with REQUEST_OK and returns the side that announces
// namespaces. Announcements cannot be sent before it returns.
func (r *SubscribeNamespaceRequest) Accept() (*NamespaceAnnouncer, error) {
	r.mu.Lock()
	if r.answered {
		r.mu.Unlock()
		return nil, errRequestAlreadyAnswered
	}
	r.answered = true
	r.mu.Unlock()

	if err := r.write(&wire2.RequestOk{Parameters: wire2.Parameters{}}); err != nil {
		return nil, err
	}
	return &NamespaceAnnouncer{requestStream: r.requestStream, prefix: r.prefix}, nil
}

// Reject answers with REQUEST_ERROR and finishes the stream.
func (r *SubscribeNamespaceRequest) Reject(code RequestErrorCode, reason string) error {
	r.mu.Lock()
	if r.answered {
		r.mu.Unlock()
		return errRequestAlreadyAnswered
	}
	r.answered = true
	r.mu.Unlock()

	err := r.finish(&wire2.RequestError{ErrorCode: uint64(code), ErrorReason: reason})
	r.cancelCtx(fmt.Errorf("rejected the namespace subscription: %v", code))
	return err
}

func (r *SubscribeNamespaceRequest) serve(h SubscribeNamespaceHandler) error {
	if h == nil {
		return r.Reject(RequestErrorNotSupported, "subscribe namespace is not supported")
	}
	go h.HandleSubscribeNamespace(r)
	return r.run(r.handleMessage)
}

func (r *SubscribeNamespaceRequest) handleMessage(msg wire2.ControlMessage) error {
	if _, ok := msg.(*wire2.RequestUpdate); !ok {
		return errUnexpectedMessageOnRequestStream
	}
	// Section 10.9.2 allows a REQUEST_UPDATE to change the prefix. Changing it
	// would change what the suffixes are relative to, so until that is
	// implemented the honest answer is that the update failed.
	return r.write(&wire2.RequestError{
		ErrorCode:   uint64(RequestErrorNotSupported),
		ErrorReason: "updating a namespace subscription is not supported",
	})
}

// NamespaceAnnouncer is the publishing side of an accepted
// SUBSCRIBE_NAMESPACE.
type NamespaceAnnouncer struct {
	*requestStream

	prefix []string
}

// Prefix is the Track Namespace Prefix this subscription matched on.
func (a *NamespaceAnnouncer) Prefix() []string { return a.prefix }

// Announce sends NAMESPACE for a namespace under the subscription's prefix.
//
// Only the part after the prefix goes on the wire, so the namespace must
// actually begin with it.
func (a *NamespaceAnnouncer) Announce(namespace []string) error {
	suffix, ok := namespaceSuffix(a.prefix, namespace)
	if !ok {
		return errNamespaceOutsidePrefix
	}
	return a.write(&wire2.Namespace{TrackNamespaceSuffix: namespaceFields(suffix)})
}

// Done sends NAMESPACE_DONE, saying this endpoint will not serve new
// subscriptions for tracks under the namespace.
//
// It must follow the Announce for the same namespace: a NAMESPACE_DONE the
// subscriber has seen no NAMESPACE for closes the session.
func (a *NamespaceAnnouncer) Done(namespace []string) error {
	suffix, ok := namespaceSuffix(a.prefix, namespace)
	if !ok {
		return errNamespaceOutsidePrefix
	}
	return a.write(&wire2.NamespaceDone{TrackNamespaceSuffix: namespaceFields(suffix)})
}

// Close ends the namespace subscription. Section 10.18 has the subscriber
// treat that as a NAMESPACE_DONE for every namespace still active, so there is
// no need to send them one by one.
func (a *NamespaceAnnouncer) Close() error {
	err := a.closeSend()
	a.cancel(StreamErrorCancelled, errNamespaceSubscriptionClosed)
	return err
}

// NamespaceEvent is a change in the set of namespaces matching a
// subscription's prefix.
type NamespaceEvent struct {
	// Namespace is the full Track Namespace, with the subscription's prefix
	// already prepended -- the wire carries only the suffix.
	Namespace []string

	// Available is true for NAMESPACE and false for NAMESPACE_DONE.
	//
	// Every namespace still available when the subscription ends is reported
	// as unavailable, which is what Section 10.18 asks a subscriber to infer
	// from a FIN or a reset.
	Available bool
}

// NamespaceSubscription is a SUBSCRIBE_NAMESPACE this endpoint made.
type NamespaceSubscription struct {
	*requestStream

	requestID uint64
	prefix    []string

	established   chan struct{}
	establishOnce sync.Once

	// events is written and closed only by this request's reader goroutine, so
	// there is never a send in flight when it closes.
	events chan NamespaceEvent

	mu        sync.Mutex
	answerErr error
	active    [][]string
}

// namespaceEventBuffer is how many events are queued before a consumer that
// has stopped reading blocks this subscription's stream.
const namespaceEventBuffer = 64

// RequestID is the ID this endpoint assigned to the SUBSCRIBE_NAMESPACE.
func (n *NamespaceSubscription) RequestID() uint64 { return n.requestID }

// Prefix is the Track Namespace Prefix this subscription matches on.
func (n *NamespaceSubscription) Prefix() []string { return n.prefix }

// Namespaces returns the channel namespace changes arrive on. It is closed
// when the subscription ends, so a range over it terminates on its own.
//
// The channel is created before the request is sent and nothing is dropped, so
// the initial set is complete. Drain it: a consumer that stops reading
// eventually blocks this subscription's stream.
func (n *NamespaceSubscription) Namespaces() <-chan NamespaceEvent { return n.events }

// Active returns the namespaces currently available under the prefix.
func (n *NamespaceSubscription) Active() [][]string {
	n.mu.Lock()
	defer n.mu.Unlock()
	return slices.Clone(n.active)
}

// Close ends the namespace subscription. Section 6.1 allows either a FIN or a
// reset; this finishes the stream and stops reading.
func (n *NamespaceSubscription) Close() error {
	err := n.closeSend()
	n.cancel(StreamErrorCancelled, errNamespaceSubscriptionClosed)
	return err
}

func (n *NamespaceSubscription) run() error {
	err := n.requestStream.run(n.handleMessage)

	n.establishOnce.Do(func() {
		n.mu.Lock()
		if n.answerErr == nil {
			n.answerErr = context.Cause(n.ctx)
		}
		n.mu.Unlock()
		close(n.established)
	})

	// A FIN or reset means every namespace still active is gone
	// (Section 10.18), and the application should not have to know that rule.
	n.mu.Lock()
	remaining := n.active
	n.active = nil
	n.mu.Unlock()
	for _, namespace := range remaining {
		select {
		case n.events <- NamespaceEvent{Namespace: namespace}:
		default:
			// A consumer that has stopped reading gets no closing summary; the
			// channel closing is signal enough.
		}
	}
	close(n.events)
	return err
}

func (n *NamespaceSubscription) handleMessage(msg wire2.ControlMessage) error {
	switch m := msg.(type) {
	case *wire2.RequestOk:
		if len(m.TrackProperties) > 0 {
			return errUnexpectedTrackProperties
		}
		n.establishOnce.Do(func() { close(n.established) })
		return nil

	case *wire2.RequestError:
		n.mu.Lock()
		n.answerErr = &RequestError{Code: RequestErrorCode(m.ErrorCode), Reason: m.ErrorReason}
		n.mu.Unlock()
		n.establishOnce.Do(func() { close(n.established) })
		return nil

	case *wire2.Namespace:
		return n.emit(namespaceStrings(m.TrackNamespaceSuffix), true)

	case *wire2.NamespaceDone:
		return n.emit(namespaceStrings(m.TrackNamespaceSuffix), false)
	}
	return errUnexpectedMessageOnRequestStream
}

// emit records a namespace change and reports it.
func (n *NamespaceSubscription) emit(suffix []string, available bool) error {
	namespace := append(slices.Clone(n.prefix), suffix...)

	n.mu.Lock()
	index := slices.IndexFunc(n.active, func(existing []string) bool {
		return slices.Equal(existing, namespace)
	})
	if available {
		if index < 0 {
			n.active = append(n.active, namespace)
		}
	} else {
		if index < 0 {
			// Section 10.18: a NAMESPACE_DONE the subscriber has seen no
			// NAMESPACE for closes the session.
			n.mu.Unlock()
			return errNamespaceDoneBeforeNamespace
		}
		n.active = slices.Delete(n.active, index, index+1)
	}
	n.mu.Unlock()

	select {
	case n.events <- NamespaceEvent{Namespace: namespace, Available: available}:
		return nil
	case <-n.ctx.Done():
		return context.Cause(n.ctx)
	}
}

func (n *NamespaceSubscription) awaitEstablished(ctx context.Context) error {
	select {
	case <-n.established:
	case <-ctx.Done():
		return ctx.Err()
	case <-n.ctx.Done():
		return context.Cause(n.ctx)
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.answerErr
}

// SubscribeNamespace asks the peer for the namespaces matching a prefix, and
// for changes to that set. An empty prefix asks for all of them.
func (s *Session) SubscribeNamespace(ctx context.Context, prefix []string) (*NamespaceSubscription, error) {
	msg := &wire2.SubscribeNamespace{
		RequestID:            s.requestIDs.nextID(),
		TrackNamespacePrefix: namespaceFields(prefix),
		Parameters:           wire2.Parameters{},
	}

	stream, err := s.conn.OpenStreamSync(ctx)
	if err != nil {
		return nil, err
	}
	rs := newRequestStream(s.ctx, stream)
	subscription := &NamespaceSubscription{
		requestStream: rs,
		requestID:     msg.RequestID,
		prefix:        slices.Clone(prefix),
		established:   make(chan struct{}),
		events:        make(chan NamespaceEvent, namespaceEventBuffer),
	}

	go func() {
		if err := subscription.run(); err != nil {
			s.failIfProtocolError(err)
		}
	}()

	if err := rs.write(msg); err != nil {
		rs.cancel(StreamErrorInternal, err)
		return nil, err
	}
	if err := subscription.awaitEstablished(ctx); err != nil {
		rs.cancel(StreamErrorCancelled, err)
		return nil, err
	}
	return subscription, nil
}

// prefixRegistry enforces the overlap rule of Sections 10.18 and 10.19: within
// a session, two namespace subscriptions may not share a common prefix.
//
// The check and the reservation are one step, because two overlapping requests
// arriving together are each dispatched on their own goroutine and would
// otherwise both find the other absent.
type prefixRegistry struct {
	mu       sync.Mutex
	prefixes [][]string
}

func newPrefixRegistry() *prefixRegistry { return &prefixRegistry{} }

// reserve takes a prefix if nothing overlapping holds one.
func (r *prefixRegistry) reserve(prefix []string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, existing := range r.prefixes {
		if overlappingPrefixes(existing, prefix) {
			return false
		}
	}
	r.prefixes = append(r.prefixes, slices.Clone(prefix))
	return true
}

func (r *prefixRegistry) release(prefix []string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	index := slices.IndexFunc(r.prefixes, func(existing []string) bool {
		return slices.Equal(existing, prefix)
	})
	if index >= 0 {
		r.prefixes = slices.Delete(r.prefixes, index, index+1)
	}
}

// overlappingPrefixes reports whether two prefixes share a common prefix,
// which for namespace tuples means one is a prefix of the other. An empty
// prefix matches everything, so it overlaps with all of them.
func overlappingPrefixes(a, b []string) bool {
	shorter, longer := a, b
	if len(b) < len(a) {
		shorter, longer = b, a
	}
	return slices.Equal(shorter, longer[:len(shorter)])
}

// namespaceSuffix returns the part of namespace after prefix, and whether
// namespace is under it at all.
func namespaceSuffix(prefix, namespace []string) ([]string, bool) {
	if len(namespace) < len(prefix) || !slices.Equal(prefix, namespace[:len(prefix)]) {
		return nil, false
	}
	return namespace[len(prefix):], true
}

func namespaceStrings(fields [][]byte) []string {
	namespace := make([]string, len(fields))
	for i, f := range fields {
		namespace[i] = string(f)
	}
	return namespace
}

var (
	errNamespaceWithdrawn          = errors.New("namespace announcement withdrawn")
	errNamespaceSubscriptionClosed = errors.New("namespace subscription closed")
	errNamespaceOutsidePrefix      = errors.New("namespace is not under the subscription's prefix")

	errNamespaceDoneBeforeNamespace = ProtocolError{
		code:    SessionErrorProtocolViolation,
		message: "NAMESPACE_DONE for a namespace that was never announced",
	}
)
