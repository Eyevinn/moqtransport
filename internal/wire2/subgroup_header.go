package wire2

import (
	"fmt"
	"io"

	"github.com/Eyevinn/locmaf/vi64"
)

// The SUBGROUP_HEADER stream type is a bitfield of the form 0b0XX1XXXX
// (draft-ietf-moq-transport-18, Section 11.4.2). Every optional header field
// is signalled by it, so the type and the header's contents are two views of
// the same thing.
const (
	subgroupFlagProperties      = 0x01
	subgroupIDModeMask          = 0x06
	subgroupIDModeShift         = 1
	subgroupFlagEndOfGroup      = 0x08
	subgroupMarker              = 0x10
	subgroupFlagDefaultPriority = 0x20
	subgroupFlagFirstObject     = 0x40
)

// SubgroupIDMode is the two-bit SUBGROUP_ID_MODE field of the stream type.
type SubgroupIDMode uint8

const (
	// SubgroupIDZero omits the field; the Subgroup ID is 0.
	SubgroupIDZero SubgroupIDMode = 0b00
	// SubgroupIDFirstObject omits the field; the Subgroup ID is the Object ID
	// of the first Object on the stream, so it is not known until that Object
	// has been read.
	SubgroupIDFirstObject SubgroupIDMode = 0b01
	// SubgroupIDExplicit carries the Subgroup ID in the header.
	SubgroupIDExplicit SubgroupIDMode = 0b10
	// subgroupIDModeReserved is 0b11, reserved for future use. Sixteen stream
	// types carry it and every one of them is a PROTOCOL_VIOLATION.
	subgroupIDModeReserved SubgroupIDMode = 0b11
)

func (m SubgroupIDMode) String() string {
	switch m {
	case SubgroupIDZero:
		return "zero"
	case SubgroupIDFirstObject:
		return "first object ID"
	case SubgroupIDExplicit:
		return "explicit"
	}
	return "reserved"
}

// SubgroupHeader opens a stream carrying Objects with Object Forwarding
// Preference = Subgroup (Section 11.4.2).
type SubgroupHeader struct {
	TrackAlias uint64
	GroupID    uint64

	// SubgroupIDMode says how the Subgroup ID is carried. SubgroupID holds it
	// once it is known, which for SubgroupIDFirstObject is only after the
	// first Object has been read; see SubgroupReader.
	SubgroupIDMode SubgroupIDMode
	SubgroupID     uint64

	// Priority applies to every Object on the stream. DefaultPriority leaves
	// it off the wire, and the Objects then inherit the priority from the
	// control message that established the subscription.
	Priority        uint8
	DefaultPriority bool

	// HasProperties says a Properties block is present in *every* Object on
	// this stream, not merely permitted in some: an Object with no properties
	// still writes a Properties Length of 0. Losing this bit between the
	// header and the object parser is why properties cannot round-trip in
	// upstream's implementation, so it is carried by SubgroupReader and
	// SubgroupWriter rather than passed per object.
	HasProperties bool

	// EndOfGroup says this subgroup carries the largest Object in the Group,
	// so a FIN on this stream means no Object in the Group beyond the last one
	// received exists. A reset says nothing of the kind.
	EndOfGroup bool

	// FirstObject says the first Object on this stream is the first Object the
	// original publisher published in the subgroup.
	FirstObject bool
}

// StreamType returns the leading varint this header encodes to. It is derived
// from the fields rather than stored, so a header cannot describe one thing
// and announce another.
func (h *SubgroupHeader) StreamType() StreamType {
	t := uint64(subgroupMarker)
	t |= uint64(h.SubgroupIDMode) << subgroupIDModeShift
	if h.HasProperties {
		t |= subgroupFlagProperties
	}
	if h.EndOfGroup {
		t |= subgroupFlagEndOfGroup
	}
	if h.DefaultPriority {
		t |= subgroupFlagDefaultPriority
	}
	if h.FirstObject {
		t |= subgroupFlagFirstObject
	}
	return StreamType(t)
}

func (h *SubgroupHeader) String() string {
	return fmt.Sprintf("SUBGROUP_HEADER{alias=%d group=%d subgroup=%d(%v) priority=%d}",
		h.TrackAlias, h.GroupID, h.SubgroupID, h.SubgroupIDMode, h.Priority)
}

// AppendSubgroupHeader writes the stream type and the header fields that
// follow it, which together are the first bytes on a subgroup stream.
func AppendSubgroupHeader(buf []byte, h *SubgroupHeader) ([]byte, error) {
	if h.SubgroupIDMode == subgroupIDModeReserved {
		return nil, errReservedSubgroupIDMode
	}
	buf = vi64.Append(buf, uint64(h.StreamType()))
	buf = vi64.Append(buf, h.TrackAlias)
	buf = vi64.Append(buf, h.GroupID)
	if h.SubgroupIDMode == SubgroupIDExplicit {
		buf = vi64.Append(buf, h.SubgroupID)
	}
	if !h.DefaultPriority {
		buf = append(buf, h.Priority)
	}
	return buf, nil
}

// ParseSubgroupHeader reads the header fields that follow stream type t, which
// the caller has already read and classified with ReadStreamType.
//
// A stream that ends part-way through the header gives io.ErrUnexpectedEOF:
// Section 11.4 makes a FIN in the middle of a serialized Object a
// PROTOCOL_VIOLATION, and the header is no different.
func ParseSubgroupHeader(t StreamType, r byteReader) (*SubgroupHeader, error) {
	if !t.IsSubgroupHeader() {
		return nil, unknownStreamTypeError{streamType: t}
	}
	mode := SubgroupIDMode((uint64(t) & subgroupIDModeMask) >> subgroupIDModeShift)
	if mode == subgroupIDModeReserved {
		return nil, errReservedSubgroupIDMode
	}

	h := &SubgroupHeader{
		SubgroupIDMode:  mode,
		HasProperties:   uint64(t)&subgroupFlagProperties != 0,
		EndOfGroup:      uint64(t)&subgroupFlagEndOfGroup != 0,
		DefaultPriority: uint64(t)&subgroupFlagDefaultPriority != 0,
		FirstObject:     uint64(t)&subgroupFlagFirstObject != 0,
	}

	var err error
	if h.TrackAlias, err = vi64.Read(r); err != nil {
		return nil, unexpectedEOF(err)
	}
	if h.GroupID, err = vi64.Read(r); err != nil {
		return nil, unexpectedEOF(err)
	}
	if mode == SubgroupIDExplicit {
		if h.SubgroupID, err = vi64.Read(r); err != nil {
			return nil, unexpectedEOF(err)
		}
	}
	if !h.DefaultPriority {
		priority, err := r.ReadByte()
		if err != nil {
			return nil, unexpectedEOF(err)
		}
		h.Priority = priority
	}
	return h, nil
}

// ReadSubgroupHeader reads a whole subgroup stream header, stream type
// included. It exists for tests and for callers that have not already sniffed
// the type; the session's accept loop uses ReadStreamType and
// ParseSubgroupHeader instead, because it has to classify the stream before it
// knows a subgroup header is what follows.
func ReadSubgroupHeader(r byteReader) (*SubgroupHeader, error) {
	t, kind, err := ReadStreamType(r)
	if err != nil {
		if err == io.EOF {
			return nil, io.EOF
		}
		return nil, err
	}
	if kind != UniStreamSubgroup {
		return nil, unknownStreamTypeError{streamType: t}
	}
	return ParseSubgroupHeader(t, r)
}
