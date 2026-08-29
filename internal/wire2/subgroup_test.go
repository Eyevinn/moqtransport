package wire2

import (
	"bufio"
	"bytes"
	"io"
	"testing"

	"github.com/Eyevinn/locmaf/vi64"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func reader(b []byte) *bufio.Reader {
	return bufio.NewReader(bytes.NewReader(b))
}

// The stream type and the header fields are two views of the same thing, so
// every combination has to survive the round trip through the bitfield.
func TestSubgroupHeaderRoundTrip(t *testing.T) {
	for _, h := range []*SubgroupHeader{
		{TrackAlias: 1, GroupID: 2, SubgroupIDMode: SubgroupIDZero, Priority: 128},
		{TrackAlias: 1, GroupID: 2, SubgroupIDMode: SubgroupIDFirstObject, Priority: 0},
		{TrackAlias: 1, GroupID: 2, SubgroupIDMode: SubgroupIDExplicit, SubgroupID: 7, Priority: 255},
		{TrackAlias: 4611686018427387903, GroupID: 1 << 40, SubgroupIDMode: SubgroupIDExplicit, SubgroupID: 9, DefaultPriority: true},
		{TrackAlias: 1, GroupID: 2, SubgroupIDMode: SubgroupIDZero, HasProperties: true, EndOfGroup: true, FirstObject: true, Priority: 3},
	} {
		t.Run(h.StreamType().String(), func(t *testing.T) {
			buf, err := AppendSubgroupHeader(nil, h)
			require.NoError(t, err)

			got, err := ReadSubgroupHeader(reader(buf))
			require.NoError(t, err)
			assert.Equal(t, h, got)
		})
	}
}

// Every type a header can produce must fall in the ranges Section 11.4.2
// lists, and must classify as a subgroup stream rather than anything else.
func TestSubgroupHeaderStreamTypesAreValid(t *testing.T) {
	for mode := SubgroupIDZero; mode <= SubgroupIDExplicit; mode++ {
		for flags := 0; flags < 8; flags++ {
			h := &SubgroupHeader{
				SubgroupIDMode:  mode,
				HasProperties:   flags&1 != 0,
				EndOfGroup:      flags&2 != 0,
				DefaultPriority: flags&4 != 0,
				FirstObject:     flags&8 != 0,
			}
			st := h.StreamType()
			assert.True(t, st.IsSubgroupHeader(), "%#x should be a subgroup header", uint64(st))

			kind, err := ClassifyStreamType(st)
			require.NoError(t, err)
			assert.Equal(t, UniStreamSubgroup, kind)
		}
	}
}

// SUBGROUP_ID_MODE 0b11 is reserved, and the sixteen types carrying it are a
// PROTOCOL_VIOLATION in both directions.
func TestSubgroupHeaderReservedMode(t *testing.T) {
	_, err := AppendSubgroupHeader(nil, &SubgroupHeader{SubgroupIDMode: 0b11})
	assert.ErrorIs(t, err, errReservedSubgroupIDMode)

	for _, st := range []StreamType{0x16, 0x17, 0x1E, 0x1F, 0x36, 0x76, 0x7F} {
		require.True(t, st.IsSubgroupHeader(), "%#x is in the subgroup range", uint64(st))
		_, err := ParseSubgroupHeader(st, reader([]byte{0x00, 0x00, 0x00}))
		assert.ErrorIs(t, err, errReservedSubgroupIDMode, "stream type %#x", uint64(st))
	}
}

// A header cut short is a truncated Object, which Section 11.4 makes a
// PROTOCOL_VIOLATION rather than a clean end.
func TestSubgroupHeaderTruncated(t *testing.T) {
	full, err := AppendSubgroupHeader(nil, &SubgroupHeader{
		TrackAlias: 300, GroupID: 400, SubgroupIDMode: SubgroupIDExplicit, SubgroupID: 500, Priority: 7,
	})
	require.NoError(t, err)

	for n := 1; n < len(full); n++ {
		_, err := ReadSubgroupHeader(reader(full[:n]))
		assert.ErrorIs(t, err, io.ErrUnexpectedEOF, "truncated to %d bytes", n)
	}
}

// Object IDs are deltas: the first is absolute, the rest are one less than the
// gap, so a run of consecutive IDs writes zero every time.
func TestSubgroupObjectDeltaEncoding(t *testing.T) {
	header := &SubgroupHeader{TrackAlias: 1, GroupID: 2, SubgroupIDMode: SubgroupIDExplicit, SubgroupID: 3}
	objects := []*SubgroupObject{
		{ObjectID: 5, Payload: []byte("a")},
		{ObjectID: 6, Payload: []byte("bb")},
		{ObjectID: 7, Payload: []byte("ccc")},
		{ObjectID: 100, Payload: []byte("gap")},
	}

	w := NewSubgroupWriter(header)
	var buf []byte
	var err error
	for _, obj := range objects {
		buf, err = w.AppendObject(buf, obj)
		require.NoError(t, err)
	}

	// The first Object's delta is its absolute ID; an Object one past the
	// previous writes a zero delta, which is what makes the encoding worth
	// having.
	assert.Equal(t, byte(0x05), buf[0])
	consecutive, err := NewSubgroupWriter(header).AppendObject(nil, &SubgroupObject{ObjectID: 5, Payload: []byte("a")})
	require.NoError(t, err)
	firstLen := len(consecutive)
	w2 := NewSubgroupWriter(header)
	consecutive, err = w2.AppendObject(nil, &SubgroupObject{ObjectID: 5, Payload: []byte("a")})
	require.NoError(t, err)
	consecutive, err = w2.AppendObject(consecutive, &SubgroupObject{ObjectID: 6, Payload: []byte("b")})
	require.NoError(t, err)
	assert.Equal(t, byte(0x00), consecutive[firstLen])

	r := NewSubgroupReader(header, reader(buf))
	for _, want := range objects {
		got, err := r.Next()
		require.NoError(t, err)
		assert.Equal(t, want.ObjectID, got.ObjectID)
		assert.Equal(t, want.Payload, got.Payload)
	}
	_, err = r.Next()
	assert.ErrorIs(t, err, io.EOF, "a clean end between objects is io.EOF")
}

// Under SUBGROUP_ID_MODE 0b01 the Subgroup ID is not on the wire at all; it is
// whatever the first Object's ID turns out to be.
func TestSubgroupIDFromFirstObject(t *testing.T) {
	header := &SubgroupHeader{TrackAlias: 1, GroupID: 2, SubgroupIDMode: SubgroupIDFirstObject}
	w := NewSubgroupWriter(header)
	buf, err := w.AppendObject(nil, &SubgroupObject{ObjectID: 42, Payload: []byte("x")})
	require.NoError(t, err)
	buf, err = w.AppendObject(buf, &SubgroupObject{ObjectID: 43, Payload: []byte("y")})
	require.NoError(t, err)
	assert.Equal(t, uint64(42), w.Header.SubgroupID)

	parsed := &SubgroupHeader{TrackAlias: 1, GroupID: 2, SubgroupIDMode: SubgroupIDFirstObject}
	r := NewSubgroupReader(parsed, reader(buf))
	assert.Zero(t, parsed.SubgroupID, "not known until the first object arrives")
	_, err = r.Next()
	require.NoError(t, err)
	assert.Equal(t, uint64(42), parsed.SubgroupID)
}

// The status field exists only when the payload is empty, and only three
// values are defined.
func TestSubgroupObjectStatus(t *testing.T) {
	header := &SubgroupHeader{TrackAlias: 1, GroupID: 2}

	w := NewSubgroupWriter(header)
	buf, err := w.AppendObject(nil, &SubgroupObject{ObjectID: 1, Payload: []byte("data")})
	require.NoError(t, err)
	buf, err = w.AppendObject(buf, &SubgroupObject{ObjectID: 2, Status: ObjectStatusEndOfGroup})
	require.NoError(t, err)

	r := NewSubgroupReader(header, reader(buf))
	first, err := r.Next()
	require.NoError(t, err)
	assert.Equal(t, ObjectStatusNormal, first.Status)

	second, err := r.Next()
	require.NoError(t, err)
	assert.Equal(t, ObjectStatusEndOfGroup, second.Status)
	assert.Empty(t, second.Payload)

	_, err = NewSubgroupWriter(header).AppendObject(nil, &SubgroupObject{ObjectID: 1, Status: 0x2})
	assert.ErrorIs(t, err, errInvalidObjectStatus)

	_, err = NewSubgroupWriter(header).AppendObject(nil, &SubgroupObject{
		ObjectID: 1, Status: ObjectStatusEndOfTrack, Payload: []byte("x"),
	})
	assert.ErrorIs(t, err, errStatusWithPayload)

	// An undefined status on the wire is a PROTOCOL_VIOLATION, not something
	// to skip: Section 11.2.1.1 leaves no room for extension.
	bad := []byte{0x00, 0x00, 0x02} // delta 0, payload length 0, status 0x2
	_, err = NewSubgroupReader(header, reader(bad)).Next()
	assert.ErrorIs(t, err, errInvalidObjectStatus)
}

// The PROPERTIES bit is a property of the stream, not of an Object: every
// Object carries the block, and none may carry one if the header did not
// declare it.
func TestSubgroupObjectProperties(t *testing.T) {
	header := &SubgroupHeader{TrackAlias: 1, GroupID: 2, HasProperties: true}
	props := KVPList{{Type: PropertyPriorObjectIDGap, ValueVarInt: 3}}

	w := NewSubgroupWriter(header)
	buf, err := w.AppendObject(nil, &SubgroupObject{ObjectID: 1, Properties: props, Payload: []byte("a")})
	require.NoError(t, err)
	// An Object with no properties still writes an empty block.
	buf, err = w.AppendObject(buf, &SubgroupObject{ObjectID: 2, Payload: []byte("b")})
	require.NoError(t, err)

	r := NewSubgroupReader(header, reader(buf))
	first, err := r.Next()
	require.NoError(t, err)
	assert.Equal(t, props, first.Properties)
	second, err := r.Next()
	require.NoError(t, err)
	assert.Empty(t, second.Properties)

	plain := &SubgroupHeader{TrackAlias: 1, GroupID: 2}
	_, err = NewSubgroupWriter(plain).AppendObject(nil, &SubgroupObject{ObjectID: 1, Properties: props})
	assert.ErrorIs(t, err, errPropertiesNotDeclared)

	_, err = NewSubgroupWriter(header).AppendObject(nil, &SubgroupObject{
		ObjectID: 1, Properties: props, Status: ObjectStatusEndOfGroup,
	})
	assert.ErrorIs(t, err, errPropertiesOnNonNormalObject)
}

// A Mandatory Track Property arriving as an Object Property makes the track
// malformed, so the parser refuses it rather than passing it on.
func TestSubgroupObjectRejectsTrackScopedProperty(t *testing.T) {
	header := &SubgroupHeader{TrackAlias: 1, GroupID: 2, HasProperties: true}
	block := KVPList{{Type: 0x4000, ValueVarInt: 1}}.appendLength(nil)

	buf := append([]byte{0x00}, block...) // object ID delta 0, then properties
	buf = append(buf, 0x01, byte('x'))    // payload length 1, payload
	_, err := NewSubgroupReader(header, reader(buf)).Next()
	assert.ErrorIs(t, err, propertyScopeError{propertyType: 0x4000, scope: PropertyScopeObject})
}

func TestSubgroupObjectIDMustIncrease(t *testing.T) {
	header := &SubgroupHeader{TrackAlias: 1, GroupID: 2}
	w := NewSubgroupWriter(header)
	buf, err := w.AppendObject(nil, &SubgroupObject{ObjectID: 5, Payload: []byte("a")})
	require.NoError(t, err)
	_, err = w.AppendObject(buf, &SubgroupObject{ObjectID: 5, Payload: []byte("b")})
	assert.ErrorIs(t, err, errObjectIDNotIncreasing)
}

func TestSubgroupObjectIDOverflow(t *testing.T) {
	header := &SubgroupHeader{TrackAlias: 1, GroupID: 2}
	// First object ID 2^62-1, then a delta that pushes past 2^64-1.
	var buf []byte
	buf = vi64.Append(buf, 1<<62-1)
	buf = append(buf, 0x01, byte('a'))
	buf = vi64.Append(buf, maxUint64)
	buf = append(buf, 0x01, byte('b'))

	r := NewSubgroupReader(header, reader(buf))
	_, err := r.Next()
	require.NoError(t, err)
	_, err = r.Next()
	assert.ErrorIs(t, err, errObjectIDOverflow)
}

// A stream that ends part-way through an Object is a PROTOCOL_VIOLATION; one
// that ends between Objects is the end of the subgroup.
func TestSubgroupObjectTruncated(t *testing.T) {
	header := &SubgroupHeader{TrackAlias: 1, GroupID: 2, HasProperties: true}
	w := NewSubgroupWriter(header)
	full, err := w.AppendObject(nil, &SubgroupObject{
		ObjectID:   9,
		Properties: KVPList{{Type: PropertyPriorObjectIDGap, ValueVarInt: 1}},
		Payload:    []byte("payload"),
	})
	require.NoError(t, err)

	for n := 1; n < len(full); n++ {
		_, err := NewSubgroupReader(header, reader(full[:n])).Next()
		assert.ErrorIs(t, err, io.ErrUnexpectedEOF, "truncated to %d bytes", n)
	}
}
