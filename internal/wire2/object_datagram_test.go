package wire2

import (
	"testing"

	"github.com/Eyevinn/locmaf/vi64"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestObjectDatagramRoundTrip(t *testing.T) {
	for _, d := range []*ObjectDatagram{
		{TrackAlias: 1, GroupID: 2, ObjectID: 3, Priority: 128, Payload: []byte("frame")},
		{TrackAlias: 1, GroupID: 2, ObjectID: 0, Priority: 4, Payload: []byte("zero object id")},
		{TrackAlias: 1, GroupID: 2, ObjectID: 3, DefaultPriority: true, Payload: []byte("inherited priority")},
		{TrackAlias: 1, GroupID: 2, ObjectID: 3, Priority: 1, EndOfGroup: true, Payload: []byte("last")},
		{
			TrackAlias: 1, GroupID: 2, ObjectID: 3, Priority: 1,
			Properties: KVPList{{Type: PropertyPriorObjectIDGap, ValueVarInt: 2}},
			Payload:    []byte("with properties"),
		},
		{TrackAlias: 1, GroupID: 2, ObjectID: 3, Priority: 1, Status: ObjectStatusEndOfTrack},
		// An empty payload is not a status: the payload runs to the end of the
		// datagram, so a zero-length one encodes on its own.
		{TrackAlias: 1, GroupID: 2, ObjectID: 3, Priority: 1},
	} {
		t.Run(d.String(), func(t *testing.T) {
			buf, err := AppendObjectDatagram(nil, d)
			require.NoError(t, err)

			got, err := ParseObjectDatagram(buf)
			require.NoError(t, err)
			assert.Equal(t, d.TrackAlias, got.TrackAlias)
			assert.Equal(t, d.GroupID, got.GroupID)
			assert.Equal(t, d.ObjectID, got.ObjectID)
			assert.Equal(t, d.Priority, got.Priority)
			assert.Equal(t, d.DefaultPriority, got.DefaultPriority)
			assert.Equal(t, d.EndOfGroup, got.EndOfGroup)
			assert.Equal(t, d.Status, got.Status)
			assert.Equal(t, d.Properties, got.Properties)
			assert.Equal(t, string(d.Payload), string(got.Payload))
		})
	}
}

// The type byte says which fields are on the wire, so it has to reflect what
// was actually written.
func TestObjectDatagramTypeBits(t *testing.T) {
	for _, tc := range []struct {
		name     string
		datagram *ObjectDatagram
		want     uint64
	}{
		{"nothing set", &ObjectDatagram{ObjectID: 1}, 0x00},
		{"zero object ID", &ObjectDatagram{ObjectID: 0}, 0x04},
		{"end of group", &ObjectDatagram{ObjectID: 1, EndOfGroup: true}, 0x02},
		{"default priority", &ObjectDatagram{ObjectID: 1, DefaultPriority: true}, 0x08},
		{"status", &ObjectDatagram{ObjectID: 1, Status: ObjectStatusEndOfGroup}, 0x20},
		{"properties", &ObjectDatagram{ObjectID: 1, Properties: KVPList{{Type: 0x3E, ValueVarInt: 1}}}, 0x01},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, tc.datagram.datagramType())
		})
	}
}

// An Object status message cannot also signal end of group, which is what
// makes eight of the sixteen status types invalid.
func TestObjectDatagramStatusAndEndOfGroup(t *testing.T) {
	_, err := AppendObjectDatagram(nil, &ObjectDatagram{
		ObjectID: 1, EndOfGroup: true, Status: ObjectStatusEndOfTrack,
	})
	assert.ErrorIs(t, err, errDatagramStatusEndOfGroup)

	for _, invalid := range []uint64{0x22, 0x23, 0x26, 0x27, 0x2A, 0x2B, 0x2E, 0x2F} {
		buf := vi64.Append(nil, invalid)
		buf = append(buf, 0x01, 0x02, 0x03, 0x04)
		_, err := ParseObjectDatagram(buf)
		assert.ErrorIs(t, err, errDatagramStatusEndOfGroup, "type %#x", invalid)
	}
}

// Only the forms 0b00X0XXXX are datagrams. The SUBGROUP_HEADER types overlap
// numerically, so this is what keeps the two tables apart.
func TestObjectDatagramInvalidTypes(t *testing.T) {
	for _, invalid := range []uint64{0x10, 0x1F, 0x30, 0x40, 0x50, 0x5F, 0x80, 0x100, 0x2F00} {
		buf := vi64.Append(nil, invalid)
		buf = append(buf, 0x01, 0x02, 0x03, 0x04)
		_, err := ParseObjectDatagram(buf)
		assert.ErrorAs(t, err, &unknownDatagramTypeError{}, "type %#x", invalid)
	}
}

// The PROPERTIES bit exists to say there is something there, so declaring an
// empty block is a violation rather than a redundancy.
func TestObjectDatagramEmptyProperties(t *testing.T) {
	buf := vi64.Append(nil, 0x01)       // PROPERTIES
	buf = append(buf, 0x01, 0x02, 0x03) // alias, group, object
	buf = append(buf, 0x80)             // priority
	buf = append(buf, 0x00)             // properties length 0
	_, err := ParseObjectDatagram(buf)
	assert.ErrorIs(t, err, errEmptyDatagramProperties)
}

// Only Normal Objects can have Properties.
func TestObjectDatagramPropertiesOnStatus(t *testing.T) {
	props := KVPList{{Type: PropertyPriorObjectIDGap, ValueVarInt: 1}}
	_, err := AppendObjectDatagram(nil, &ObjectDatagram{
		ObjectID: 1, Status: ObjectStatusEndOfGroup, Properties: props,
	})
	assert.ErrorIs(t, err, errPropertiesOnNonNormalObject)

	buf := vi64.Append(nil, 0x21)       // STATUS | PROPERTIES
	buf = append(buf, 0x01, 0x02, 0x03) // alias, group, object
	buf = append(buf, 0x80)             // priority
	buf = append(buf, props.appendLength(nil)...)
	buf = vi64.Append(buf, uint64(ObjectStatusEndOfGroup))
	_, err = ParseObjectDatagram(buf)
	assert.ErrorIs(t, err, errPropertiesOnNonNormalObject)
}

// There is no payload length field: whatever follows the header is payload,
// however long the datagram happens to be.
func TestObjectDatagramPayloadRunsToEnd(t *testing.T) {
	buf, err := AppendObjectDatagram(nil, &ObjectDatagram{
		TrackAlias: 1, GroupID: 2, ObjectID: 3, Priority: 9, Payload: []byte("abc"),
	})
	require.NoError(t, err)

	got, err := ParseObjectDatagram(append(buf, "def"...))
	require.NoError(t, err)
	assert.Equal(t, "abcdef", string(got.Payload))
}

func TestObjectDatagramTruncated(t *testing.T) {
	full, err := AppendObjectDatagram(nil, &ObjectDatagram{
		TrackAlias: 300, GroupID: 400, ObjectID: 500, Priority: 9, Status: ObjectStatusEndOfGroup,
	})
	require.NoError(t, err)

	for n := 1; n < len(full); n++ {
		_, err := ParseObjectDatagram(full[:n])
		assert.Error(t, err, "truncated to %d bytes", n)
	}
}
