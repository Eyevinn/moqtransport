package moqtransport

import (
	"fmt"

	"github.com/Eyevinn/moqtransport/internal/wire2"
)

// The draft-18 error registries.
//
// draft-18 replaced the per-message error tables -- SUBSCRIBE_ERROR,
// FETCH_ERROR, ANNOUNCE_ERROR and the rest -- with one registry shared by
// every REQUEST_ERROR, and kept a second, separate one for ending a
// subscription. The two overlap in name but not in value: GOING_AWAY is 0x6 as
// a request error and 0x4 as a publish-done code, which is exactly why they
// are separate Go types rather than one set of constants.
//
// A third registry, the stream reset codes of Section 3.3.3, is
// [StreamErrorCode] in request_stream.go. Together they replace the
// ErrorCodeSubscribe / ErrorCodeFetch / ErrorCodeAnnounce families in
// errors.go, which go with the draft-14/16 session layer.

// RequestErrorCode is the reason a request was rejected, carried in
// REQUEST_ERROR (draft-ietf-moq-transport-18, Section 15.10.2).
type RequestErrorCode uint64

const (
	// RequestErrorInternal is an implementation specific error.
	RequestErrorInternal RequestErrorCode = 0x0
	// RequestErrorUnauthorized means the requester is not authorized.
	RequestErrorUnauthorized RequestErrorCode = 0x1
	// RequestErrorTimeout means the request took too long to satisfy.
	RequestErrorTimeout RequestErrorCode = 0x2
	// RequestErrorNotSupported means the endpoint does not implement this
	// request. It is what a request with no handler is answered with, which is
	// both spec-correct and more useful than silence.
	RequestErrorNotSupported RequestErrorCode = 0x3
	// RequestErrorMalformedAuthToken means the authorization token could not
	// be parsed.
	RequestErrorMalformedAuthToken RequestErrorCode = 0x4
	// RequestErrorExpiredAuthToken means the authorization token has expired.
	RequestErrorExpiredAuthToken RequestErrorCode = 0x5
	// RequestErrorGoingAway means the endpoint has sent or received a GOAWAY.
	RequestErrorGoingAway RequestErrorCode = 0x6
	// RequestErrorExcessiveLoad means the endpoint is overloaded.
	RequestErrorExcessiveLoad RequestErrorCode = 0x9
	// RequestErrorDoesNotExist means the track or namespace is unknown.
	RequestErrorDoesNotExist RequestErrorCode = 0x10
	// RequestErrorInvalidRange means the requested range cannot be satisfied.
	RequestErrorInvalidRange RequestErrorCode = 0x11
	// RequestErrorMalformedTrack means the track violates the object model.
	RequestErrorMalformedTrack RequestErrorCode = 0x12
	// RequestErrorDuplicateSubscription means an equivalent subscription is
	// already established.
	RequestErrorDuplicateSubscription RequestErrorCode = 0x19
	// RequestErrorUninterested means the receiver does not want what was
	// offered, in response to a PUBLISH or PUBLISH_NAMESPACE.
	RequestErrorUninterested RequestErrorCode = 0x20
	// RequestErrorPrefixOverlap means a namespace prefix overlaps one already
	// subscribed to.
	RequestErrorPrefixOverlap RequestErrorCode = 0x30
	// RequestErrorNamespaceTooLarge means the namespace tuple exceeds what the
	// endpoint accepts.
	RequestErrorNamespaceTooLarge RequestErrorCode = 0x31
	// RequestErrorInvalidJoiningRequestID means a Joining FETCH named a
	// Request ID that is not a subscription it can join.
	RequestErrorInvalidJoiningRequestID RequestErrorCode = 0x32
	// RequestErrorUnsupportedExtension means the track carries a Mandatory
	// Track Property the endpoint does not understand, so it cannot serve or
	// forward the track at all.
	RequestErrorUnsupportedExtension RequestErrorCode = 0x33
	// RequestErrorRedirect means the request should be retried elsewhere; the
	// REQUEST_ERROR carries the destination.
	RequestErrorRedirect RequestErrorCode = 0x34
)

