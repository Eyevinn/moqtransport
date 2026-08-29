package wire2

import (
	"reflect"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newMessage returns a fresh zero value of the same concrete type as m, so a
// round trip parses into a message that shares nothing with the original.
func newMessage(m MessageV18) MessageV18 {
	return reflect.New(reflect.TypeOf(m).Elem()).Interface().(MessageV18)
}

// TestRoundTripV18 encodes each message and parses the bytes back, which pins
// that the two generated halves agree on field order and framing.
func TestRoundTripV18(t *testing.T) {
	namespace := [][]byte{[]byte("example.com"), []byte("meeting=123")}
	params := Parameters{
		// In ascending Type order, which is how the codec writes them, so a
		// round trip compares equal.
		VarintParameter(ParamObjectDeliveryTimeout, 5000),
		BytesParameter(ParamAuthorizationToken, []byte("token")),
		LocationParameter(ParamLargestObject, Location{Group: 9, Object: 3}),
		Uint8Parameter(ParamSubscriberPriority, 128),
	}
	properties := KVPList{{Type: 4, ValueVarInt: 1}}

	cases := []struct {
		name string
		msg  MessageV18
	}{
		{"Setup", &Setup{Options: KVPList{{Type: 1, ValueBytes: []byte("/path")}, {Type: 2, ValueVarInt: 100}}}},
		{"Setup empty", &Setup{Options: KVPList{}}},
		{"GoAwayCtrl", &GoAwayCtrl{NewSessionURI: "moqt://relay.example/", Timeout: 5000, RequestID: 12}},
		{"GoAwayReq", &GoAwayReq{NewSessionURI: "", Timeout: 0}},
		{"Subscribe", &Subscribe{RequestID: 4, TrackNamespace: namespace, TrackName: []byte("video0"), Parameters: params}},
		{"SubscribeOk", &SubscribeOk{TrackAlias: 7, Parameters: params, TrackProperties: properties}},
		{"TrackStatus", &TrackStatus{RequestID: 6, TrackNamespace: namespace, TrackName: []byte("audio0"), Parameters: Parameters{}}},
		{"RequestUpdate", &RequestUpdate{RequestID: 8, Parameters: params}},
		{"Publish", &Publish{RequestID: 2, TrackNamespace: namespace, TrackName: []byte("video0"), TrackAlias: 3, Parameters: params, TrackProperties: properties}},
		{"PublishDone", &PublishDone{StatusCode: 0x4, StreamCount: 17, ErrorReason: "track ended"}},
		{"FetchOk", &FetchOk{EndOfTrack: true, EndLocation: Location{Group: 9, Object: 4}, Parameters: params, TrackProperties: properties}},
		{"PublishNamespace", &PublishNamespace{RequestID: 10, TrackNamespace: namespace, Parameters: Parameters{}}},
		{"Namespace", &Namespace{TrackNamespaceSuffix: [][]byte{[]byte("participant=100")}}},
		{"NamespaceDone", &NamespaceDone{TrackNamespaceSuffix: [][]byte{[]byte("participant=100")}}},
		{"SubscribeNamespace", &SubscribeNamespace{RequestID: 12, TrackNamespacePrefix: namespace, Parameters: Parameters{}}},
		{"SubscribeTracks", &SubscribeTracks{RequestID: 14, TrackNamespacePrefix: namespace, Parameters: params}},
		{"PublishBlocked", &PublishBlocked{TrackNamespaceSuffix: [][]byte{[]byte("p=1")}, TrackName: []byte("video0")}},
		{"RequestOk", &RequestOk{Parameters: params, TrackProperties: properties}},
		{"RequestOk empty", &RequestOk{Parameters: Parameters{}, TrackProperties: KVPList{}}},
		{"PublishOk", &PublishOk{Parameters: Parameters{}, TrackProperties: KVPList{}}},
		{"RequestError", &RequestError{ErrorCode: 0x3, RetryInterval: 1, ErrorReason: "timeout"}},
		{"RequestError with redirect", &RequestError{
			ErrorCode:     0x10,
			RetryInterval: 0,
			ErrorReason:   "moved",
			Redirect: &Redirect{
				ConnectURI:     "moqt://other.example/",
				TrackNamespace: namespace,
				TrackName:      []byte("video1"),
			},
		}},
		{"Fetch standalone", &Fetch{
			RequestID: 16,
			FetchType: FetchTypeStandalone,
			Standalone: &StandaloneFetch{
				TrackNamespace: namespace,
				TrackName:      []byte("video0"),
				StartLocation:  Location{Group: 1, Object: 0},
				EndLocation:    Location{Group: 5, Object: 0},
			},
			Parameters: params,
		}},
		{"Fetch relative joining", &Fetch{
			RequestID:  18,
			FetchType:  FetchTypeRelativeJoining,
			Joining:    &JoiningFetch{JoiningRequestID: 4, JoiningStart: 2},
			Parameters: Parameters{},
		}},
		{"Fetch absolute joining", &Fetch{
			RequestID:  20,
			FetchType:  FetchTypeAbsoluteJoining,
			Joining:    &JoiningFetch{JoiningRequestID: 4, JoiningStart: 77},
			Parameters: Parameters{},
		}},
		{"FetchHeader", &FetchHeader{RequestID: 16}},
		{"Padding", &Padding{}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			buf, err := tc.msg.appendV18(nil)
			require.NoError(t, err)
			got := newMessage(tc.msg)
			require.NoError(t, got.parseV18(buf))
			assert.Equal(t, tc.msg, got)
			assert.Equal(t, tc.msg.Type(), got.Type())
		})
	}
}

