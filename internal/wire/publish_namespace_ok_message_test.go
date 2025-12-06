package wire

import (
	"fmt"
	"io"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestPublishNamespaceOkMessageAppend(t *testing.T) {
	cases := []struct {
		aom    PublishNamespaceOkMessage
		buf    []byte
		expect []byte
	}{
		{
			aom: PublishNamespaceOkMessage{
				RequestID: 1,
			},
			buf: []byte{},
			expect: []byte{
				0x01,
			},
		},
		{
			aom: PublishNamespaceOkMessage{
				RequestID: 1,
			},
			buf:    []byte{0x0a, 0x0b},
			expect: []byte{0x0a, 0x0b, 0x01},
		},
	}
	for i, tc := range cases {
		t.Run(fmt.Sprintf("%v", i), func(t *testing.T) {
			res := tc.aom.Append(tc.buf)
			assert.Equal(t, tc.expect, res)
		})
	}
}

func TestParsePublishNamespaceOkMessage(t *testing.T) {
	cases := []struct {
		data   []byte
		expect *PublishNamespaceOkMessage
		err    error
	}{
		{
			data:   nil,
			expect: &PublishNamespaceOkMessage{},
			err:    io.EOF,
		},
		{
			data: []byte{0x01},
			expect: &PublishNamespaceOkMessage{
				RequestID: 1,
			},
			err: nil,
		},
		{
			data: []byte{0x01},
			expect: &PublishNamespaceOkMessage{
				RequestID: 1,
			},
			err: nil,
		},
		{
			data: []byte{},
			expect: &PublishNamespaceOkMessage{
				RequestID: 0,
			},
			err: io.EOF,
		},
	}
	for i, tc := range cases {
		t.Run(fmt.Sprintf("%v", i), func(t *testing.T) {
			res := &PublishNamespaceOkMessage{}
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
