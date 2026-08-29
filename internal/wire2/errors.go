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

	// errReservedSubgroupIDMode means a SUBGROUP_HEADER stream type used
	// SUBGROUP_ID_MODE 0b11, which Section 11.4.2 reserves for future use.
	errReservedSubgroupIDMode = errors.New("subgroup header uses the reserved subgroup ID mode")

	// errInvalidObjectStatus means an Object Status was not one of the three
	// values Section 11.2.1.1 defines.
	errInvalidObjectStatus = errors.New("invalid object status")

	// errPropertiesOnNonNormalObject means an Object with a status other than
	// Normal carried Properties, which Section 11.2.1.2 forbids.
	errPropertiesOnNonNormalObject = errors.New("properties on an object whose status is not Normal")

	// errObjectIDOverflow means a delta-encoded Object ID would exceed 2^64-1.
	errObjectIDOverflow = errors.New("object ID delta overflows")

	// errObjectIDNotIncreasing means an Object was written with an ID at or
	// below the previous Object's on the same stream. The delta encoding has
	// no way to express it.
	errObjectIDNotIncreasing = errors.New("object IDs on a stream must increase")

	// errStatusWithPayload means an Object carried both a payload and a status
	// other than Normal. Section 11.2.1.1 requires a non-zero status to have
	// an empty payload, and the wire format has nowhere to put the status of
	// an Object that has one.
	errStatusWithPayload = errors.New("object with a payload cannot carry a status")

	// errPropertiesNotDeclared means an Object carried Properties on a stream
	// whose header did not set the PROPERTIES bit. The bit covers the whole
	// stream, so this cannot be encoded.
	errPropertiesNotDeclared = errors.New("properties on a stream whose header did not declare them")

	// errDatagramStatusEndOfGroup means a datagram set both the STATUS and
	// END_OF_GROUP bits. An Object status message cannot signal end of group,
	// which rules out eight of the datagram types outright (Section 11.3.1).
	errDatagramStatusEndOfGroup = errors.New("datagram sets both the status and end-of-group bits")

	// errEmptyDatagramProperties means a datagram set the PROPERTIES bit and
	// then gave a Properties Length of 0.
	errEmptyDatagramProperties = errors.New("datagram declares properties but carries none")

	// errUnexpectedEndOfDatagram means a datagram ended part-way through a
	// field. A datagram is a single message with no continuation.
	errUnexpectedEndOfDatagram = errors.New("datagram ended mid-field")

	// errInvalidSerializationFlags means a FETCH Object's Serialization Flags
	// were 128 or more and not one of the End of Range values in Table 7.
	errInvalidSerializationFlags = errors.New("invalid fetch object serialization flags")

	// errFetchObjectNeedsPrior means a FETCH Object used a flag referring to
	// the prior Object when there is no prior Object to refer to. The first
	// Object on a stream MUST carry both deltas, and Subgroup ID and Priority
	// cannot be inherited across an End of Range indicator that follows none.
	errFetchObjectNeedsPrior = errors.New("fetch object refers to a prior object that does not exist")

	// errEndOfRangeWithPayload means an End of Range indicator declared a
	// payload. It stands in for Objects that were not serialized, so it has
	// none.
	errEndOfRangeWithPayload = errors.New("end of range indicator carries a payload")

	// errGroupIDOverflow means a Group ID Delta moved the Group ID outside
	// [0, 2^64-1] in the Group Order's direction.
	errGroupIDOverflow = errors.New("group ID delta overflows")

	// errSubgroupIDOverflow means "prior Subgroup ID plus one" exceeded
	// 2^64-1.
	errSubgroupIDOverflow = errors.New("subgroup ID plus one overflows")

	// errGroupIDNotAscending and errGroupIDNotDescending mean a FETCH response
	// was written with Groups out of the order it declared. The Group ID Delta
	// only moves one way, so the encoding cannot express it.
	errGroupIDNotAscending  = errors.New("group IDs must ascend in an ascending fetch response")
	errGroupIDNotDescending = errors.New("group IDs must descend in a descending fetch response")
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
