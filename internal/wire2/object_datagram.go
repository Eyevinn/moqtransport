package wire2

import (
	"fmt"

	"github.com/Eyevinn/locmaf/vi64"
)

// An OBJECT_DATAGRAM type is a bitfield of the form 0b00X0XXXX
// (draft-ietf-moq-transport-18, Section 11.3.1): the values 0x00-0x0F and
// 0x20-0x2F. Bits 4 and 6 and everything above must be clear, which is what
// keeps datagram types disjoint from the SUBGROUP_HEADER stream types even
// though the two bitfields overlap numerically.
const (
	datagramFlagProperties      = 0x01
	datagramFlagEndOfGroup      = 0x02
	datagramFlagZeroObjectID    = 0x04
	datagramFlagDefaultPriority = 0x08
	datagramFlagStatus          = 0x20

	datagramTypeMask  = 0b1101_0000
	datagramTypeValue = 0b0000_0000
)

// ObjectDatagram is a single Object carried in a QUIC datagram
// (Section 11.3.1). Its Object Forwarding Preference is Datagram, so it has no
// Subgroup ID.
type ObjectDatagram struct {
	TrackAlias uint64
	GroupID    uint64

	// ObjectID is omitted from the wire when it is 0, which the ZERO_OBJECT_ID
	// bit signals; the field here always holds the real value.
	ObjectID uint64

	// Priority is omitted from the wire when DefaultPriority is set, and the
	// Object then inherits the priority from the control message that
	// established the subscription.
	Priority        uint8
	DefaultPriority bool

	Properties KVPList

	// EndOfGroup says no Object in this Group with a larger Object ID exists.
	// It is a bit rather than a status, so unlike ObjectStatusEndOfGroup it
	// describes an Object that carries a payload of its own.
	EndOfGroup bool

	// Status and Payload are mutually exclusive: the STATUS bit selects one or
	// the other, and a datagram carrying a status has no payload length field
	// because it has no payload.
	Status  ObjectStatus
	Payload []byte
}

// datagramType returns the type byte this datagram encodes to.
func (d *ObjectDatagram) datagramType() uint64 {
	var t uint64
	if len(d.Properties) > 0 {
		t |= datagramFlagProperties
	}
	if d.EndOfGroup {
		t |= datagramFlagEndOfGroup
	}
	if d.ObjectID == 0 {
		t |= datagramFlagZeroObjectID
	}
	if d.DefaultPriority {
		t |= datagramFlagDefaultPriority
	}
	if d.hasStatus() {
		t |= datagramFlagStatus
	}
	return t
}

// hasStatus reports whether the datagram carries a status instead of a
// payload. A Normal Object with an empty payload is still a payload datagram:
// the payload runs to the end of the datagram, so an empty one encodes
// naturally and needs no status.
func (d *ObjectDatagram) hasStatus() bool {
	return d.Status != ObjectStatusNormal
}

func (d *ObjectDatagram) String() string {
	return fmt.Sprintf("OBJECT_DATAGRAM{alias=%d group=%d object=%d priority=%d len=%d}",
		d.TrackAlias, d.GroupID, d.ObjectID, d.Priority, len(d.Payload))
}

// AppendObjectDatagram writes d onto buf, type byte included.
func AppendObjectDatagram(buf []byte, d *ObjectDatagram) ([]byte, error) {
	if d.hasStatus() {
		if !d.Status.valid() {
			return nil, errInvalidObjectStatus
		}
		if len(d.Payload) > 0 {
			return nil, errStatusWithPayload
		}
		// The STATUS and END_OF_GROUP bits are mutually exclusive: an Object
		// status message cannot also signal end of group.
		if d.EndOfGroup {
			return nil, errDatagramStatusEndOfGroup
		}
		// Only Normal Objects may carry Properties, and a status datagram is
		// never Normal.
		if len(d.Properties) > 0 {
			return nil, errPropertiesOnNonNormalObject
		}
	}

	buf = vi64.Append(buf, d.datagramType())
	buf = vi64.Append(buf, d.TrackAlias)
	buf = vi64.Append(buf, d.GroupID)
	if d.ObjectID != 0 {
		buf = vi64.Append(buf, d.ObjectID)
	}
	if !d.DefaultPriority {
		buf = append(buf, d.Priority)
	}
	if len(d.Properties) > 0 {
		buf = d.Properties.appendLength(buf)
	}
	if d.hasStatus() {
		buf = vi64.Append(buf, uint64(d.Status))
		return buf, nil
	}
	return append(buf, d.Payload...), nil
}

// ParseObjectDatagram reads a whole datagram. A datagram is a single message
// with no framing of its own, so data must be exactly one datagram: the
// payload is whatever follows the header, with no length field to bound it.
func ParseObjectDatagram(data []byte) (*ObjectDatagram, error) {
	t, n, err := vi64.Parse(data)
	if err != nil {
		return nil, err
	}
	data = data[n:]
	if t&datagramTypeMask != datagramTypeValue || t > 0x2F {
		return nil, unknownDatagramTypeError{datagramType: t}
	}
	hasStatus := t&datagramFlagStatus != 0
	endOfGroup := t&datagramFlagEndOfGroup != 0
	if hasStatus && endOfGroup {
		return nil, errDatagramStatusEndOfGroup
	}

	d := &ObjectDatagram{
		EndOfGroup:      endOfGroup,
		DefaultPriority: t&datagramFlagDefaultPriority != 0,
	}
	if d.TrackAlias, n, err = vi64.Parse(data); err != nil {
		return nil, err
	}
	data = data[n:]
	if d.GroupID, n, err = vi64.Parse(data); err != nil {
		return nil, err
	}
	data = data[n:]
	if t&datagramFlagZeroObjectID == 0 {
		if d.ObjectID, n, err = vi64.Parse(data); err != nil {
			return nil, err
		}
		data = data[n:]
	}
	if !d.DefaultPriority {
		if len(data) < 1 {
			return nil, errUnexpectedEndOfDatagram
		}
		d.Priority = data[0]
		data = data[1:]
	}
	if t&datagramFlagProperties != 0 {
		var length uint64
		if length, n, err = vi64.Parse(data); err != nil {
			return nil, err
		}
		// A PROPERTIES bit with an empty block is a PROTOCOL_VIOLATION: the
		// bit exists to say there is something there.
		if length == 0 {
			return nil, errEmptyDatagramProperties
		}
		data = data[n:]
		if uint64(len(data)) < length {
			return nil, errUnexpectedEndOfDatagram
		}
		if _, err = d.Properties.parseAll(data[:length]); err != nil {
			return nil, err
		}
		if err = ValidateObjectProperties(d.Properties); err != nil {
			return nil, err
		}
		data = data[length:]
	}

	if !hasStatus {
		// No length field: the payload is the rest of the datagram.
		d.Payload = data
		return d, nil
	}

	var status uint64
	if status, n, err = vi64.Parse(data); err != nil {
		return nil, err
	}
	data = data[n:]
	d.Status = ObjectStatus(status)
	if !d.Status.valid() {
		return nil, errInvalidObjectStatus
	}
	if d.Status != ObjectStatusNormal && len(d.Properties) > 0 {
		return nil, errPropertiesOnNonNormalObject
	}
	if len(data) > 0 {
		return nil, errTrailingBytes
	}
	return d, nil
}

// unknownDatagramTypeError is a datagram whose type does not match the form
// 0b00X0XXXX. The session MUST be closed.
type unknownDatagramTypeError struct {
	datagramType uint64
}

func (e unknownDatagramTypeError) Error() string {
	return fmt.Sprintf("invalid object datagram type %#x", e.datagramType)
}
