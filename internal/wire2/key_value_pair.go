package wire2

import (
	"fmt"
	"io"
	"math"

	"github.com/Eyevinn/locmaf/vi64"
)

// maxValueLength is the largest Key-Value-Pair value draft-18 allows
// (Section 1.4.3). A longer one is a PROTOCOL_VIOLATION.
const maxValueLength = 1<<16 - 1

// KeyValuePair is the MOQT Key-Value-Pair (draft-ietf-moq-transport-18,
// Section 1.4.3). It carries the absolute Type; the delta against the
// preceding Type only exists on the wire.
//
// The parity of Type selects the value: an even Type carries a varint in
// ValueVarInt, an odd Type a length-prefixed byte string in ValueBytes.
//
// One structure serves three registries with different rules — Setup Options,
// Message Parameters and Properties — which differ in how the enclosing block
// is bounded and in what an unknown Type means. See kvp_list.go.
type KeyValuePair struct {
	Type        uint64
	ValueBytes  []byte
	ValueVarInt uint64
}

func (p KeyValuePair) String() string {
	if p.Type%2 == 1 {
		return fmt.Sprintf("{key: %v, value: '%v'}", p.Type, p.ValueBytes)
	}
	return fmt.Sprintf("{key: %v, value: %v}", p.Type, p.ValueVarInt)
}

// appendLen returns the number of bytes appendDelta writes for this pair with
// the given preceding Type.
//
// It reports the length of the encoding this package produces, which is always
// the shortest form. It must never be used to advance over a *received* pair:
// draft-18 permits non-minimal varints, so a parsed pair can be longer. Use
// the byte count parseDelta returns for that.
func (p KeyValuePair) appendLen(prevType uint64) int {
	n := vi64.Len(p.Type - prevType)
	if p.Type%2 == 1 {
		return n + vi64.Len(uint64(len(p.ValueBytes))) + len(p.ValueBytes)
	}
	return n + vi64.Len(p.ValueVarInt)
}

// appendDelta writes the pair with its Type delta-encoded against prevType.
// The caller must pass the Types in non-decreasing order.
func (p KeyValuePair) appendDelta(buf []byte, prevType uint64) []byte {
	buf = vi64.Append(buf, p.Type-prevType)
	if p.Type%2 == 1 {
		buf = vi64.Append(buf, uint64(len(p.ValueBytes)))
		return append(buf, p.ValueBytes...)
	}
	return vi64.Append(buf, p.ValueVarInt)
}

// parseDelta reads one pair from the start of data, recovering the absolute
// Type by adding the delta to prevType, and returns the bytes consumed.
//
// The value is copied rather than aliased into data: the same codec parses
// object Properties, where the caller may be reading into a reusable buffer.
func (p *KeyValuePair) parseDelta(data []byte, prevType uint64) (int, error) {
	delta, parsed, err := vi64.Parse(data)
	if err != nil {
		return parsed, err
	}
	if delta > math.MaxUint64-prevType {
		return parsed, errDeltaTypeOverflow
	}
	p.Type = prevType + delta
	data = data[parsed:]

	if p.Type%2 == 0 {
		value, n, err := vi64.Parse(data)
		parsed += n
		if err != nil {
			return parsed, err
		}
		p.ValueVarInt = value
		return parsed, nil
	}

	length, n, err := vi64.Parse(data)
	parsed += n
	if err != nil {
		return parsed, err
	}
	data = data[n:]
	if length > maxValueLength {
		return parsed, errValueTooLong
	}
	if uint64(len(data)) < length {
		return parsed, io.ErrUnexpectedEOF
	}
	p.ValueBytes = make([]byte, length)
	copy(p.ValueBytes, data)
	return parsed + int(length), nil
}
