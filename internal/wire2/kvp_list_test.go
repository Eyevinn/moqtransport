package wire2

import (
	"io"
	"testing"

	"github.com/Eyevinn/locmaf/vi64"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func vi64Bytes(v uint64) []byte {
	return vi64.Append(nil, v)
}

// TestKVPListSortsOnAppend pins that the append side puts Types in
// non-decreasing order, which delta encoding requires, and keeps repeated
// Types in their original relative order.
func TestKVPListSortsOnAppend(t *testing.T) {
	list := KVPList{
		{Type: 3, ValueBytes: []byte("b")},
		{Type: 1, ValueBytes: []byte("x")},
		{Type: 3, ValueBytes: []byte("a")},
	}
	buf := list.appendDelta(nil)

	var got KVPList
	n, err := got.parseAll(buf)
	require.NoError(t, err)
	assert.Equal(t, len(buf), n)
	require.Len(t, got, 3)
	assert.Equal(t, []byte("x"), got[0].ValueBytes)
	assert.Equal(t, []byte("b"), got[1].ValueBytes)
	assert.Equal(t, []byte("a"), got[2].ValueBytes)
	assert.Equal(t, uint64(3), list[0].Type, "the caller's list must not be mutated")
}

func TestKVPListLengthPrefixed(t *testing.T) {
	list := KVPList{
		{Type: 1, ValueBytes: []byte("ab")},
		{Type: 4, ValueVarInt: 9},
	}
	buf := list.appendLength(nil)
	// 1 byte length prefix, then 0x01 0x02 'a' 'b' and 0x03 0x09.
	assert.Equal(t, []byte{0x06, 0x01, 0x02, 'a', 'b', 0x03, 0x09}, buf)

	var got KVPList
	n, err := got.parseLength(buf)
	require.NoError(t, err)
	assert.Equal(t, len(buf), n)
	assert.Equal(t, list, got)

	t.Run("rejects a length that runs past the buffer", func(t *testing.T) {
		var short KVPList
		_, err := short.parseLength([]byte{0x08, 0x01, 0x02})
		assert.ErrorIs(t, err, io.ErrUnexpectedEOF)
	})
}

func TestKVPListParseAll(t *testing.T) {
	t.Run("consumes exactly the block it is given", func(t *testing.T) {
		list := KVPList{{Type: 2, ValueVarInt: 1}, {Type: 5, ValueBytes: []byte("z")}}
		buf := list.appendDelta(nil)
		var got KVPList
		n, err := got.parseAll(buf)
		require.NoError(t, err)
		assert.Equal(t, len(buf), n)
		assert.Equal(t, list, got)
	})

	t.Run("parses an empty block", func(t *testing.T) {
		var got KVPList
		n, err := got.parseAll(nil)
		require.NoError(t, err)
		assert.Equal(t, 0, n)
		assert.Empty(t, got)
	})

	t.Run("reports a pair cut off by the block boundary", func(t *testing.T) {
		var got KVPList
		_, err := got.parseAll([]byte{0x01, 0x04, 'A'})
		assert.ErrorIs(t, err, io.ErrUnexpectedEOF)
	})
}

func TestKVPListGet(t *testing.T) {
	list := KVPList{{Type: 2, ValueVarInt: 1}, {Type: 4, ValueVarInt: 2}}
	p, ok := list.Get(4)
	require.True(t, ok)
	assert.Equal(t, uint64(2), p.ValueVarInt)

	_, ok = list.Get(6)
	assert.False(t, ok)
}
