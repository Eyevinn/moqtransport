package wire2

import (
	"bytes"
	"encoding/binary"
	"io"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestFramingRoundTrip runs each message through the appender and back out of
// the parser in the scope it belongs to.
func TestFramingRoundTrip(t *testing.T) {
	namespace := [][]byte{[]byte("example.com"), []byte("live")}
	params := KVPList{{Type: 2, ValueVarInt: 3000}}

	cases := []struct {
		name  string
		scope StreamScope
		msg   MessageV18
	}{
		{"SETUP", ScopeControl, &Setup{Options: KVPList{{Type: 1, ValueBytes: []byte("/live")}}}},
		{"GOAWAY on the control stream", ScopeControl, &GoAwayCtrl{NewSessionURI: "moqt://b.example/", Timeout: 100, RequestID: 6}},
		{"GOAWAY on a request stream", ScopeRequest, &GoAwayReq{Timeout: 100}},
		{"SUBSCRIBE", ScopeRequest, &Subscribe{RequestID: 0, TrackNamespace: namespace, TrackName: []byte("v0"), Parameters: params}},
		{"SUBSCRIBE_OK", ScopeRequest, &SubscribeOk{TrackAlias: 1, Parameters: KVPList{}, TrackProperties: KVPList{}}},
		{"REQUEST_ERROR", ScopeRequest, &RequestError{ErrorCode: 4, RetryInterval: 0, ErrorReason: "no such track"}},
		{"FETCH", ScopeRequest, &Fetch{
			RequestID:  2,
			FetchType:  FetchTypeRelativeJoining,
			Joining:    &JoiningFetch{JoiningRequestID: 0, JoiningStart: 1},
			Parameters: KVPList{},
		}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			buf, err := AppendControlMessage(nil, tc.msg)
			require.NoError(t, err)

			got, err := NewControlMessageParser(bytes.NewReader(buf), tc.scope).Parse()
			require.NoError(t, err)
			assert.Equal(t, tc.msg, got)
		})
	}
}

// TestFramingLengthField pins the frame layout: a vi64 type, then the body
// length as a fixed 16-bit big-endian integer, then the body.
func TestFramingLengthField(t *testing.T) {
	msg := &GoAwayReq{NewSessionURI: "", Timeout: 0}
	buf, err := AppendControlMessage(nil, msg)
	require.NoError(t, err)

	// Type 0x10, length 0x0002, body 0x00 0x00.
	assert.Equal(t, []byte{0x10, 0x00, 0x02, 0x00, 0x00}, buf)
}

func TestFramingSetupTypeIsTwoBytes(t *testing.T) {
	buf, err := AppendControlMessage(nil, &Setup{Options: KVPList{}})
	require.NoError(t, err)
	// 0x2F00 does not fit the 1-byte vi64 form, so the type occupies two bytes
	// before the fixed 16-bit length.
	assert.Equal(t, []byte{0xaf, 0x00, 0x00, 0x00}, buf)
}

func TestAppendRejectsOversizedBody(t *testing.T) {
	// One parameter whose value alone fills the 16-bit length field.
	msg := &Subscribe{
		TrackNamespace: [][]byte{},
		TrackName:      []byte{},
		Parameters:     KVPList{{Type: 1, ValueBytes: make([]byte, maxControlMessageBodyLen)}},
	}
	_, err := AppendControlMessage(nil, msg)
	assert.ErrorIs(t, err, errControlMessageTooLong)
}

func TestAppendPreservesExistingBuffer(t *testing.T) {
	prefix := []byte{0xde, 0xad}
	buf, err := AppendControlMessage(prefix, &GoAwayReq{Timeout: 0})
	require.NoError(t, err)
	assert.Equal(t, prefix, buf[:2])
}

// TestParseRejectsLengthShorterThanBody covers a declared length that cuts the
// body short: the message wants bytes the frame did not include.
func TestParseRejectsLengthShorterThanBody(t *testing.T) {
	msg := &Subscribe{RequestID: 0, TrackNamespace: [][]byte{[]byte("ns")}, TrackName: []byte("v0"), Parameters: KVPList{}}
	buf, err := AppendControlMessage(nil, msg)
	require.NoError(t, err)

	binary.BigEndian.PutUint16(buf[1:3], uint16(len(buf)-3-1))
	buf = buf[:len(buf)-1]

	_, err = NewControlMessageParser(bytes.NewReader(buf), ScopeRequest).Parse()
	assert.ErrorIs(t, err, io.ErrUnexpectedEOF)
}

// TestParseRejectsLengthLongerThanBody covers the other half of the check: the
// frame declared more bytes than the message consumed.
func TestParseRejectsLengthLongerThanBody(t *testing.T) {
	msg := &GoAwayReq{NewSessionURI: "", Timeout: 0}
	buf, err := AppendControlMessage(nil, msg)
	require.NoError(t, err)

	buf = append(buf, 0x00)
	binary.BigEndian.PutUint16(buf[1:3], uint16(len(buf)-3))

	_, err = NewControlMessageParser(bytes.NewReader(buf), ScopeRequest).Parse()
	assert.ErrorIs(t, err, errTrailingBytes)
}

func TestParseTruncatedFrame(t *testing.T) {
	msg := &Subscribe{RequestID: 0, TrackNamespace: [][]byte{[]byte("ns")}, TrackName: []byte("v0"), Parameters: KVPList{}}
	full, err := AppendControlMessage(nil, msg)
	require.NoError(t, err)

	for i := 1; i < len(full); i++ {
		_, err := NewControlMessageParser(bytes.NewReader(full[:i]), ScopeRequest).Parse()
		assert.ErrorIs(t, err, io.ErrUnexpectedEOF, "truncating to %d bytes", i)
	}
}

