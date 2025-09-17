package wire

import (
	"fmt"
	"io"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestUnannounceMessageAppend(t *testing.T) {
	cases := []struct {
		uam    PublishNamespaceDoneMessage
		buf    []byte
		expect []byte
	}{
		{
			uam: PublishNamespaceDoneMessage{
				TrackNamespace: []string{""},
			},
			buf: []byte{},
			expect: []byte{
				0x01, 0x00,
			},
		},
		{
			uam: PublishNamespaceDoneMessage{
				TrackNamespace: []string{"tracknamespace"},
			},
			buf:    []byte{0x0a, 0x0b},
			expect: []byte{0x0a, 0x0b, 0x01, 0x0e, 't', 'r', 'a', 'c', 'k', 'n', 'a', 'm', 'e', 's', 'p', 'a', 'c', 'e'},
		},
	}
	for i, tc := range cases {
		t.Run(fmt.Sprintf("%v", i), func(t *testing.T) {
			res := tc.uam.Append(tc.buf)
			assert.Equal(t, tc.expect, res)
		})
	}
}

func TestParseUnannounceMessage(t *testing.T) {
	cases := []struct {
		data   []byte
		expect *PublishNamespaceDoneMessage
		err    error
	}{
		{
			data:   nil,
			expect: &PublishNamespaceDoneMessage{},
			err:    io.EOF,
		},
		{
			data: append([]byte{0x01, 0x0E}, "tracknamespace"...),
			expect: &PublishNamespaceDoneMessage{
				TrackNamespace: []string{"tracknamespace"},
			},
			err: nil,
		},
		{
			data: append([]byte{0x01, 0x05}, "tracknamespace"...),
			expect: &PublishNamespaceDoneMessage{
				TrackNamespace: []string{"track"},
			},
			err: nil,
		},
		{
			data: append([]byte{0x01, 0x0F}, "tracknamespace"...),
			expect: &PublishNamespaceDoneMessage{
				TrackNamespace: []string{},
			},
			err: errLengthMismatch,
		},
	}
	for i, tc := range cases {
		t.Run(fmt.Sprintf("%v", i), func(t *testing.T) {
			res := &PublishNamespaceDoneMessage{}
			err := res.parse(CurrentVersion, tc.data)
			if tc.err != nil {
				assert.Equal(t, tc.err, err)
				assert.Equal(t, tc.expect, res)
				return
			}
			assert.NoError(t, err)
			assert.Equal(t, tc.expect, res)
		})
	}
}
