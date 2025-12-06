package wire

import (
	"fmt"
	"io"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestTrackStatusOkMessageAppend(t *testing.T) {
	cases := []struct {
		tsm    TrackStatusOkMessage
		buf    []byte
		expect []byte
	}{
		{
			tsm: TrackStatusOkMessage{
				RequestID:  0,
				StatusCode: 0,
				LargestLocation: Location{
					Group:  0,
					Object: 0,
				},
			},
			buf:    []byte{},
			expect: []byte{0x00, 0x00, 0x00, 0x00, 0x00},
		},
		{
			tsm: TrackStatusOkMessage{
				RequestID:  1,
				StatusCode: 2,
				LargestLocation: Location{
					Group:  1,
					Object: 2,
				},
				Parameters: KVPList{},
			},
			buf:    []byte{0x0a, 0x0b},
			expect: []byte{0x0a, 0x0b, 0x01, 0x02, 0x01, 0x02, 0x00},
		},
	}
	for i, tc := range cases {
		t.Run(fmt.Sprintf("%v", i), func(t *testing.T) {
			res := tc.tsm.Append(tc.buf)
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
			data: []byte{0x01, 0x02, 0x03, 0x04, 0x00},
			expect: &TrackStatusOkMessage{
				RequestID:  1,
				StatusCode: 2,
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
