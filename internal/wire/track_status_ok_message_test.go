package wire

import (
	"fmt"
	"io"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestTrackStatusOkMessageAppend(t *testing.T) {
	cases := []struct {
		msg    TrackStatusOkMessage
		buf    []byte
		expect []byte
	}{
		{
			msg: TrackStatusOkMessage{
				RequestID:     0,
				TrackAlias:    0,
				Expires:       0,
				GroupOrder:    0,
				ContentExists: false,
				Parameters:    KVPList{},
			},
			buf: []byte{},
			expect: []byte{
				0x00, // RequestID
				0x00, // TrackAlias
				0x00, // Expires
				0x00, // GroupOrder
				0x00, // ContentExists = false
				0x00, // Number of Parameters
			},
		},
		{
			msg: TrackStatusOkMessage{
				RequestID:     1,
				TrackAlias:    0,
				Expires:       1000,
				GroupOrder:    1,
				ContentExists: true,
				LargestLocation: Location{
					Group:  3,
					Object: 4,
				},
				Parameters: KVPList{},
			},
			buf: []byte{0x0a, 0x0b},
			expect: []byte{
				0x0a, 0x0b,
				0x01,       // RequestID
				0x00,       // TrackAlias
				0x43, 0xe8, // Expires (1000 as varint)
				0x01, // GroupOrder
				0x01, // ContentExists = true
				0x03, // LargestLocation.Group
				0x04, // LargestLocation.Object
				0x00, // Number of Parameters
			},
		},
	}
	for i, tc := range cases {
		t.Run(fmt.Sprintf("%v", i), func(t *testing.T) {
			res := tc.msg.Append(tc.buf)
			assert.Equal(t, tc.expect, res)
		})
	}
}

func TestParseTrackStatusOkMessage(t *testing.T) {
	cases := []struct {
		data   []byte
		expect *TrackStatusOkMessage
		err    error
	}{
		{
			data:   nil,
			expect: &TrackStatusOkMessage{},
			err:    io.EOF,
		},
		{
			data:   []byte{},
			expect: &TrackStatusOkMessage{},
			err:    io.EOF,
		},
		{
			data: []byte{
				0x01, // RequestID
				0x00, // TrackAlias
				0x40, 0x64, // Expires (100ms as 2-byte varint)
				0x01, // GroupOrder
				0x01, // ContentExists = true
				0x03, // LargestLocation.Group
				0x04, // LargestLocation.Object
				0x00, // Number of Parameters
			},
			expect: &TrackStatusOkMessage{
				RequestID:     1,
				TrackAlias:    0,
				Expires:       100 * time.Millisecond,
				GroupOrder:    1,
				ContentExists: true,
				LargestLocation: Location{
					Group:  3,
					Object: 4,
				},
				Parameters: KVPList{},
			},
			err: nil,
		},
	}
	for i, tc := range cases {
		t.Run(fmt.Sprintf("%v", i), func(t *testing.T) {
			res := &TrackStatusOkMessage{}
			err := res.parse(CurrentVersion, tc.data)
			assert.Equal(t, tc.expect, res)
			if tc.err != nil {
				assert.Equal(t, tc.err, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}