func (c RequestErrorCode) String() string {
	switch c {
	case RequestErrorInternal:
		return "INTERNAL_ERROR"
	case RequestErrorUnauthorized:
		return "UNAUTHORIZED"
	case RequestErrorTimeout:
		return "TIMEOUT"
	case RequestErrorNotSupported:
		return "NOT_SUPPORTED"
	case RequestErrorMalformedAuthToken:
		return "MALFORMED_AUTH_TOKEN"
	case RequestErrorExpiredAuthToken:
		return "EXPIRED_AUTH_TOKEN"
	case RequestErrorGoingAway:
		return "GOING_AWAY"
	case RequestErrorExcessiveLoad:
		return "EXCESSIVE_LOAD"
	case RequestErrorDoesNotExist:
		return "DOES_NOT_EXIST"
	case RequestErrorInvalidRange:
		return "INVALID_RANGE"
	case RequestErrorMalformedTrack:
		return "MALFORMED_TRACK"
	case RequestErrorDuplicateSubscription:
		return "DUPLICATE_SUBSCRIPTION"
	case RequestErrorUninterested:
		return "UNINTERESTED"
	case RequestErrorPrefixOverlap:
		return "PREFIX_OVERLAP"
	case RequestErrorNamespaceTooLarge:
		return "NAMESPACE_TOO_LARGE"
	case RequestErrorInvalidJoiningRequestID:
		return "INVALID_JOINING_REQUEST_ID"
	case RequestErrorUnsupportedExtension:
		return "UNSUPPORTED_EXTENSION"
	case RequestErrorRedirect:
		return "REDIRECT"
	}
	return unregisteredCode("request error", uint64(c))
}

// PublishDoneCode is why a subscription ended, carried in PUBLISH_DONE
// (Section 15.10.3). It is the graceful counterpart to resetting the request
// stream, and the draft asks publishers to prefer it.
type PublishDoneCode uint64

const (
	// PublishDoneInternalError is an implementation specific error.
	PublishDoneInternalError PublishDoneCode = 0x0
	// PublishDoneUnauthorized means the subscriber lost authorization.
	PublishDoneUnauthorized PublishDoneCode = 0x1
	// PublishDoneTrackEnded means the track itself has ended, so no future
	// subscription to it would deliver anything either.
	PublishDoneTrackEnded PublishDoneCode = 0x2
	// PublishDoneSubscriptionEnded means this subscription reached its end,
	// while the track continues.
	PublishDoneSubscriptionEnded PublishDoneCode = 0x3
	// PublishDoneGoingAway means the publisher is shutting down or migrating.
	PublishDoneGoingAway PublishDoneCode = 0x4
	// PublishDoneTooFarBehind means the subscriber could not keep up and the
	// publisher gave up on it.
	PublishDoneTooFarBehind PublishDoneCode = 0x5
	// PublishDoneExpired means the subscription's lifetime elapsed.
	PublishDoneExpired PublishDoneCode = 0x6
	// PublishDoneUpdateFailed means a REQUEST_UPDATE could not be applied.
	PublishDoneUpdateFailed PublishDoneCode = 0x8
	// PublishDoneExcessiveLoad means the publisher is overloaded.
	PublishDoneExcessiveLoad PublishDoneCode = 0x9
	// PublishDoneMalformedTrack means the track violates the object model.
	PublishDoneMalformedTrack PublishDoneCode = 0x12
)

func (c PublishDoneCode) String() string {
	switch c {
	case PublishDoneInternalError:
		return "INTERNAL_ERROR"
	case PublishDoneUnauthorized:
		return "UNAUTHORIZED"
	case PublishDoneTrackEnded:
		return "TRACK_ENDED"
	case PublishDoneSubscriptionEnded:
		return "SUBSCRIPTION_ENDED"
	case PublishDoneGoingAway:
		return "GOING_AWAY"
	case PublishDoneTooFarBehind:
		return "TOO_FAR_BEHIND"
	case PublishDoneExpired:
		return "EXPIRED"
	case PublishDoneUpdateFailed:
		return "UPDATE_FAILED"
	case PublishDoneExcessiveLoad:
		return "EXCESSIVE_LOAD"
	case PublishDoneMalformedTrack:
		return "MALFORMED_TRACK"
	}
	return unregisteredCode("publish done", uint64(c))
}

// unregisteredCode names a code that is not in its registry. A grease code is
// called out separately because it is deliberate rather than a bug: Section 14
// reserves 0x7f*N + 0x9D in every registry so that peers exercise their
// unknown-value paths, and receiving one means the peer is doing its job.
func unregisteredCode(registry string, code uint64) string {
	if wire2.IsGreaseCode(code) {
		return fmt.Sprintf("grease %s code: %#x", registry, code)
	}
	return fmt.Sprintf("unknown %s code: %#x", registry, code)
}
