package wire

import (
	"fmt"
	"io"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestTrackStatusMessageAppend(t *testing.T) {
	cases := []struct {
		msg    TrackStatusMessage
		buf    []byte
		expect []byte
	}{
		{
			msg: TrackStatusMessage{
				RequestID:          0,
				TrackNamespace:     []string{"ns"},
				TrackName:          []byte("track"),
				SubscriberPriority: 0,
				GroupOrder:         0,
				Forward:            1,
				FilterType:         FilterTypeLatestObject,
				Parameters:         KVPList{},
			},
			buf: []byte{},
			expect: []byte{
				0x00,                                     // RequestID
				0x01, 0x02, 'n', 's',                     // TrackNamespace (tuple: 1 element, len 2, "ns")
				0x05, 't', 'r', 'a', 'c', 'k',           // TrackName
				0x00,                                     // SubscriberPriority
				0x00,                                     // GroupOrder
				0x01,                                     // Forward
				0x02,                                     // FilterType (LatestObject)
				0x00,                                     // Number of Parameters
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

func TestParseTrackStatusMessage(t *testing.T) {
	cases := []struct {
		data   []byte
		expect *TrackStatusMessage
		err    error
	}{
		{
			data:   nil,
			expect: &TrackStatusMessage{},
			err:    io.EOF,
		},
		{
			data: []byte{
				0x00,                                     // RequestID
				0x01, 0x02, 'n', 's',                     // TrackNamespace
				0x05, 't', 'r', 'a', 'c', 'k',           // TrackName
				0x00,                                     // SubscriberPriority
				0x00,                                     // GroupOrder
				0x01,                                     // Forward
				0x02,                                     // FilterType (LatestObject)
				0x00,                                     // Number of Parameters
			},
			expect: &TrackStatusMessage{
				RequestID:          0,
				TrackNamespace:     []string{"ns"},
				TrackName:          []byte("track"),
				SubscriberPriority: 0,
				GroupOrder:         0,
				Forward:            1,
				FilterType:         FilterTypeLatestObject,
				Parameters:         KVPList{},
			},
			err: nil,
		},
	}
	for i, tc := range cases {
		t.Run(fmt.Sprintf("%v", i), func(t *testing.T) {
			res := &TrackStatusMessage{}
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
