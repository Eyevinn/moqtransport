package wire2

import "errors"

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
)
