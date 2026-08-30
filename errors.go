package moqtransport

import "fmt"

// SessionErrorCode is the code an endpoint closes the whole session with
// (draft-ietf-moq-transport-18, Section 15.10.1).
//
// It is the most serious of the four registries: where a REQUEST_ERROR ends
// one request and a stream reset ends one stream, these end the connection.
// Everything the draft calls a PROTOCOL_VIOLATION arrives here.
type SessionErrorCode uint64

const (
	// SessionErrorNoError closes the session without an error.
	SessionErrorNoError SessionErrorCode = 0x0
	// SessionErrorInternal is an implementation specific error.
	SessionErrorInternal SessionErrorCode = 0x1
	// SessionErrorUnauthorized means the peer is not authorized.
	SessionErrorUnauthorized SessionErrorCode = 0x2
	// SessionErrorProtocolViolation means the peer sent something the draft
	// does not allow. Most parse failures end here: draft-18 leaves very
	// little that an endpoint is permitted to skip.
	SessionErrorProtocolViolation SessionErrorCode = 0x3
	// SessionErrorInvalidRequestID means a Request ID had the wrong parity for
	// its sender, or repeated one already used.
	SessionErrorInvalidRequestID SessionErrorCode = 0x4
	// SessionErrorDuplicateTrackAlias means one Track Alias was used for two
	// different tracks at once.
	SessionErrorDuplicateTrackAlias SessionErrorCode = 0x5
	// SessionErrorKeyValueFormattingError means a Key-Value-Pair could not be
	// decoded.
	SessionErrorKeyValueFormattingError SessionErrorCode = 0x6
	// SessionErrorInvalidPath means the PATH Setup Option was used where it is
	// not allowed, such as over WebTransport or from a server.
	SessionErrorInvalidPath SessionErrorCode = 0x8
	// SessionErrorMalformedPath means the PATH Setup Option could not be
	// parsed.
	SessionErrorMalformedPath SessionErrorCode = 0x9
	// SessionErrorGoAwayTimeout means the peer did not close after GOAWAY.
	SessionErrorGoAwayTimeout SessionErrorCode = 0x10
	// SessionErrorControlMessageTimeout means a control message took too long.
	SessionErrorControlMessageTimeout SessionErrorCode = 0x11
	// SessionErrorDataStreamTimeout means a data stream took too long.
	SessionErrorDataStreamTimeout SessionErrorCode = 0x12
	// SessionErrorAuthTokenCacheOverflow means the peer exceeded the auth
	// token cache size it was given.
	SessionErrorAuthTokenCacheOverflow SessionErrorCode = 0x13
	// SessionErrorDuplicateAuthTokenAlias means an auth token alias was reused
	// while still registered.
	SessionErrorDuplicateAuthTokenAlias SessionErrorCode = 0x14
	// SessionErrorVersionNegotiationFailed means no common version was found.
	// From draft-17 that is settled by ALPN before any MOQT byte is written,
	// so reaching this means the transport handed us a version we do not
	// implement.
	SessionErrorVersionNegotiationFailed SessionErrorCode = 0x15
	// SessionErrorMalformedAuthToken means an auth token could not be parsed.
	SessionErrorMalformedAuthToken SessionErrorCode = 0x16
	// SessionErrorUnknownAuthTokenAlias means a token alias was used before it
	// was registered.
	SessionErrorUnknownAuthTokenAlias SessionErrorCode = 0x17
	// SessionErrorExpiredAuthToken means an auth token had expired.
	SessionErrorExpiredAuthToken SessionErrorCode = 0x18
	// SessionErrorInvalidAuthority means the AUTHORITY Setup Option was used
	// where it is not allowed.
	SessionErrorInvalidAuthority SessionErrorCode = 0x19
	// SessionErrorMalformedAuthority means the AUTHORITY Setup Option could
	// not be parsed.
	SessionErrorMalformedAuthority SessionErrorCode = 0x1A
)

func (c SessionErrorCode) String() string {
	switch c {
	case SessionErrorNoError:
		return "NO_ERROR"
	case SessionErrorInternal:
		return "INTERNAL_ERROR"
	case SessionErrorUnauthorized:
		return "UNAUTHORIZED"
	case SessionErrorProtocolViolation:
		return "PROTOCOL_VIOLATION"
	case SessionErrorInvalidRequestID:
		return "INVALID_REQUEST_ID"
	case SessionErrorDuplicateTrackAlias:
		return "DUPLICATE_TRACK_ALIAS"
	case SessionErrorKeyValueFormattingError:
		return "KEY_VALUE_FORMATTING_ERROR"
	case SessionErrorInvalidPath:
		return "INVALID_PATH"
	case SessionErrorMalformedPath:
		return "MALFORMED_PATH"
	case SessionErrorGoAwayTimeout:
		return "GOAWAY_TIMEOUT"
	case SessionErrorControlMessageTimeout:
		return "CONTROL_MESSAGE_TIMEOUT"
	case SessionErrorDataStreamTimeout:
		return "DATA_STREAM_TIMEOUT"
	case SessionErrorAuthTokenCacheOverflow:
		return "AUTH_TOKEN_CACHE_OVERFLOW"
	case SessionErrorDuplicateAuthTokenAlias:
		return "DUPLICATE_AUTH_TOKEN_ALIAS"
	case SessionErrorVersionNegotiationFailed:
		return "VERSION_NEGOTIATION_FAILED"
	case SessionErrorMalformedAuthToken:
		return "MALFORMED_AUTH_TOKEN"
	case SessionErrorUnknownAuthTokenAlias:
		return "UNKNOWN_AUTH_TOKEN_ALIAS"
	case SessionErrorExpiredAuthToken:
		return "EXPIRED_AUTH_TOKEN"
	case SessionErrorInvalidAuthority:
		return "INVALID_AUTHORITY"
	case SessionErrorMalformedAuthority:
		return "MALFORMED_AUTHORITY"
	}
	return unregisteredCode("session error", uint64(c))
}

// ProtocolError is a violation that closes the session, carrying the code to
// close it with.
type ProtocolError struct {
	code    SessionErrorCode
	message string
}

func (e ProtocolError) Error() string {
	return fmt.Sprintf("%v: %v", e.code, e.message)
}

func (e ProtocolError) String() string {
	return e.Error()
}

// Code returns the session error code to close with.
func (e ProtocolError) Code() SessionErrorCode {
	return e.code
}
