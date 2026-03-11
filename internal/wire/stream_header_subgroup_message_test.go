package wire

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestStreamHeaderSubgroupMessageAppend(t *testing.T) {
	cases := []struct {
		shgm   SubgroupHeaderMessage
		buf    []byte
		expect []byte
	}{
		{
			shgm: SubgroupHeaderMessage{
				TrackAlias:        0,
				GroupID:           0,
				SubgroupID:        0,
				PublisherPriority: 0,
			},
			buf:    []byte{},
			expect: []byte{byte(StreamTypeSubgroupSIDExt), 0x00, 0x00, 0x00, 0x00},
		},
		{
			shgm: SubgroupHeaderMessage{
				TrackAlias:        1,
				GroupID:           2,
				SubgroupID:        3,
				PublisherPriority: 4,
			},
			buf:    []byte{},
			expect: []byte{byte(StreamTypeSubgroupSIDExt), 0x01, 0x02, 0x03, 0x04},
		},
		{
			shgm: SubgroupHeaderMessage{
				TrackAlias:        1,
				GroupID:           2,
				SubgroupID:        3,
				PublisherPriority: 4,
			},
			buf:    []byte{0x0a, 0x0b},
			expect: []byte{0x0a, 0x0b, byte(StreamTypeSubgroupSIDExt), 0x01, 0x02, 0x03, 0x04},
		},
		{
			shgm: SubgroupHeaderMessage{
				TrackAlias:        1,
				GroupID:           2,
				SubgroupID:        3,
				PublisherPriority: 4,
				EndOfGroup:        true,
			},
			buf:    []byte{},
			expect: []byte{byte(StreamTypeSubgroupSIDExtEOG), 0x01, 0x02, 0x03, 0x04},
		},
		{
			shgm: SubgroupHeaderMessage{
				TrackAlias:        0,
				GroupID:           0,
				SubgroupID:        0,
				PublisherPriority: 0,
				EndOfGroup:        true,
			},
			buf:    []byte{},
			expect: []byte{byte(StreamTypeSubgroupSIDExtEOG), 0x00, 0x00, 0x00, 0x00},
		},
	}
	for i, tc := range cases {
		t.Run(fmt.Sprintf("%v", i), func(t *testing.T) {
			res := tc.shgm.Append(tc.buf)
			assert.Equal(t, tc.expect, res)
		})
	}
}

func TestSubgroupStreamTypeHelpers(t *testing.T) {
	// All non-EOG subgroup types
	for _, st := range []StreamType{0x10, 0x11, 0x12, 0x13, 0x14, 0x15} {
		assert.True(t, isSubgroupStreamType(st), "expected 0x%x to be subgroup", st)
		assert.False(t, subgroupContainsEndOfGroup(st), "expected 0x%x to not be EOG", st)
	}
	// All EOG subgroup types
	for _, st := range []StreamType{0x18, 0x19, 0x1A, 0x1B, 0x1C, 0x1D} {
		assert.True(t, isSubgroupStreamType(st), "expected 0x%x to be subgroup", st)
		assert.True(t, subgroupContainsEndOfGroup(st), "expected 0x%x to be EOG", st)
	}
	// Gap between non-EOG and EOG (0x16, 0x17) should not be valid
	for _, st := range []StreamType{0x16, 0x17} {
		assert.False(t, isSubgroupStreamType(st), "expected 0x%x to not be subgroup", st)
	}
	// Values outside the range
	for _, st := range []StreamType{0x00, 0x05, 0x08, 0x0d, 0x0f, 0x1E, 0xFF} {
		assert.False(t, isSubgroupStreamType(st), "expected 0x%x to not be subgroup", st)
	}
	// Explicit SID types
	assert.True(t, subgroupHasExplicitSID(StreamTypeSubgroupSIDNoExt))
	assert.True(t, subgroupHasExplicitSID(StreamTypeSubgroupSIDExt))
	assert.True(t, subgroupHasExplicitSID(StreamTypeSubgroupSIDNoExtEOG))
	assert.True(t, subgroupHasExplicitSID(StreamTypeSubgroupSIDExtEOG))
	assert.False(t, subgroupHasExplicitSID(StreamTypeSubgroupZeroSIDNoExt))
	assert.False(t, subgroupHasExplicitSID(StreamTypeSubgroupNoSIDNoExt))

	// SID from first object ID types
	assert.True(t, subgroupSIDIsFirstObjectID(StreamTypeSubgroupNoSIDNoExt))
	assert.True(t, subgroupSIDIsFirstObjectID(StreamTypeSubgroupNoSIDExt))
	assert.True(t, subgroupSIDIsFirstObjectID(StreamTypeSubgroupNoSIDNoExtEOG))
	assert.True(t, subgroupSIDIsFirstObjectID(StreamTypeSubgroupNoSIDExtEOG))
	assert.False(t, subgroupSIDIsFirstObjectID(StreamTypeSubgroupSIDExt))
	assert.False(t, subgroupSIDIsFirstObjectID(StreamTypeSubgroupZeroSIDNoExt))
}

func TestNewObjectStreamParserEndOfGroup(t *testing.T) {
	// Build a stream with EOG subgroup header: type 0x1D (SID+Ext+EOG),
	// followed by TrackAlias=1, GroupID=2, SubgroupID=3, Priority=4
	data := []byte{byte(StreamTypeSubgroupSIDExtEOG), 0x01, 0x02, 0x03, 0x04}
	parser, err := NewObjectStreamParser(bytes.NewReader(data), 0, nil)
	assert.NoError(t, err)
	assert.True(t, parser.EndOfGroup)
	assert.Equal(t, uint64(1), parser.Identifier())
	assert.Equal(t, uint64(2), parser.GroupID)
	assert.Equal(t, uint64(3), parser.SubgroupID)
	assert.Equal(t, uint8(4), parser.PublisherPriority)

	// Non-EOG variant
	data2 := []byte{byte(StreamTypeSubgroupSIDExt), 0x01, 0x02, 0x03, 0x04}
	parser2, err := NewObjectStreamParser(bytes.NewReader(data2), 0, nil)
	assert.NoError(t, err)
	assert.False(t, parser2.EndOfGroup)
}

func TestParseStreamHeaderSubgroupMessage(t *testing.T) {
	cases := []struct {
		data   []byte
		expect *SubgroupHeaderMessage
		err    error
	}{
		{
			data:   nil,
			expect: &SubgroupHeaderMessage{},
			err:    io.EOF,
		},
		{
			data:   []byte{},
			expect: &SubgroupHeaderMessage{},
			err:    io.EOF,
		},
		{
			data: []byte{0x01, 0x02, 0x03, 0x04},
			expect: &SubgroupHeaderMessage{
				TrackAlias:        1,
				GroupID:           2,
				SubgroupID:        3,
				PublisherPriority: 4,
			},
			err: nil,
		},
	}
	for i, tc := range cases {
		t.Run(fmt.Sprintf("%v", i), func(t *testing.T) {
			reader := bufio.NewReader(bytes.NewReader(tc.data))
			res := &SubgroupHeaderMessage{}
			err := res.parse(reader, true)
			assert.Equal(t, tc.expect, res)
			if tc.err != nil {
				assert.Equal(t, tc.err, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}
