package wire2

import (
	"io"
	"math"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLocationRoundTrip(t *testing.T) {
	for _, l := range []Location{
		{},
		{Group: 1, Object: 2},
		{Group: 127, Object: 128},
		{Group: math.MaxUint64, Object: math.MaxUint64},
	} {
		buf := l.append(nil)
		var got Location
		n, err := got.parse(buf)
		require.NoError(t, err)
		assert.Equal(t, len(buf), n)
		assert.Equal(t, l, got)
	}
}

func TestLocationAppend(t *testing.T) {
	// 64 and 127 are one byte in vi64 where RFC 9000 needs two, so this also
	// pins that we are not writing QUIC varints.
	assert.Equal(t, []byte{0x40, 0x7f}, Location{Group: 64, Object: 127}.append(nil))
}

func TestLocationParseTruncated(t *testing.T) {
	var l Location
	_, err := l.parse([]byte{0x01})
	assert.ErrorIs(t, err, io.ErrUnexpectedEOF)
}

func TestLocationParseLeavesFieldsUnsetOnError(t *testing.T) {
	l := Location{Group: 9, Object: 9}
	_, err := l.parse([]byte{0x01})
	require.Error(t, err)
	assert.Equal(t, Location{Group: 9, Object: 9}, l)
}
