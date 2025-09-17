package wire

import (
	"fmt"
	"io"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestAnnounceErrorMessageAppend(t *testing.T) {
	cases := []struct {
		aem    PublishNamespaceErrorMessage
		buf    []byte
		expect []byte
	}{
		{
			aem: PublishNamespaceErrorMessage{
				RequestID:    0,
				ErrorCode:    0,
				ReasonPhrase: "",
			},
			buf: []byte{},
			expect: []byte{
				0x00, 0x00, 0x00,
			},
		},
		{
			aem: PublishNamespaceErrorMessage{
				RequestID:    1,
				ErrorCode:    1,
				ReasonPhrase: "reason",
			},
			buf:    []byte{},
			expect: append([]byte{0x01, 0x01, 0x06}, "reason"...),
		},
		{
			aem: PublishNamespaceErrorMessage{
				RequestID:    1,
				ErrorCode:    1,
				ReasonPhrase: "reason",
			},
			buf:    []byte{0x0a, 0x0b, 0x0c, 0x0d},
			expect: append([]byte{0x0a, 0x0b, 0x0c, 0x0d, 0x01, 0x01, 0x06}, "reason"...),
		},
	}
	for i, tc := range cases {
		t.Run(fmt.Sprintf("%v", i), func(t *testing.T) {
			res := tc.aem.Append(tc.buf)
			assert.Equal(t, tc.expect, res)
		})
	}
}

func TestParseAnnounceErrorMessage(t *testing.T) {
	cases := []struct {
		data   []byte
		expect *PublishNamespaceErrorMessage
		err    error
	}{
		{
			data:   nil,
			expect: &PublishNamespaceErrorMessage{},
			err:    io.EOF,
		},
		{
			data: []byte{0x01, 0x03, 0x03, 'e', 'r'},
			expect: &PublishNamespaceErrorMessage{
				RequestID:    1,
				ErrorCode:    3,
				ReasonPhrase: "",
			},
			err: io.ErrUnexpectedEOF,
		},
		{
			data: append([]byte{0x00, 0x01, 0x0d}, "reason phrase"...),
			expect: &PublishNamespaceErrorMessage{
				RequestID:    0,
				ErrorCode:    1,
				ReasonPhrase: "reason phrase",
			},
			err: nil,
		},
	}
	for i, tc := range cases {
		t.Run(fmt.Sprintf("%v", i), func(t *testing.T) {
			res := &PublishNamespaceErrorMessage{}
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