// TestParseCleanEndOfStream keeps io.EOF meaning "no more messages", which the
// read loops rely on to tell a closed stream from a broken one.
func TestParseCleanEndOfStream(t *testing.T) {
	_, err := NewControlMessageParser(bytes.NewReader(nil), ScopeRequest).Parse()
	assert.ErrorIs(t, err, io.EOF)
	assert.NotErrorIs(t, err, io.ErrUnexpectedEOF)
}

func TestParseSequentialMessages(t *testing.T) {
	first := &GoAwayReq{Timeout: 1}
	second := &RequestOk{Parameters: KVPList{}, TrackProperties: KVPList{}}

	buf, err := AppendControlMessage(nil, first)
	require.NoError(t, err)
	buf, err = AppendControlMessage(buf, second)
	require.NoError(t, err)

	p := NewControlMessageParser(bytes.NewReader(buf), ScopeRequest)
	got, err := p.Parse()
	require.NoError(t, err)
	assert.Equal(t, first, got)
	got, err = p.Parse()
	require.NoError(t, err)
	assert.Equal(t, second, got)
	_, err = p.Parse()
	assert.ErrorIs(t, err, io.EOF)
}

// TestScopeSelectsTheMessage is the point of scoping: the same codepoint means
// different things on the two stream kinds.
func TestScopeSelectsTheMessage(t *testing.T) {
	t.Run("GOAWAY differs by scope", func(t *testing.T) {
		buf, err := AppendControlMessage(nil, &GoAwayCtrl{NewSessionURI: "", Timeout: 5, RequestID: 8})
		require.NoError(t, err)

		got, err := NewControlMessageParser(bytes.NewReader(buf), ScopeControl).Parse()
		require.NoError(t, err)
		assert.IsType(t, &GoAwayCtrl{}, got)

		// The same bytes read as a request-stream GOAWAY have a trailing
		// Request ID the message does not define.
		_, err = NewControlMessageParser(bytes.NewReader(buf), ScopeRequest).Parse()
		assert.ErrorIs(t, err, errTrailingBytes)
	})

	t.Run("SUBSCRIBE is not a control stream message", func(t *testing.T) {
		buf, err := AppendControlMessage(nil, &Subscribe{TrackNamespace: [][]byte{}, TrackName: []byte{}, Parameters: KVPList{}})
		require.NoError(t, err)
		_, err = NewControlMessageParser(bytes.NewReader(buf), ScopeControl).Parse()
		assert.ErrorContains(t, err, "unknown control message type")
	})

	t.Run("SETUP is not a request stream message", func(t *testing.T) {
		buf, err := AppendControlMessage(nil, &Setup{Options: KVPList{}})
		require.NoError(t, err)
		_, err = NewControlMessageParser(bytes.NewReader(buf), ScopeRequest).Parse()
		assert.ErrorContains(t, err, "unknown control message type")
	})
}

func TestParseUnknownMessageType(t *testing.T) {
	// Type 0x60, zero-length body.
	_, err := NewControlMessageParser(bytes.NewReader([]byte{0x60, 0x00, 0x00}), ScopeRequest).Parse()
	assert.ErrorContains(t, err, "unknown control message type")
}

func TestClassifyStreamType(t *testing.T) {
	cases := []struct {
		streamType StreamType
		kind       UniStreamKind
	}{
		{StreamTypeSetup, UniStreamControl},
		{StreamTypeFetchHeader, UniStreamFetch},
		{StreamTypePadding, UniStreamPadding},
		{0x10, UniStreamSubgroup},
		{0x1d, UniStreamSubgroup},
		{0x7f, UniStreamSubgroup},
	}
	for _, tc := range cases {
		kind, err := ClassifyStreamType(tc.streamType)
		require.NoError(t, err, "stream type %#x", uint64(tc.streamType))
		assert.Equal(t, tc.kind, kind)
	}

	for _, unknown := range []StreamType{0x00, 0x01, 0x06, 0x0f, 0x20, 0x2f, 0x80, 0x2F01} {
		_, err := ClassifyStreamType(unknown)
		assert.ErrorContains(t, err, "unknown stream type", "stream type %#x", uint64(unknown))
	}
}

// TestFetchHeaderCodepointIsScoped is the sharpest case of codepoint reuse:
// 0x5 opens a FETCH response stream but means REQUEST_ERROR on a request
// stream.
func TestFetchHeaderCodepointIsScoped(t *testing.T) {
	kind, err := ClassifyStreamType(StreamType(0x5))
	require.NoError(t, err)
	assert.Equal(t, UniStreamFetch, kind)

	msg, err := newControlMessage(ScopeRequest, ControlMessageType(0x5))
	require.NoError(t, err)
	assert.IsType(t, &RequestError{}, msg)
}

func TestReadStreamType(t *testing.T) {
	t.Run("reads a multi-byte stream type", func(t *testing.T) {
		r := bytes.NewReader([]byte{0xaf, 0x00})
		streamType, kind, err := ReadStreamType(r)
		require.NoError(t, err)
		assert.Equal(t, StreamTypeSetup, streamType)
		assert.Equal(t, UniStreamControl, kind)
	})

	t.Run("returns the type of an unknown stream alongside the error", func(t *testing.T) {
		streamType, _, err := ReadStreamType(bytes.NewReader([]byte{0x20}))
		assert.ErrorContains(t, err, "unknown stream type")
		assert.Equal(t, StreamType(0x20), streamType)
	})

	t.Run("reports an empty stream", func(t *testing.T) {
		_, _, err := ReadStreamType(bytes.NewReader(nil))
		assert.ErrorIs(t, err, io.EOF)
	})
}
