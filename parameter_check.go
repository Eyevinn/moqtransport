package moqtransport

import (
	"fmt"
	"sync"

	"github.com/Eyevinn/moqtransport/internal/wire2"
)

// parameterScope follows one request stream's exchange so that every message
// on it, sent or received, can be checked against the parameters its draft
// allows there (draft-18 Section 10.2.1, draft-22 Section 9.20.1).
//
// The message alone does not say. A REQUEST_OK answers the request on some
// streams and a REQUEST_UPDATE on others, and a REQUEST_UPDATE may carry
// different parameters for a subscription than for a SUBSCRIBE_NAMESPACE. So
// the scope remembers what opened the stream, which side sent it, and whether
// the request has been answered yet.
type parameterScope struct {
	mu sync.Mutex
	// kind is the type of the message that opened the stream, 0 until there
	// is one.
	kind wire2.ControlMessageType
	// requester is whether this endpoint sent it.
	requester bool
	// answered is whether the other side has sent the request's answer.
	// Everything that side sends after it -- in particular every further
	// REQUEST_OK -- answers a REQUEST_UPDATE instead.
	answered bool
}

// opened records what opened the stream, if nothing has yet. A stream's first
// message does it on its own; a request built from a message read elsewhere
// says so explicitly.
func (p *parameterScope) opened(kind wire2.ControlMessageType, requester bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.kind == 0 {
		p.kind, p.requester = kind, requester
	}
}

// check checks msg, which this endpoint is sending or has received, and moves
// the exchange on. Messages must be passed in the order the stream carries
// them in each direction.
func (p *parameterScope) check(version wire2.Version, msg wire2.ControlMessage, sending bool) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.kind == 0 {
		p.kind, p.requester = msg.Type(), sending
	}
	fromRequester := sending == p.requester

	params, ctx, ok := p.contextOf(msg, fromRequester)
	if !fromRequester && isAnswer(msg) {
		p.answered = true
	}
	if !ok {
		// A message with no parameters, or one whose place on this stream
		// is not a question of parameters: the request handlers reject a
		// message that does not belong on the stream at all.
		return nil
	}
	return wire2.CheckParameters(version, ctx, params)
}

// contextOf is where msg's parameters sit, given what opened the stream.
func (p *parameterScope) contextOf(msg wire2.ControlMessage, fromRequester bool) (wire2.Parameters, wire2.ParameterContext, bool) {
	switch m := msg.(type) {
	case *wire2.Subscribe:
		return m.Parameters, wire2.ContextSubscribe, true
	case *wire2.Publish:
		return m.Parameters, wire2.ContextPublish, true
	case *wire2.Fetch:
		return m.Parameters, wire2.ContextFetch, true
	case *wire2.TrackStatus:
		return m.Parameters, wire2.ContextTrackStatus, true
	case *wire2.PublishNamespace:
		return m.Parameters, wire2.ContextPublishNamespace, true
	case *wire2.SubscribeNamespace:
		return m.Parameters, wire2.ContextSubscribeNamespace, true
	case *wire2.SubscribeTracks:
		return m.Parameters, wire2.ContextSubscribeTracks, true
	case *wire2.SubscribeOk:
		return m.Parameters, wire2.ContextSubscribeOk, true
	case *wire2.FetchOk:
		return m.Parameters, wire2.ContextFetchOk, true
	case *wire2.PublishStateNotify:
		return m.Parameters, wire2.ContextPublishStateNotify, true
	case *wire2.RequestUpdate:
		ctx, ok := updateContext(p.kind)
		return m.Parameters, ctx, ok
	case *wire2.RequestOk:
		return m.Parameters, p.okContext(fromRequester), true
	case *wire2.PublishOk:
		return m.Parameters, p.okContext(fromRequester), true
	}
	return nil, 0, false
}

// okContext says what a REQUEST_OK answers. The first one from the side that
// was asked answers the request, if that request is answered with one at all;
// every other answers a REQUEST_UPDATE. A REQUEST_OK from the requester can
// only answer an update too: the subscriber's, on a subscription the
// publisher began with PUBLISH.
func (p *parameterScope) okContext(fromRequester bool) wire2.ParameterContext {
	if fromRequester || p.answered {
		return wire2.ContextRequestUpdateOk
	}
	switch p.kind {
	case wire2.ControlMessageTypePublish:
		return wire2.ContextPublishOk
	case wire2.ControlMessageTypeTrackStatus:
		return wire2.ContextTrackStatusOk
	case wire2.ControlMessageTypePublishNamespace:
		return wire2.ContextPublishNamespaceOk
	case wire2.ControlMessageTypeSubscribeNamespace:
		return wire2.ContextSubscribeNamespaceOk
	case wire2.ControlMessageTypeSubscribeTracks:
		return wire2.ContextSubscribeTracksOk
	}
	// A SUBSCRIBE is answered with SUBSCRIBE_OK and a FETCH with FETCH_OK,
	// so a REQUEST_OK on either answers an update.
	return wire2.ContextRequestUpdateOk
}

// updateContext is what a REQUEST_UPDATE on a stream opened by kind may
// carry. TRACK_STATUS cannot be updated at all, which its handler enforces.
func updateContext(kind wire2.ControlMessageType) (wire2.ParameterContext, bool) {
	switch kind {
	case wire2.ControlMessageTypeSubscribe, wire2.ControlMessageTypePublish:
		return wire2.ContextUpdateSubscription, true
	case wire2.ControlMessageTypeFetch:
		return wire2.ContextUpdateFetch, true
	case wire2.ControlMessageTypePublishNamespace:
		return wire2.ContextUpdatePublishNamespace, true
	case wire2.ControlMessageTypeSubscribeNamespace:
		return wire2.ContextUpdateSubscribeNamespace, true
	case wire2.ControlMessageTypeSubscribeTracks:
		return wire2.ContextUpdateSubscribeTracks, true
	}
	return 0, false
}

// isAnswer reports whether msg answers a request.
func isAnswer(msg wire2.ControlMessage) bool {
	switch msg.(type) {
	case *wire2.SubscribeOk, *wire2.FetchOk, *wire2.RequestOk, *wire2.PublishOk, *wire2.RequestError:
		return true
	}
	return false
}

// checkRequest checks the parameters of a request this endpoint is about to
// send, before a stream is opened for it. Refusing it after the stream exists
// would hand the peer a stream opened and reset for nothing.
func checkRequest(version wire2.Version, msg wire2.ControlMessage) error {
	var scope parameterScope
	if err := scope.check(version, msg, true); err != nil {
		return errParameterNotAllowed(msg, err)
	}
	return nil
}

// parameterViolation is a received parameter outside the messages its
// definition lists, which closes the session.
func parameterViolation(err error) ProtocolError {
	return ProtocolError{code: SessionErrorProtocolViolation, message: err.Error()}
}

// errParameterNotAllowed wraps a parameter this endpoint was about to send
// where its draft does not allow it. Sending it would close the session, so
// the write fails instead.
func errParameterNotAllowed(msg wire2.ControlMessage, err error) error {
	return fmt.Errorf("not sending %v: %w", msg.Type(), err)
}
