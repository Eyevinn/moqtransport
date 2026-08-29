package wire2

import (
	"io"
	"testing"

	"github.com/Eyevinn/locmaf/vi64"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// writeFetch runs a sequence through the writer and reads it back, which is
// the only way to check the delta encoding: every record's meaning depends on
// the one before it.
func writeFetch(t *testing.T, order GroupOrder, objects []*FetchObject) []*FetchObject {
	t.Helper()
	w := NewFetchWriter(order)
	var buf []byte
	var err error
	for _, obj := range objects {
		buf, err = w.AppendObject(buf, obj)
		require.NoError(t, err)
	}

	r := NewFetchReader(reader(buf), order)
	got := make([]*FetchObject, 0, len(objects))
	for {
		obj, err := r.Next()
		if err == io.EOF {
			return got
		}
		require.NoError(t, err)
		got = append(got, obj)
	}
}

func TestFetchObjectRoundTrip(t *testing.T) {
	objects := []*FetchObject{
		{GroupID: 3, SubgroupID: 0, ObjectID: 0, Priority: 128, Payload: []byte("a")},
		{GroupID: 3, SubgroupID: 0, ObjectID: 1, Priority: 128, Payload: []byte("b")},
		{GroupID: 3, SubgroupID: 0, ObjectID: 5, Priority: 128, Payload: []byte("gap")},
		{GroupID: 4, SubgroupID: 2, ObjectID: 0, Priority: 7, Payload: []byte("new group")},
		{
			GroupID: 4, SubgroupID: 2, ObjectID: 1, Priority: 7,
			Properties: KVPList{{Type: PropertyPriorObjectIDGap, ValueVarInt: 3}},
			Payload:    []byte("with properties"),
		},
		{GroupID: 5, ObjectID: 0, Priority: 7, Datagram: true, Payload: []byte("datagram object")},
	}

	got := writeFetch(t, GroupOrderAscending, objects)
	require.Len(t, got, len(objects))
	for i, want := range objects {
		assert.Equal(t, want.GroupID, got[i].GroupID, "object %d group", i)
		assert.Equal(t, want.SubgroupID, got[i].SubgroupID, "object %d subgroup", i)
		assert.Equal(t, want.ObjectID, got[i].ObjectID, "object %d id", i)
		assert.Equal(t, want.Priority, got[i].Priority, "object %d priority", i)
		assert.Equal(t, want.Datagram, got[i].Datagram, "object %d datagram", i)
		assert.Equal(t, string(want.Payload), string(got[i].Payload), "object %d payload", i)
		assert.Equal(t, want.Properties, got[i].Properties, "object %d properties", i)
	}
}

// A Group ID Delta always advances, so it is the *absence* of the delta that
// keeps a record in the Group before it. Get that backwards and every Object
// after the first lands in its own Group.
func TestFetchObjectSameGroupOmitsDelta(t *testing.T) {
	w := NewFetchWriter(GroupOrderAscending)
	buf, err := w.AppendObject(nil, &FetchObject{GroupID: 9, ObjectID: 0, Priority: 1, Payload: []byte("a")})
	require.NoError(t, err)
	firstLen := len(buf)
	buf, err = w.AppendObject(buf, &FetchObject{GroupID: 9, ObjectID: 1, Priority: 1, Payload: []byte("b")})
	require.NoError(t, err)

	flags, _, err := vi64.Parse(buf[firstLen:])
	require.NoError(t, err)
	assert.Zero(t, flags&fetchFlagGroupIDDelta, "staying in a group omits the group delta")
}

func TestFetchObjectDescendingGroups(t *testing.T) {
	objects := []*FetchObject{
		{GroupID: 10, ObjectID: 0, Priority: 1, Payload: []byte("a")},
		{GroupID: 10, ObjectID: 1, Priority: 1, Payload: []byte("b")},
		{GroupID: 9, ObjectID: 0, Priority: 1, Payload: []byte("c")},
		{GroupID: 4, ObjectID: 0, Priority: 1, Payload: []byte("d")},
	}
	got := writeFetch(t, GroupOrderDescending, objects)
	require.Len(t, got, len(objects))
	for i, want := range objects {
		assert.Equal(t, want.GroupID, got[i].GroupID, "object %d", i)
		assert.Equal(t, want.ObjectID, got[i].ObjectID, "object %d", i)
	}

	// Groups that do not move in the declared direction cannot be encoded.
	w := NewFetchWriter(GroupOrderDescending)
	buf, err := w.AppendObject(nil, &FetchObject{GroupID: 5, ObjectID: 0, Priority: 1})
	require.NoError(t, err)
	_, err = w.AppendObject(buf, &FetchObject{GroupID: 6, ObjectID: 0, Priority: 1})
	assert.ErrorIs(t, err, errGroupIDNotDescending)

	w = NewFetchWriter(GroupOrderAscending)
	buf, err = w.AppendObject(nil, &FetchObject{GroupID: 5, ObjectID: 0, Priority: 1})
	require.NoError(t, err)
	_, err = w.AppendObject(buf, &FetchObject{GroupID: 4, ObjectID: 0, Priority: 1})
	assert.ErrorIs(t, err, errGroupIDNotAscending)
}

// An End of Range indicator stands in for Objects that were not serialized. Its
// Location is absolute, so it can point inside the Group it follows.
func TestFetchObjectEndOfRange(t *testing.T) {
	objects := []*FetchObject{
		{GroupID: 7, ObjectID: 0, Priority: 3, Payload: []byte("a")},
		{GroupID: 7, ObjectID: 9, EndOfRange: EndOfRangeNonExistent},
		{GroupID: 7, ObjectID: 12, Priority: 3, Payload: []byte("b")},
		{GroupID: 8, ObjectID: 4, EndOfRange: EndOfRangeUnknown},
	}
	got := writeFetch(t, GroupOrderAscending, objects)
	require.Len(t, got, len(objects))

	assert.Equal(t, EndOfRangeNonExistent, got[1].EndOfRange)
	assert.Equal(t, uint64(7), got[1].GroupID)
	assert.Equal(t, uint64(9), got[1].ObjectID)
	assert.Empty(t, got[1].Payload)

	assert.Equal(t, EndOfRangeNone, got[2].EndOfRange)
	assert.Equal(t, uint64(7), got[2].GroupID)
	assert.Equal(t, uint64(12), got[2].ObjectID)

	assert.Equal(t, EndOfRangeUnknown, got[3].EndOfRange)
	assert.Equal(t, uint64(8), got[3].GroupID)
	assert.Equal(t, uint64(4), got[3].ObjectID)
}

// Below 128 the Serialization Flags are a bitfield; above it only the two End
// of Range values exist and everything else closes the session.
func TestFetchObjectInvalidSerializationFlags(t *testing.T) {
	for _, flags := range []uint64{0x80, 0x8B, 0x8D, 0x100, 0x10D, 0x4000} {
		buf := vi64.Append(nil, flags)
		buf = append(buf, 0x01, 0x02, 0x00)
		_, err := NewFetchReader(reader(buf), GroupOrderAscending).Next()
		assert.ErrorIs(t, err, errInvalidSerializationFlags, "flags %#x", flags)
	}

	_, err := NewFetchWriter(GroupOrderAscending).AppendObject(nil, &FetchObject{EndOfRange: 0x99})
	assert.ErrorIs(t, err, errInvalidSerializationFlags)
}

// Nothing on the first record may refer to a prior Object, because there is
// none to refer to.
func TestFetchObjectFirstRecordNeedsAbsoluteLocation(t *testing.T) {
	for _, flags := range []uint64{
		0x00,                   // neither delta present
		fetchFlagGroupIDDelta,  // no object ID delta
		fetchFlagObjectIDDelta, // no group ID delta
	} {
		buf := vi64.Append(nil, flags)
		buf = append(buf, 0x01, 0x02, 0x03, 0x00)
		_, err := NewFetchReader(reader(buf), GroupOrderAscending).Next()
		assert.ErrorIs(t, err, errFetchObjectNeedsPrior, "flags %#x", flags)
	}

	// Inheriting the prior Subgroup ID or Priority is the same problem.
	both := uint64(fetchFlagGroupIDDelta | fetchFlagObjectIDDelta)
	for _, flags := range []uint64{
		both | uint64(FetchSubgroupPrior) | fetchFlagPriority,
		both, // priority flag clear, so it would inherit
	} {
		buf := vi64.Append(nil, flags)
		buf = append(buf, 0x01, 0x02, 0x80, 0x00)
		_, err := NewFetchReader(reader(buf), GroupOrderAscending).Next()
		assert.ErrorIs(t, err, errFetchObjectNeedsPrior, "flags %#x", flags)
	}
}

// The compact forms are not what this writer emits, but a peer may send them,
// so the reader has to resolve each one.
func TestFetchObjectReaderResolvesCompactForms(t *testing.T) {
	both := uint64(fetchFlagGroupIDDelta | fetchFlagObjectIDDelta | fetchFlagPriority)

	// Group 3, subgroup 0, object 8, priority 5, payload "a".
	buf := vi64.Append(nil, both)
	buf = append(buf, 0x03, 0x08, 0x05, 0x01, byte('a'))
	// Object ID delta absent: prior + 1. Subgroup ID from prior. Priority from
	// prior. No payload.
	buf = vi64.Append(buf, uint64(FetchSubgroupPrior))
	buf = append(buf, 0x00)
	// Subgroup ID prior + 1, object ID delta 2 within the same group: prior + 2.
	buf = vi64.Append(buf, uint64(FetchSubgroupPriorPlusOne)|fetchFlagObjectIDDelta)
	buf = append(buf, 0x02, 0x00)

	r := NewFetchReader(reader(buf), GroupOrderAscending)
	first, err := r.Next()
	require.NoError(t, err)
	assert.Equal(t, uint64(3), first.GroupID)
	assert.Equal(t, uint64(8), first.ObjectID)
	assert.Equal(t, uint8(5), first.Priority)

	second, err := r.Next()
	require.NoError(t, err)
	assert.Equal(t, uint64(3), second.GroupID, "no group delta means the prior group")
	assert.Equal(t, uint64(9), second.ObjectID, "no object delta means prior plus one")
	assert.Equal(t, uint64(0), second.SubgroupID)
	assert.Equal(t, uint8(5), second.Priority, "priority carries over")

	third, err := r.Next()
	require.NoError(t, err)
	assert.Equal(t, uint64(11), third.ObjectID, "an in-group delta is added as-is")
	assert.Equal(t, uint64(1), third.SubgroupID, "prior subgroup plus one")
}

// After an End of Range indicator, the Location advances but the Subgroup ID
// and Priority still come from the last real Object (Section 11.4.4.2).
func TestFetchObjectPriorAcrossEndOfRange(t *testing.T) {
	both := uint64(fetchFlagGroupIDDelta | fetchFlagObjectIDDelta | fetchFlagPriority)

	// Group 2, subgroup 6, object 0, priority 9.
	buf := vi64.Append(nil, both|uint64(FetchSubgroupExplicit))
	buf = append(buf, 0x02, 0x06, 0x00, 0x09, 0x00)
	// End of Non-Existent Range at {2, 20}.
	buf = vi64.Append(buf, uint64(EndOfRangeNonExistent))
	buf = append(buf, 0x02, 0x14, 0x00)
	// Then an Object inheriting everything it can.
	buf = vi64.Append(buf, uint64(FetchSubgroupPrior))
	buf = append(buf, 0x00)

	r := NewFetchReader(reader(buf), GroupOrderAscending)
	_, err := r.Next()
	require.NoError(t, err)
	marker, err := r.Next()
	require.NoError(t, err)
	require.Equal(t, uint64(20), marker.ObjectID)

	after, err := r.Next()
	require.NoError(t, err)
	assert.Equal(t, uint64(2), after.GroupID)
	assert.Equal(t, uint64(21), after.ObjectID, "the indicator's location is the prior location")
	assert.Equal(t, uint64(6), after.SubgroupID, "but the subgroup comes from the last real object")
	assert.Equal(t, uint8(9), after.Priority, "and so does the priority")
}

func TestFetchObjectEndOfRangeRejectsPayload(t *testing.T) {
	_, err := NewFetchWriter(GroupOrderAscending).AppendObject(nil, &FetchObject{
		GroupID: 1, ObjectID: 2, EndOfRange: EndOfRangeUnknown, Payload: []byte("x"),
	})
	assert.ErrorIs(t, err, errEndOfRangeWithPayload)

	buf := vi64.Append(nil, uint64(EndOfRangeUnknown))
	buf = append(buf, 0x01, 0x02, 0x01, byte('x'))
	_, err = NewFetchReader(reader(buf), GroupOrderAscending).Next()
	assert.ErrorIs(t, err, errEndOfRangeWithPayload)
}

func TestFetchObjectTruncated(t *testing.T) {
	w := NewFetchWriter(GroupOrderAscending)
	full, err := w.AppendObject(nil, &FetchObject{
		GroupID: 300, SubgroupID: 400, ObjectID: 500, Priority: 3,
		Properties: KVPList{{Type: PropertyPriorObjectIDGap, ValueVarInt: 1}},
		Payload:    []byte("payload"),
	})
	require.NoError(t, err)

	for n := 1; n < len(full); n++ {
		_, err := NewFetchReader(reader(full[:n]), GroupOrderAscending).Next()
		assert.ErrorIs(t, err, io.ErrUnexpectedEOF, "truncated to %d bytes", n)
	}
}
