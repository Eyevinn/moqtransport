package wire2

import (
	"fmt"
	"io"

	"github.com/Eyevinn/locmaf/vi64"
)

// ObjectStatus says whether an Object is a normal Object or marks the end of a
// Group or Track (draft-ietf-moq-transport-18, Section 11.2.1.1).
//
// It exists only on Objects delivered through a subscription. A FETCH response
// has no status field at all: it marks absent Objects with an End of Range
// indicator in the Serialization Flags instead.
type ObjectStatus uint64

const (
	// ObjectStatusNormal is implicit for any Object with a non-zero payload
	// length; a zero-length Object encodes it explicitly.
	ObjectStatusNormal ObjectStatus = 0x0
	// ObjectStatusEndOfGroup says no Object in this Group with an Object ID at
	// or above this one exists.
	ObjectStatusEndOfGroup ObjectStatus = 0x3
	// ObjectStatusEndOfTrack says no Object at or after this Location exists.
	ObjectStatusEndOfTrack ObjectStatus = 0x4
)

func (s ObjectStatus) String() string {
	switch s {
	case ObjectStatusNormal:
		return "NORMAL"
	case ObjectStatusEndOfGroup:
		return "END_OF_GROUP"
	case ObjectStatusEndOfTrack:
		return "END_OF_TRACK"
	}
	return fmt.Sprintf("invalid object status: %#x", uint64(s))
}

// valid reports whether s is one of the three defined statuses. The draft
// leaves no room for extension here: any other value closes the session with a
// PROTOCOL_VIOLATION.
func (s ObjectStatus) valid() bool {
	switch s {
	case ObjectStatusNormal, ObjectStatusEndOfGroup, ObjectStatusEndOfTrack:
		return true
	}
	return false
}

// byteReader is what every data-stream parser needs: vi64 and the single
// Publisher Priority byte read one byte at a time, payloads read in bulk.
// *bufio.Reader is the implementation the session layer passes in.
type byteReader interface {
	io.Reader
	io.ByteReader
}

// readProperties reads an Object Properties block: a byte length followed by
// that many bytes of Key-Value-Pairs (Section 11.2.1.2).
//
// Unknown Property types are not an error. Properties are relay-visible and
// forwarded unchanged, so a parser that rejected what it did not recognise
// would break the forwarding model; only scope violations are rejected.
func readProperties(r byteReader) (KVPList, error) {
	length, err := vi64.Read(r)
	if err != nil {
		return nil, unexpectedEOF(err)
	}
	if length == 0 {
		return KVPList{}, nil
	}
	body := make([]byte, length)
	if _, err := io.ReadFull(r, body); err != nil {
		return nil, unexpectedEOF(err)
	}
	var pp KVPList
	if _, err := pp.parseAll(body); err != nil {
		return nil, err
	}
	if err := ValidateObjectProperties(pp); err != nil {
		return nil, err
	}
	return pp, nil
}

// readPayload reads length bytes of Object payload.
func readPayload(r io.Reader, length uint64) ([]byte, error) {
	payload := make([]byte, length)
	if _, err := io.ReadFull(r, payload); err != nil {
		return nil, unexpectedEOF(err)
	}
	return payload, nil
}

// addObjectID computes prev + delta + 1, the rule every delta-encoded Object
// ID follows once there is a previous Object. Overflowing 2^64-1 is a
// PROTOCOL_VIOLATION rather than a wrap.
func addObjectID(prev, delta uint64) (uint64, error) {
	if prev == maxUint64 || delta > maxUint64-prev-1 {
		return 0, errObjectIDOverflow
	}
	return prev + delta + 1, nil
}
