package wire

import (
	"fmt"

	"github.com/quic-go/quic-go/quicvarint"
)

type KeyValuePair struct {
	Type        uint64
	ValueBytes  []byte
	ValueVarInt uint64
}

func (p *KeyValuePair) String() string {
	if p.Type%2 == 1 {
		return fmt.Sprintf("{key: %v, value: '%v'}", p.Type, p.ValueBytes)
	}
	return fmt.Sprintf("{key: %v, value: '%v'}", p.Type, p.ValueVarInt)
}

func (p KeyValuePair) length() uint64 {
	length := uint64(quicvarint.Len(p.Type))
	if p.Type%2 == 1 {
		length += uint64(quicvarint.Len(uint64(len(p.ValueBytes))))
		length += uint64(len(p.ValueBytes))
		return length
	}
	length += uint64(quicvarint.Len(p.ValueVarInt))
	return length
}

func (p KeyValuePair) append(buf []byte) []byte {
	buf = quicvarint.Append(buf, p.Type)
	if p.Type%2 == 1 {
		buf = quicvarint.Append(buf, uint64(len(p.ValueBytes)))
		return append(buf, p.ValueBytes...)
	}
	return quicvarint.Append(buf, p.ValueVarInt)
}

// appendDelta appends this parameter using delta-encoded type.
// The delta is (p.Type - prevType). The caller must ensure ascending order.
func (p KeyValuePair) appendDelta(buf []byte, prevType uint64) []byte {
	delta := p.Type - prevType
	buf = quicvarint.Append(buf, delta)
	if p.Type%2 == 1 {
		buf = quicvarint.Append(buf, uint64(len(p.ValueBytes)))
		return append(buf, p.ValueBytes...)
	}
	return quicvarint.Append(buf, p.ValueVarInt)
}

func (p *KeyValuePair) parse(data []byte) (int, error) {
	var n, parsed int
	var err error
	p.Type, n, err = quicvarint.Parse(data)
	parsed += n
	if err != nil {
		return n, err
	}
	data = data[n:]

	if p.Type%2 == 1 {
		var length uint64
		length, n, err = quicvarint.Parse(data)
		parsed += n
		if err != nil {
			return parsed, err
		}
		data = data[n:]
		p.ValueBytes = make([]byte, length) // TODO: Don't allocate memory here?
		m := copy(p.ValueBytes, data[:length])
		parsed += m
		if uint64(m) != length {
			return parsed, errLengthMismatch
		}
		return parsed, nil
	}

	p.ValueVarInt, n, err = quicvarint.Parse(data)
	parsed += n
	return parsed, err
}

// parseDelta parses a parameter with delta-encoded type.
// prevType is added to the delta to recover the absolute type.
func (p *KeyValuePair) parseDelta(data []byte, prevType uint64) (int, error) {
	var n, parsed int
	var err error
	var delta uint64
	delta, n, err = quicvarint.Parse(data)
	parsed += n
	if err != nil {
		return n, err
	}
	p.Type = prevType + delta
	data = data[n:]

	if p.Type%2 == 1 {
		var length uint64
		length, n, err = quicvarint.Parse(data)
		parsed += n
		if err != nil {
			return parsed, err
		}
		data = data[n:]
		p.ValueBytes = make([]byte, length)
		m := copy(p.ValueBytes, data[:length])
		parsed += m
		if uint64(m) != length {
			return parsed, errLengthMismatch
		}
		return parsed, nil
	}

	p.ValueVarInt, n, err = quicvarint.Parse(data)
	parsed += n
	return parsed, err
}

