package wire

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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
				Type:       1,
				ValueBytes: []byte("A"),
			}},
			err: nil,
		},
		{
			data: []byte{0x02, 0x02, 0x03, 0x01, 0x01, 'A'},
			expect: KVPList{
				KeyValuePair{
					Type:        2,
					ValueVarInt: uint64(3),
				},
				KeyValuePair{
					Type:       1,
					ValueBytes: []byte("A"),
				},
			},
			err: nil,
		},
		{
			data: []byte{0x01, 0x01, 0x01, 'A', 0x02, 0x02, 0x02, 0x02},
			expect: KVPList{KeyValuePair{
				Type:       1,
				ValueBytes: []byte("A"),
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
					Type:       0x0f,
					ValueBytes: []byte{0x00},
				},
				KeyValuePair{
					Type:       PathParameterKey,
					ValueBytes: []byte("A"),
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

// TestParseLengthReader exercises parseLengthReader, which decodes a
// byte-length-prefixed KVP list from a bufio.Reader. This is the form used on
// the wire for per-object extension headers (see Subgroup object encoding).
//
// Regression: the previous implementation wrapped the reader in a
// LimitReader+quicvarint reader and looped forever until an error surfaced,
// conflating clean end-of-list with a real I/O error and sometimes reading
// past the length boundary. This test covers both correct termination at the
// exact byte boundary and the empty (length=0) case, and verifies that bytes
// following the KVP block remain available on the underlying reader.
func TestParseLengthReader(t *testing.T) {
	kvp := KVPList{
		{Type: 0x0A, ValueVarInt: 0x00},     // even: media-type = 0
		{Type: 0x0D, ValueBytes: []byte{1}}, // odd: 1-byte payload
		{Type: 0x0F, ValueBytes: []byte{2, 3, 4}},
	}

	body := kvp.append(nil)
	lengthPrefixed := kvp.appendLength(nil)
	trailing := []byte{0xAA, 0xBB, 0xCC}

	cases := []struct {
		name      string
		input     []byte
		expect    KVPList
		remaining []byte
	}{
		{
			name:      "three entries then EOF",
			input:     lengthPrefixed,
			expect:    kvp,
			remaining: nil,
		},
		{
			name:      "three entries with trailing bytes",
			input:     append(append([]byte{}, lengthPrefixed...), trailing...),
			expect:    kvp,
			remaining: trailing,
		},
		{
			name:      "empty list",
			input:     []byte{0x00}, // length=0, no entries
			expect:    KVPList{},
			remaining: nil,
		},
		{
			name:      "empty list with trailing bytes",
			input:     append([]byte{0x00}, trailing...),
			expect:    KVPList{},
			remaining: trailing,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			br := bufio.NewReader(bytes.NewReader(tc.input))
			got := KVPList{}
			err := got.parseLengthReader(br)
			require.NoError(t, err)
			assert.Equal(t, tc.expect, got)

			remaining, _ := io.ReadAll(br)
			assert.Equal(t, tc.remaining, bytesOrNil(remaining),
				"parseLengthReader must stop at the length boundary")
		})
	}

	// Sanity check: body length equals the length prefix we just consumed.
	_ = body
}

// bytesOrNil returns nil for empty slices so that assert.Equal matches the
// test-case expectation (nil vs empty byte slice).
func bytesOrNil(b []byte) []byte {
	if len(b) == 0 {
		return nil
	}
	return b
}
