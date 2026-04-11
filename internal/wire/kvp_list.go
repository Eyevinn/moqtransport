package wire

import (
	"bufio"
	"fmt"
	"io"
	"sort"

	"github.com/quic-go/quic-go/quicvarint"
)

// Setup parameters
const (
	PathParameterKey                  = 0x01
	MaxRequestIDParameterKey          = 0x02
	MaxAuthTokenCacheSizeParameterKey = 0x04
)

// Version specific parameters
const (
	DeliveryTimeoutParameterKey    = 0x02
	AuthorizationTokenParameterKey = 0x03
	MaxCacheDurationParameterKey   = 0x04
)

type KVPList []KeyValuePair

func (pp KVPList) length() uint64 {
	length := uint64(0)
	for _, p := range pp {
		length += p.length()
	}
	return length
}

// Appends pp to buf with a prefix indicating the number of elements
func (pp KVPList) appendNum(buf []byte) []byte {
	buf = quicvarint.Append(buf, uint64(len(pp)))
	return pp.append(buf)
}

// Appends pp to buf with a prefix indicating the length in bytes
func (pp KVPList) appendLength(buf []byte) []byte {
	buf = quicvarint.Append(buf, pp.length())
	return pp.append(buf)
}

func (pp KVPList) append(buf []byte) []byte {
	for _, p := range pp {
		buf = p.append(buf)
	}
	return buf
}

// appendDelta appends all parameters using delta-encoded types (draft-16+).
// Parameters must be sorted by ascending Type before calling.
func (pp KVPList) appendDelta(buf []byte) []byte {
	var prevType uint64
	for _, p := range pp {
		buf = p.appendDelta(buf, prevType)
		prevType = p.Type
	}
	return buf
}

// AppendNumVersioned appends with count prefix, using delta encoding for draft-16+.
func (pp KVPList) AppendNumVersioned(v Version, buf []byte) []byte {
	buf = quicvarint.Append(buf, uint64(len(pp)))
	if v.NegotiatedViaALPN() {
		sorted := pp.sorted()
		return sorted.appendDelta(buf)
	}
	return pp.append(buf)
}

// ParseNumVersioned parses a count-prefixed parameter list, using delta decoding for draft-16+.
func (pp *KVPList) ParseNumVersioned(v Version, data []byte) error {
	numParameters, n, err := quicvarint.Parse(data)
	if err != nil {
		return err
	}
	data = data[n:]

	if v.NegotiatedViaALPN() {
		var prevType uint64
		for i := uint64(0); i < numParameters; i++ {
			param := KeyValuePair{}
			n, err := param.parseDelta(data, prevType)
			if err != nil {
				return err
			}
			prevType = param.Type
			data = data[n:]
			*pp = append(*pp, param)
		}
		return nil
	}

	for i := uint64(0); i < numParameters; i++ {
		param := KeyValuePair{}
		n, err := param.parse(data)
		if err != nil {
			return err
		}
		data = data[n:]
		*pp = append(*pp, param)
	}
	return nil
}

// sorted returns a copy sorted by ascending Type (required for delta encoding).
func (pp KVPList) sorted() KVPList {
	cp := make(KVPList, len(pp))
	copy(cp, pp)
	sort.Slice(cp, func(i, j int) bool { return cp[i].Type < cp[j].Type })
	return cp
}

func (pp KVPList) String() string {
	res := "["
	i := 0
	for _, v := range pp {
		if i < len(pp)-1 {
			res += fmt.Sprintf("%v, ", v)
		} else {
			res += fmt.Sprintf("%v", v)
		}
		i++
	}
	return res + "]"
}

func (pp *KVPList) parseLengthReader(br *bufio.Reader) error {
	length, err := quicvarint.Read(br)
	if err != nil {
		return err
	}
	if length == 0 {
		return nil
	}
	lr := io.LimitReader(br, int64(length))
	lbr := bufio.NewReader(quicvarint.NewReader(lr))
	for {
		var hdrExt KeyValuePair
		if err = hdrExt.parseReader(lbr); err != nil {
			return err
		}
		*pp = append(*pp, hdrExt)
	}
}

// Parses pp from data based on a length prefix in number of elements
func (pp *KVPList) parseNum(data []byte) error {
	numParameters, n, err := quicvarint.Parse(data)
	if err != nil {
		return err
	}
	data = data[n:]

	for i := uint64(0); i < numParameters; i++ {
		param := KeyValuePair{}
		n, err := param.parse(data)
		if err != nil {
			return err
		}
		data = data[n:]
		*pp = append(*pp, param)
	}
	return nil
}

// Parses pp from data based on a length prefix in bytes
func (pp *KVPList) parseLength(data []byte) (parsed int, err error) {
	length, n, err := quicvarint.Parse(data)
	parsed += n
	if err != nil {
		return
	}
	data = data[n:]
	data = data[:length]

	for len(data) > 0 {
		var hdrExt KeyValuePair
		n, err = hdrExt.parse(data)
		parsed += n
		if err != nil {
			return parsed, err
		}
		*pp = append(*pp, hdrExt)
	}
	return
}
