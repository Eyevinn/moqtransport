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

func TestKVPListAppendNum(t *testing.T) {
	t.Run("prefixes the element count and delta-encodes the types", func(t *testing.T) {
		list := KVPList{
			{Type: 2, ValueVarInt: 1},
			{Type: 6, ValueVarInt: 2},
		}
		assert.Equal(t, []byte{0x02, 0x02, 0x01, 0x04, 0x02}, list.appendNum(nil))
	})

	t.Run("sorts into non-decreasing type order", func(t *testing.T) {
		list := KVPList{
			{Type: 6, ValueVarInt: 2},
			{Type: 2, ValueVarInt: 1},
		}
		assert.Equal(t, []byte{0x02, 0x02, 0x01, 0x04, 0x02}, list.appendNum(nil))
	})

	t.Run("does not mutate the caller's list", func(t *testing.T) {
		list := KVPList{{Type: 6}, {Type: 2}}
		list.appendNum(nil)
		assert.Equal(t, uint64(6), list[0].Type)
	})

	t.Run("keeps repeated types in their original order", func(t *testing.T) {
		list := KVPList{
			{Type: 3, ValueBytes: []byte("b")},
			{Type: 1, ValueBytes: []byte("x")},
			{Type: 3, ValueBytes: []byte("a")},
		}
		var got KVPList
		n, err := got.parseNum(list.appendNum(nil))
		require.NoError(t, err)
		assert.Equal(t, len(list.appendNum(nil)), n)
		require.Len(t, got, 3)
		assert.Equal(t, []byte("x"), got[0].ValueBytes)
		assert.Equal(t, []byte("b"), got[1].ValueBytes)
		assert.Equal(t, []byte("a"), got[2].ValueBytes)
	})
}

func TestKVPListParseNum(t *testing.T) {
	t.Run("stops after the declared count", func(t *testing.T) {
		data := []byte{0x01, 0x02, 0x01, 0xff, 0xff}
		var list KVPList
		n, err := list.parseNum(data)
		require.NoError(t, err)
		assert.Equal(t, 3, n)
		require.Len(t, list, 1)
		assert.Equal(t, uint64(2), list[0].Type)
	})

	t.Run("rejects a count larger than the remaining bytes", func(t *testing.T) {
		var list KVPList
		_, err := list.parseNum([]byte{0x0a, 0x02, 0x01})
		assert.ErrorIs(t, err, io.ErrUnexpectedEOF)
	})

	t.Run("parses an empty list", func(t *testing.T) {
		var list KVPList
		n, err := list.parseNum([]byte{0x00})
		require.NoError(t, err)
		assert.Equal(t, 1, n)
		assert.Empty(t, list)
	})
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
