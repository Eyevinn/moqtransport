package wire2

import (
	"io"
	"sort"
	"strings"

	"github.com/Eyevinn/locmaf/vi64"
)

// KVPList is a sequence of Key-Value-Pairs. draft-18 delta-encodes the Types
// against the preceding one, so a list is always written in non-decreasing
// Type order; the append methods sort a copy rather than requiring the caller
// to keep the list sorted.
//
// Three blocks in the wire format hold a KVPList, and they differ only in how
// the sequence is bounded:
//
//   - count-prefixed, used by Message Parameters — appendNum / parseNum
//   - byte-length-prefixed, used by object Properties — appendLength / parseLength
//   - unbounded to the end of the enclosing body, used by Setup Options and by
//     trailing Track Properties — appendDelta / parseAll
//
// The registries they draw on differ further in what an unknown Type means:
// a Setup Option MUST be ignored, a Message Parameter is a PROTOCOL_VIOLATION.
// That distinction belongs to the caller that knows the registry, not here.
type KVPList []KeyValuePair

// appendNum writes the list prefixed by its element count.
func (pp KVPList) appendNum(buf []byte) []byte {
	buf = vi64.Append(buf, uint64(len(pp)))
	return pp.appendDelta(buf)
}

// appendLength writes the list prefixed by its length in bytes.
func (pp KVPList) appendLength(buf []byte) []byte {
	sorted := pp.sorted()
	buf = vi64.Append(buf, uint64(sorted.appendLen()))
	return sorted.appendUnsorted(buf)
}

// appendDelta writes the list with no prefix of its own. The enclosing message
// or object length bounds it.
func (pp KVPList) appendDelta(buf []byte) []byte {
	return pp.sorted().appendUnsorted(buf)
}

// appendUnsorted writes an already-sorted list.
func (pp KVPList) appendUnsorted(buf []byte) []byte {
	var prevType uint64
	for _, p := range pp {
		buf = p.appendDelta(buf, prevType)
		prevType = p.Type
	}
	return buf
}

// appendLen returns the number of bytes an already-sorted list occupies when
// written by appendUnsorted. Like KeyValuePair.appendLen it describes what
// this package writes, never what it read.
func (pp KVPList) appendLen() int {
	var prevType uint64
	total := 0
	for _, p := range pp {
		total += p.appendLen(prevType)
		prevType = p.Type
	}
	return total
}

// parseNum reads a count-prefixed list and returns the bytes consumed.
func (pp *KVPList) parseNum(data []byte) (int, error) {
	count, parsed, err := vi64.Parse(data)
	if err != nil {
		return parsed, err
	}
	// Every pair costs at least one byte, so the remaining length bounds the
	// count and with it the allocation.
	if count > uint64(len(data)-parsed) {
		return parsed, io.ErrUnexpectedEOF
	}
	list := make(KVPList, 0, count)
	prevType := uint64(0)
	for range count {
		var p KeyValuePair
		n, err := p.parseDelta(data[parsed:], prevType)
		parsed += n
		if err != nil {
			return parsed, err
		}
		prevType = p.Type
		list = append(list, p)
	}
	*pp = list
	return parsed, nil
}

// parseLength reads a byte-length-prefixed list and returns the bytes
// consumed, including the prefix.
func (pp *KVPList) parseLength(data []byte) (int, error) {
	length, parsed, err := vi64.Parse(data)
	if err != nil {
		return parsed, err
	}
	data = data[parsed:]
	if uint64(len(data)) < length {
		return parsed, io.ErrUnexpectedEOF
	}
	n, err := pp.parseAll(data[:length])
	return parsed + n, err
}

// parseAll reads pairs until data is exhausted. The caller must pass exactly
// the bytes the block covers.
func (pp *KVPList) parseAll(data []byte) (int, error) {
	list := KVPList{}
	prevType := uint64(0)
	parsed := 0
	for parsed < len(data) {
		var p KeyValuePair
		n, err := p.parseDelta(data[parsed:], prevType)
		parsed += n
		if err != nil {
			return parsed, err
		}
		prevType = p.Type
		list = append(list, p)
	}
	*pp = list
	return parsed, nil
}

// sorted returns a copy ordered by non-decreasing Type, which delta encoding
// requires. Pairs sharing a Type keep their relative order, since parameters
// whose definition allows repeats are distinguished only by position.
func (pp KVPList) sorted() KVPList {
	cp := make(KVPList, len(pp))
	copy(cp, pp)
	sort.SliceStable(cp, func(i, j int) bool { return cp[i].Type < cp[j].Type })
	return cp
}

// Get returns the first pair with the given Type.
func (pp KVPList) Get(typ uint64) (KeyValuePair, bool) {
	for _, p := range pp {
		if p.Type == typ {
			return p, true
		}
	}
	return KeyValuePair{}, false
}

func (pp KVPList) String() string {
	parts := make([]string, 0, len(pp))
	for _, p := range pp {
		parts = append(parts, p.String())
	}
	return "[" + strings.Join(parts, ", ") + "]"
}
