package wire2

import (
	"fmt"
	"io"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestParseKVPList(t *testing.T) {
	cases := []struct {
		data   []byte
		expect KVPList
		err    error
	}{
		{
			data:   nil,
			expect: KVPList{},
			err:    io.EOF,
		},
		{
			data:   nil,
			expect: KVPList{},
			err:    io.EOF,
		},
		{
			data:   []byte{},
			expect: KVPList{},
			err:    io.EOF,
		},
		{
			data: []byte{0x01, 0x01, 0x01, 'A'},
			expect: KVPList{KeyValuePair{
				Type:  1,
				Bytes: []byte("A"),
			}},
			err: nil,
		},
		{
			data: []byte{0x02, 0x02, 0x03, 0x01, 0x01, 'A'},
			expect: KVPList{
				KeyValuePair{
					Type:   2,
					Varint: uint64(3),
				},
				KeyValuePair{
					Type:  1,
					Bytes: []byte("A"),
				},
			},
			err: nil,
		},
		{
			data: []byte{0x01, 0x01, 0x01, 'A', 0x02, 0x02, 0x02, 0x02},
			expect: KVPList{KeyValuePair{
				Type:  1,
				Bytes: []byte("A"),
			}},
			err: nil,
		},
		{
			data:   []byte{},
			expect: KVPList{},
			err:    io.EOF,
		},
		{
			data: []byte{0x02, 0x0f, 0x01, 0x00, 0x01, 0x01, 'A'},
			expect: KVPList{
				KeyValuePair{
					Type:  0x0f,
					Bytes: []byte{0x00},
				},
				KeyValuePair{
					Type:  PathParameterKey,
					Bytes: []byte("A"),
				},
			},
			err: nil,
		},
	}
	for i, tc := range cases {
		t.Run(fmt.Sprintf("%v", i), func(t *testing.T) {
			res := KVPList{}
			err := res.parseNum(tc.data)
			assert.Equal(t, tc.expect, res)
			if tc.err != nil {
				assert.Equal(t, tc.err, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}
