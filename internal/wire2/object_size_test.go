package wire2

import (
	"bytes"
	"io"
	"testing"

	"github.com/Eyevinn/locmaf/vi64"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A length the peer declares is only a claim. One far beyond memory must be
// refused before anything is allocated: make() panics on a length this large,
// and nothing above the reader recovers.
func TestSubgroupObjectHugeLengthIsRefused(t *testing.T) {
	header := &SubgroupHeader{TrackAlias: 1, GroupID: 2}
	buf := vi64.Append(nil, 0)    // Object ID delta
	buf = vi64.Append(buf, 1<<52) // Object Payload Length
	r := NewSubgroupReader(header, reader(buf), Version18)
	_, err := r.Next()
	assert.ErrorIs(t, err, ErrObjectTooLarge)

	withProps := &SubgroupHeader{TrackAlias: 1, GroupID: 2, HasProperties: true}
	buf = vi64.Append(nil, 0)     // Object ID delta
	buf = vi64.Append(buf, 1<<52) // Properties Length
	r = NewSubgroupReader(withProps, reader(buf), Version18)
	_, err = r.Next()
	assert.ErrorIs(t, err, ErrObjectTooLarge)
}

// MaxObjectSize is inclusive: an Object exactly at the limit is read, one
// byte over is refused.
func TestSubgroupObjectMaxObjectSize(t *testing.T) {
	header := &SubgroupHeader{TrackAlias: 1, GroupID: 2}
	buf, err := NewSubgroupWriter(header).AppendObject(nil, &SubgroupObject{
		ObjectID: 0, Payload: bytes.Repeat([]byte{'x'}, 100),
	})
	require.NoError(t, err)

	r := NewSubgroupReader(header, reader(buf), Version18)
	r.MaxObjectSize = 100
	obj, err := r.Next()
	require.NoError(t, err)
	assert.Len(t, obj.Payload, 100)

	r = NewSubgroupReader(header, reader(buf), Version18)
	r.MaxObjectSize = 99
	_, err = r.Next()
	assert.ErrorIs(t, err, ErrObjectTooLarge)
}

// A payload above eagerAlloc is read in pieces as it arrives; it must come
// out whole and in order.
func TestSubgroupObjectLargePayload(t *testing.T) {
	payload := make([]byte, 3*eagerAlloc+5)
	for i := range payload {
		payload[i] = byte(i * 7)
	}
	header := &SubgroupHeader{TrackAlias: 1, GroupID: 2}
	buf, err := NewSubgroupWriter(header).AppendObject(nil, &SubgroupObject{ObjectID: 0, Payload: payload})
	require.NoError(t, err)

	obj, err := NewSubgroupReader(header, reader(buf), Version18).Next()
	require.NoError(t, err)
	assert.True(t, bytes.Equal(payload, obj.Payload), "payload differs")

	// The same stream cut short part-way through is a truncation.
	cut := buf[:len(buf)-eagerAlloc]
	_, err = NewSubgroupReader(header, reader(cut), Version18).Next()
	assert.ErrorIs(t, err, io.ErrUnexpectedEOF)
}

func TestFetchObjectMaxObjectSize(t *testing.T) {
	w := NewFetchWriter(GroupOrderAscending, Version18)
	buf, err := w.AppendObject(nil, &FetchObject{
		GroupID: 1, ObjectID: 0, Priority: 128, Payload: bytes.Repeat([]byte{'x'}, 100),
	})
	require.NoError(t, err)

	r := NewFetchReader(reader(buf), GroupOrderAscending, Version18)
	r.MaxObjectSize = 99
	_, err = r.Next()
	assert.ErrorIs(t, err, ErrObjectTooLarge)

	r = NewFetchReader(reader(buf), GroupOrderAscending, Version18)
	obj, err := r.Next()
	require.NoError(t, err, "zero means the default limit")
	assert.Len(t, obj.Payload, 100)
}
