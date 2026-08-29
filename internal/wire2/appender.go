package wire2

import (
	"encoding/binary"
	"math"

	"github.com/Eyevinn/locmaf/vi64"
)

// maxControlMessageBodyLen is the largest body a control message can carry.
// The Message Length field is a fixed 16-bit integer, not a varint
// (draft-ietf-moq-transport-18, Section 10), so anything longer is
// unrepresentable and must be prevented at the source: namespace tuples,
// auth tokens and parameter lists all have to be bounded to fit.
const maxControlMessageBodyLen = math.MaxUint16

// AppendControlMessage frames m onto buf as
//
//	Message Type (vi64) | Message Length (16) | Message Body (..)
//
// The length is written as a placeholder and back-patched once the body is
// known, since the body length cannot be computed in advance: vi64 lengths
// depend on the values and the trailing Track Properties block has no count.
func AppendControlMessage(buf []byte, m MessageV18) ([]byte, error) {
	buf = vi64.Append(buf, uint64(m.Type()))
	lengthOffset := len(buf)
	buf = append(buf, 0, 0)

	buf, err := m.appendV18(buf)
	if err != nil {
		return nil, err
	}

	bodyLen := len(buf) - lengthOffset - 2
	if bodyLen > maxControlMessageBodyLen {
		return nil, errControlMessageTooLong
	}
	binary.BigEndian.PutUint16(buf[lengthOffset:lengthOffset+2], uint16(bodyLen))
	return buf, nil
}
