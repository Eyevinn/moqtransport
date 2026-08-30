package moqtransport

import (
	"bufio"
	"bytes"
	"errors"
	"io"
	"testing"

	"github.com/Eyevinn/moqtransport/internal/wire2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

// sendCapture is a SendStream that records what was written to it and how it
// ended, which is the whole observable behaviour of a subgroup.
type sendCapture struct {
	buf    bytes.Buffer
	closes int
	resets []StreamErrorCode
}

func newSendCapture(t *testing.T) (*MockSendStream, *sendCapture) {
	t.Helper()
	ctrl := gomock.NewController(t)
	stream := NewMockSendStream(ctrl)
	c := &sendCapture{}

	stream.EXPECT().Write(gomock.Any()).DoAndReturn(c.buf.Write).AnyTimes()
	stream.EXPECT().Close().DoAndReturn(func() error { c.closes++; return nil }).AnyTimes()
	stream.EXPECT().Reset(gomock.Any()).Do(func(code uint32) {
		c.resets = append(c.resets, StreamErrorCode(code))
	}).AnyTimes()
	stream.EXPECT().StreamID().Return(uint64(4)).AnyTimes()
	return stream, c
}

// receiveAll reads back what a subgroup wrote, the way a subscriber would:
// classify the stream type, parse the header, then read Objects.
func receiveAll(t *testing.T, raw []byte, defaultPriority uint8) ([]*Object, error) {
	t.Helper()
	r := bufio.NewReader(bytes.NewReader(raw))
	header, err := wire2.ReadSubgroupHeader(r)
	require.NoError(t, err)

	var got []*Object
	err = newSubgroupReceiver(nil, header, r, defaultPriority, qlogger{}).receive(func(o *Object) error {
		got = append(got, o)
		return nil
	})
	return got, err
}

func TestSubgroupWriteAndReceive(t *testing.T) {
	stream, capture := newSendCapture(t)
	header := &wire2.SubgroupHeader{
		TrackAlias:     1,
		GroupID:        7,
		SubgroupIDMode: wire2.SubgroupIDExplicit,
		SubgroupID:     2,
		Priority:       200,
	}

	sg, err := newSubgroup(stream, header, qlogger{})
	require.NoError(t, err)
	assert.Equal(t, uint64(7), sg.GroupID())
	assert.Equal(t, uint64(2), sg.SubgroupID())

	n, err := sg.WriteObject(0, []byte("first"))
	require.NoError(t, err)
	assert.Equal(t, 5, n)
	_, err = sg.WriteObject(1, []byte("second"))
	require.NoError(t, err)
	require.NoError(t, sg.WriteStatus(2, ObjectStatusEndOfGroup))
	require.NoError(t, sg.Close())

	objects, err := receiveAll(t, capture.buf.Bytes(), 128)
	require.NoError(t, err)
	require.Len(t, objects, 3)

	for _, o := range objects {
		assert.Equal(t, uint64(7), o.GroupID)
		assert.Equal(t, uint64(2), o.SubgroupID)
		assert.Equal(t, uint8(200), o.Priority)
		assert.Equal(t, ObjectForwardingPreferenceSubgroup, o.ForwardingPreference)
	}
	assert.Equal(t, "first", string(objects[0].Payload))
	assert.Equal(t, "second", string(objects[1].Payload))
	assert.Equal(t, ObjectStatusEndOfGroup, objects[2].Status)
	assert.Equal(t, 1, capture.closes)
}

// With DEFAULT_PRIORITY the header carries no priority byte at all, and the
// Objects take the one the subscription was established with.
func TestSubgroupInheritsSubscriptionPriority(t *testing.T) {
	stream, capture := newSendCapture(t)
	sg, err := newSubgroup(stream, &wire2.SubgroupHeader{
		TrackAlias:      1,
		GroupID:         0,
		DefaultPriority: true,
	}, qlogger{})
	require.NoError(t, err)
	_, err = sg.WriteObject(0, []byte("x"))
	require.NoError(t, err)

	objects, err := receiveAll(t, capture.buf.Bytes(), 42)
	require.NoError(t, err)
	require.Len(t, objects, 1)
	assert.Equal(t, uint8(42), objects[0].Priority)
}

func TestSubgroupProperties(t *testing.T) {
	props := KVPList{{Type: wire2.PropertyPriorObjectIDGap, ValueVarInt: 4}}

	stream, capture := newSendCapture(t)
	sg, err := newSubgroup(stream, &wire2.SubgroupHeader{TrackAlias: 1, HasProperties: true}, qlogger{})
	require.NoError(t, err)
	_, err = sg.WriteObjectWithProperties(0, props, []byte("x"))
	require.NoError(t, err)

	objects, err := receiveAll(t, capture.buf.Bytes(), 0)
	require.NoError(t, err)
	require.Len(t, objects, 1)
	assert.Equal(t, props, objects[0].Properties)

	// The PROPERTIES bit is in the header and covers the whole stream, so a
	// subgroup opened without it cannot carry them on any object.
	plain, _ := newSendCapture(t)
	sg, err = newSubgroup(plain, &wire2.SubgroupHeader{TrackAlias: 1}, qlogger{})
	require.NoError(t, err)
	_, err = sg.WriteObjectWithProperties(0, props, []byte("x"))
	assert.Error(t, err)
}

// A FIN says every Object in the subgroup arrived; a reset says some did not.
// Confusing the two is not detectable by the subscriber, so the two paths are
// kept distinct and neither happens twice.
func TestSubgroupCloseAndReset(t *testing.T) {
	stream, capture := newSendCapture(t)
	sg, err := newSubgroup(stream, &wire2.SubgroupHeader{TrackAlias: 1}, qlogger{})
	require.NoError(t, err)

	require.NoError(t, sg.Close())
	assert.Equal(t, 1, capture.closes)
	require.NoError(t, sg.Close(), "closing twice is a no-op")
	assert.Equal(t, 1, capture.closes)

	_, err = sg.WriteObject(0, []byte("x"))
	assert.ErrorIs(t, err, errSubgroupClosed)

	sg.Reset(StreamErrorCancelled)
	assert.Empty(t, capture.resets, "a finished subgroup is not reset afterwards")

	stream, capture = newSendCapture(t)
	sg, err = newSubgroup(stream, &wire2.SubgroupHeader{TrackAlias: 1}, qlogger{})
	require.NoError(t, err)
	sg.Reset(StreamErrorDeliveryTimeout)
	sg.Reset(StreamErrorCancelled)
	assert.Equal(t, []StreamErrorCode{StreamErrorDeliveryTimeout}, capture.resets)
	assert.Zero(t, capture.closes, "a reset subgroup is never FINed")
}

// A stream that fails part-way through surfaces the failure rather than
// looking like a clean end, since the two mean opposite things about whether
// the subgroup is complete.
func TestSubgroupReceiveStreamError(t *testing.T) {
	stream, capture := newSendCapture(t)
	sg, err := newSubgroup(stream, &wire2.SubgroupHeader{TrackAlias: 1}, qlogger{})
	require.NoError(t, err)
	_, err = sg.WriteObject(0, []byte("x"))
	require.NoError(t, err)

	raw := capture.buf.Bytes()
	r := bufio.NewReader(io.MultiReader(bytes.NewReader(raw), errReader{errors.New("stream reset")}))
	header, err := wire2.ReadSubgroupHeader(r)
	require.NoError(t, err)

	var delivered int
	err = newSubgroupReceiver(nil, header, r, 0, qlogger{}).receive(func(*Object) error {
		delivered++
		return nil
	})
	assert.Equal(t, 1, delivered)
	assert.Error(t, err)
}

type errReader struct{ err error }

func (r errReader) Read([]byte) (int, error) { return 0, r.err }
