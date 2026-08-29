package wire2

import (
	"errors"
	"fmt"
	"math"
)

// maxUint64 is the largest Key-Value-Pair or Message Parameter Type; a delta
// that would carry the running Type past it is a PROTOCOL_VIOLATION.
const maxUint64 = uint64(math.MaxUint64)

// Parse errors. Each one is a MUST-close condition in draft-18; the session
// layer maps them to a session error code when it closes.
var (
	// errTrailingBytes means the body did not consume the whole declared
	// Message Length. draft-18 Section 10 makes that a PROTOCOL_VIOLATION.
	errTrailingBytes = errors.New("message body shorter than the declared length")

	// errFieldTooLong means a length-prefixed field exceeded the maximum its
	// definition allows, e.g. a Reason Phrase over 1024 bytes.
	errFieldTooLong = errors.New("length-prefixed field exceeds its maximum")

	// errTooManyFields means a counted sequence exceeded its maximum, e.g. a
	// Track Namespace with more than 32 fields.
	errTooManyFields = errors.New("counted field sequence exceeds its maximum")

	// errInvalidBoolValue means a single-byte flag was neither 0 nor 1.
	errInvalidBoolValue = errors.New("invalid bool flag value")

	// errDeltaTypeOverflow means a Key-Value-Pair Delta Type would push the
	// running Type past 2^64-1 (draft-18 Section 1.4.3).
	errDeltaTypeOverflow = errors.New("key-value-pair delta type overflows")

	// errValueTooLong means a Key-Value-Pair value exceeded 2^16-1 bytes.
	errValueTooLong = errors.New("key-value-pair value exceeds 65535 bytes")

	// errInvalidFetchType means a FETCH named a Fetch Type outside the three
	// draft-18 defines.
	errInvalidFetchType = errors.New("invalid fetch type")

	// errControlMessageTooLong means a message body would not fit the 16-bit
	// Message Length field, so it cannot be framed at all.
	errControlMessageTooLong = errors.New("control message body exceeds 65535 bytes")
)

// unknownControlMessageTypeError is returned for a message type that has no
// meaning in the scope it arrived in. draft-18 Section 10 requires the session
// to be closed: control messages are not intended to be ignored.
type unknownControlMessageTypeError struct {
	scope       StreamScope
	messageType ControlMessageType
}

func (e unknownControlMessageTypeError) Error() string {
	return fmt.Sprintf("unknown control message type %#x on a %v stream", uint64(e.messageType), e.scope)
}

// unknownParameterError is returned for a Message Parameter type the registry
// does not know. draft-18 Section 10.2 makes it a PROTOCOL_VIOLATION, and the
// parser has no choice either way: without the registry entry it cannot tell
// how long the value is, so it cannot reach the next parameter.
type unknownParameterError struct {
	parameterType uint64
}

func (e unknownParameterError) Error() string {
	return fmt.Sprintf("unknown message parameter type %#x", e.parameterType)
}

// duplicateParameterError is returned for a repeated Message Parameter type
// whose definition does not allow repeats.
type duplicateParameterError struct {
	parameterType uint64
}

func (e duplicateParameterError) Error() string {
	return fmt.Sprintf("duplicate message parameter %s", ParameterName(e.parameterType))
}

// propertyScopeError is returned for a Property used outside the scope its
// definition allows.
type propertyScopeError struct {
	propertyType uint64
	scope        PropertyScope
}

func (e propertyScopeError) Error() string {
	where := "track"
	if e.scope == PropertyScopeObject {
		where = "object"
	}
	return fmt.Sprintf("property %s is not allowed in %s scope", PropertyName(e.propertyType), where)
}

// setupOptionError is returned for a Setup Option used where the draft forbids
// it. Unlike an unknown option, which MUST be ignored, these close the session.
type setupOptionError struct {
	option uint64
	reason string
}

func (e setupOptionError) Error() string {
	return fmt.Sprintf("setup option %s %s", SetupOptionName(e.option), e.reason)
}

// unknownStreamTypeError is returned for a unidirectional stream whose leading
// varint is not in Table 3. The session MUST be closed.
type unknownStreamTypeError struct {
	streamType StreamType
}

func (e unknownStreamTypeError) Error() string {
	return fmt.Sprintf("unknown stream type %#x", uint64(e.streamType))
}