// TestParseRejectsTrailingBytes covers the read-side length check: the body a
// message parses must be exactly the body the Length field declared.
func TestParseRejectsTrailingBytes(t *testing.T) {
	msg := &Subscribe{RequestID: 4, TrackNamespace: [][]byte{[]byte("ns")}, TrackName: []byte("t"), Parameters: Parameters{}}
	body, err := msg.appendV18(nil)
	require.NoError(t, err)
	assert.ErrorIs(t, (&Subscribe{}).parseV18(append(body, 0x00)), errTrailingBytes)
}

func TestParseRejectsTruncatedBody(t *testing.T) {
	msg := &Subscribe{RequestID: 4, TrackNamespace: [][]byte{[]byte("ns")}, TrackName: []byte("track"), Parameters: Parameters{}}
	buf, err := msg.appendV18(nil)
	require.NoError(t, err)
	for i := range buf[:len(buf)-1] {
		assert.Error(t, (&Subscribe{}).parseV18(buf[:i]), "truncating to %d bytes should fail", i)
	}
}

func TestParseRejectsOversizedReasonPhrase(t *testing.T) {
	long := make([]byte, 1025)
	msg := &PublishDone{StatusCode: 1, StreamCount: 1, ErrorReason: string(long)}
	buf, err := msg.appendV18(nil)
	require.NoError(t, err)
	assert.ErrorIs(t, (&PublishDone{}).parseV18(buf), errFieldTooLong)
}

func TestParseRejectsOversizedNamespace(t *testing.T) {
	ns := make([][]byte, 33)
	for i := range ns {
		ns[i] = []byte("x")
	}
	msg := &Subscribe{RequestID: 0, TrackNamespace: ns, TrackName: []byte("t"), Parameters: Parameters{}}
	buf, err := msg.appendV18(nil)
	require.NoError(t, err)
	assert.ErrorIs(t, (&Subscribe{}).parseV18(buf), errTooManyFields)
}

func TestFetchRejectsUnknownType(t *testing.T) {
	msg := &Fetch{RequestID: 2, FetchType: FetchType(0x9), Parameters: Parameters{}}
	buf, err := msg.appendV18(nil)
	require.NoError(t, err)
	assert.ErrorIs(t, (&Fetch{}).parseV18(buf), errInvalidFetchType)
}

// TestSubscribeWireFormat pins the exact draft-18 byte layout of a SUBSCRIBE
// body, so a template or field-order change cannot pass unnoticed.
func TestSubscribeWireFormat(t *testing.T) {
	msg := &Subscribe{
		RequestID:      4,
		TrackNamespace: [][]byte{[]byte("ns")},
		TrackName:      []byte("v0"),
		Parameters:     Parameters{Uint8Parameter(ParamSubscriberPriority, 100)},
	}
	buf, err := msg.appendV18(nil)
	require.NoError(t, err)
	assert.Equal(t, []byte{
		0x04, // Request ID
		0x01, // Number of Track Namespace Fields
		0x02, 'n', 's',
		0x02, 'v', '0', // Track Name
		0x01,       // Number of Parameters
		0x20, 0x64, // Delta Type 0x20 (SUBSCRIBER_PRIORITY), uint8 value 100
	}, buf)
}

func TestControlMessageTypeString(t *testing.T) {
	assert.Equal(t, "SUBSCRIBE", ControlMessageTypeSubscribe.String())
	assert.Equal(t, "SETUP", ControlMessageTypeSetup.String())
	assert.Contains(t, ControlMessageType(0x7fff).String(), "unknown")
}

func TestStreamTypeIsSubgroupHeader(t *testing.T) {
	for _, v := range []StreamType{0x10, 0x1f, 0x30, 0x3f, 0x50, 0x5f, 0x70, 0x7f} {
		assert.True(t, v.IsSubgroupHeader(), "%#x should be a subgroup header", uint64(v))
	}
	for _, v := range []StreamType{0x00, 0x05, 0x0f, 0x20, 0x2f, 0x80, 0x90, 0x2F00, 0x132B3E28} {
		assert.False(t, v.IsSubgroupHeader(), "%#x should not be a subgroup header", uint64(v))
	}
}
