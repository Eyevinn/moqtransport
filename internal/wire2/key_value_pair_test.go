package wire2

import (
	"io"
	"math"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestKeyValuePairAppendDelta(t *testing.T) {
	cases := []struct {
		name     string
		p        KeyValuePair
		prevType uint64
		expect   []byte
	}{
		{
			name:   "even type carries a varint",
			p:      KeyValuePair{Type: 2, ValueVarInt: 37},
			expect: []byte{0x02, 0x25},
		},
		{
			name:   "odd type carries length-prefixed bytes",
			p:      KeyValuePair{Type: 1, ValueBytes: []byte("A")},
			expect: []byte{0x01, 0x01, 'A'},
		},
		{
			name:   "odd type with an empty value still writes the length",
			p:      KeyValuePair{Type: 1, ValueBytes: nil},
			expect: []byte{0x01, 0x00},
		},
		{
			name:     "the type is written as a delta",
			p:        KeyValuePair{Type: 10, ValueVarInt: 1},
			prevType: 4,
			expect:   []byte{0x06, 0x01},
		},
		{
			name:     "a repeated type is a zero delta",
			p:        KeyValuePair{Type: 4, ValueVarInt: 1},
			prevType: 4,
			expect:   []byte{0x00, 0x01},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.expect, tc.p.appendDelta(nil, tc.prevType))
			assert.Equal(t, len(tc.expect), tc.p.appendLen(tc.prevType))
		})
	}
}

func TestKeyValuePairParseDelta(t *testing.T) {
	t.Run("recovers the absolute type", func(t *testing.T) {
		var p KeyValuePair
		n, err := p.parseDelta([]byte{0x06, 0x01}, 4)
		require.NoError(t, err)
		assert.Equal(t, 2, n)
		assert.Equal(t, uint64(10), p.Type)
		assert.Equal(t, uint64(1), p.ValueVarInt)
	})

	t.Run("copies the value instead of aliasing", func(t *testing.T) {
		data := []byte{0x01, 0x01, 'A'}
		var p KeyValuePair
		_, err := p.parseDelta(data, 0)
		require.NoError(t, err)
		data[2] = 'B'
		assert.Equal(t, []byte("A"), p.ValueBytes)
	})

	t.Run("accepts a non-minimal delta", func(t *testing.T) {
		var p KeyValuePair
		n, err := p.parseDelta([]byte{0x80, 0x02, 0x25}, 0)
		require.NoError(t, err)
		assert.Equal(t, 3, n)
		assert.Equal(t, uint64(2), p.Type)
		assert.Equal(t, uint64(37), p.ValueVarInt)
	})

	t.Run("rejects a delta that overflows the type space", func(t *testing.T) {
		var p KeyValuePair
		// A 9-byte varint holding MaxUint64 as the delta, on top of a
		// non-zero previous type.
		data := []byte{0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff}
		_, err := p.parseDelta(data, 1)
		assert.ErrorIs(t, err, errDeltaTypeOverflow)
	})

	t.Run("accepts a delta that lands exactly on the largest type", func(t *testing.T) {
		var p KeyValuePair
		data := append([]byte{0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xfe}, 0x00)
		_, err := p.parseDelta(data, 1)
		require.NoError(t, err)
		assert.Equal(t, uint64(math.MaxUint64), p.Type)
	})

	t.Run("rejects a value longer than 65535 bytes", func(t *testing.T) {
		var p KeyValuePair
		// Type 1 (odd), length 65536.
		data := append([]byte{0x01}, vi64Bytes(maxValueLength+1)...)
		_, err := p.parseDelta(data, 0)
		assert.ErrorIs(t, err, errValueTooLong)
	})

	t.Run("rejects a value that runs past the end of the block", func(t *testing.T) {
		var p KeyValuePair
		_, err := p.parseDelta([]byte{0x01, 0x04, 'A'}, 0)
		assert.ErrorIs(t, err, io.ErrUnexpectedEOF)
	})

	t.Run("rejects a truncated pair", func(t *testing.T) {
		var p KeyValuePair
		_, err := p.parseDelta([]byte{0x02}, 0)
		assert.ErrorIs(t, err, io.ErrUnexpectedEOF)
	})
}

func TestKeyValuePairRoundTrip(t *testing.T) {
	pairs := []KeyValuePair{
		{Type: 0, ValueVarInt: 0},
		{Type: 1, ValueBytes: []byte("token")},
		{Type: 2, ValueVarInt: math.MaxUint64},
		{Type: math.MaxUint64 - 1, ValueVarInt: 5},
	}
	for _, p := range pairs {
		buf := p.appendDelta(nil, 0)
		var got KeyValuePair
		n, err := got.parseDelta(buf, 0)
		require.NoError(t, err)
		assert.Equal(t, len(buf), n)
		assert.Equal(t, p.Type, got.Type)
		assert.Equal(t, p.ValueVarInt, got.ValueVarInt)
		if p.Type%2 == 1 {
			assert.Equal(t, p.ValueBytes, got.ValueBytes)
		}
	}
}
