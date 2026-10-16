package wire2

import (
	"reflect"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newMessage returns a fresh zero value of the same concrete type as m, so a
// round trip parses into a message that shares nothing with the original.
func newMessage(m ControlMessage) ControlMessage {
	return reflect.New(reflect.TypeOf(m).Elem()).Interface().(ControlMessage)
}

var versions = []Version{Version18, Version22}

// TestRoundTrip encodes each message in each draft it exists in and parses
// the bytes back, which pins that the two halves of every codec agree on
// field order and framing.
func TestRoundTrip(t *testing.T) {
	namespace := [][]byte{[]byte("example.com"), []byte("meeting=123")}
	params := Parameters{
		// In ascending Type order, which is how the codec writes them, so a
		// round trip compares equal.
		VarintParameter(ParamObjectDeliveryTimeout, 5000),
		BytesParameter(ParamAuthorizationToken, []byte("token")),
		LocationParameter(ParamLargestObject, Location{Group: 9, Object: 3}),
		Uint8Parameter(ParamSubscriberPriority, 128),
		LocationFilterParameter(LocationFilter{Type: LocationFilterAbsoluteStart, Start: Location{Group: 4, Object: 1}}),
	}
	properties := KVPList{{Type: 4, ValueVarInt: 1}}

	cases := []struct {
		name   string
		msg    ControlMessage
		drafts []Version // nil means both
	}{
		{"Setup", &Setup{Options: KVPList{{Type: 1, ValueBytes: []byte("/path")}, {Type: 2, ValueVarInt: 100}}}, nil},
		{"Setup empty", &Setup{Options: KVPList{}}, nil},
		{"GoAwayCtrl", &GoAwayCtrl{NewSessionURI: "moqt://relay.example/", Timeout: 5000, RequestID: 12}, []Version{Version18}},
		{"GoAwayCtrl without Request ID", &GoAwayCtrl{NewSessionURI: "moqt://relay.example/", Timeout: 5000}, nil},
		{"GoAwayReq", &GoAwayReq{NewSessionURI: "", Timeout: 0}, nil},
		{"Subscribe", &Subscribe{RequestID: 4, TrackNamespace: namespace, TrackName: []byte("video0"), Parameters: params}, nil},
		{"SubscribeOk", &SubscribeOk{TrackAlias: 7, Parameters: params, TrackProperties: properties}, nil},
		{"TrackStatus", &TrackStatus{RequestID: 6, TrackNamespace: namespace, TrackName: []byte("audio0"), Parameters: Parameters{}}, nil},
		{"RequestUpdate", &RequestUpdate{RequestID: 8, Parameters: params}, nil},
		{"Publish", &Publish{RequestID: 2, TrackNamespace: namespace, TrackName: []byte("video0"), TrackAlias: 3, Parameters: params, TrackProperties: properties}, nil},
		{"PublishDone", &PublishDone{StatusCode: 0x4, StreamCount: 17, ErrorReason: "track ended"}, nil},
		{"PublishStateNotify", &PublishStateNotify{Parameters: Parameters{
			LocationParameter(ParamLargestObject, Location{Group: 9, Object: 3}),
			Uint8Parameter(ParamForward, 0),
		}}, []Version{Version22}},
		{"FetchOk", &FetchOk{EndOfTrack: true, EndLocation: Location{Group: 9, Object: 4}, Parameters: params, TrackProperties: properties}, nil},
		{"PublishNamespace", &PublishNamespace{RequestID: 10, TrackNamespace: namespace, Parameters: Parameters{}}, nil},
		{"Namespace", &Namespace{TrackNamespaceSuffix: [][]byte{[]byte("participant=100")}}, nil},
		{"NamespaceDone", &NamespaceDone{TrackNamespaceSuffix: [][]byte{[]byte("participant=100")}}, nil},
		{"SubscribeNamespace", &SubscribeNamespace{RequestID: 12, TrackNamespacePrefix: namespace, Parameters: Parameters{}}, nil},
		{"SubscribeTracks", &SubscribeTracks{RequestID: 14, TrackNamespacePrefix: namespace, Parameters: params}, nil},
		{"PublishSkipped", &PublishSkipped{TrackNamespaceSuffix: [][]byte{[]byte("p=1")}, TrackName: []byte("video0")}, nil},
		{"RequestOk", &RequestOk{Parameters: params, TrackProperties: properties}, nil},
		{"RequestOk empty", &RequestOk{Parameters: Parameters{}, TrackProperties: KVPList{}}, nil},
		{"PublishOk", &PublishOk{Parameters: Parameters{}, TrackProperties: KVPList{}}, []Version{Version18}},
		{"RequestError", &RequestError{ErrorCode: 0x3, RetryInterval: 1, ErrorReason: "timeout"}, nil},
		{"RequestError with redirect", &RequestError{
			ErrorCode:     0x10,
			RetryInterval: 0,
			ErrorReason:   "moved",
			Redirect: &Redirect{
				ConnectURI:     "moqt://other.example/",
				TrackNamespace: namespace,
				TrackName:      []byte("video1"),
			},
		}, nil},
		{"Fetch standalone", &Fetch{
			RequestID:      16,
			FetchType:      FetchTypeStandalone,
			TrackNamespace: namespace,
			TrackName:      []byte("video0"),
			StartLocation:  Location{Group: 1, Object: 0},
			EndLocation:    Location{Group: 5, Object: 0},
			Parameters:     params,
		}, []Version{Version18}},
		{"Fetch relative joining", &Fetch{
			RequestID:  18,
			FetchType:  FetchTypeRelativeJoining,
			Joining:    &JoiningFetch{JoiningRequestID: 4, JoiningStart: 2},
			Parameters: Parameters{},
		}, []Version{Version18}},
		{"Fetch absolute joining", &Fetch{
			RequestID:  20,
			FetchType:  FetchTypeAbsoluteJoining,
			Joining:    &JoiningFetch{JoiningRequestID: 4, JoiningStart: 77},
			Parameters: Parameters{},
		}, []Version{Version18}},
		{"Fetch draft-22", &Fetch{
			RequestID:      16,
			FetchType:      FetchTypeStandalone,
			TrackNamespace: namespace,
			TrackName:      []byte("video0"),
			Parameters: Parameters{LocationFilterParameter(LocationFilter{
				Type: LocationFilterAbsoluteRange, Start: Location{Group: 1}, EndGroupDelta: 4, EndObject: 9,
			})},
		}, []Version{Version22}},
		{"FetchHeader", &FetchHeader{RequestID: 16}, nil},
		{"Padding", &Padding{}, nil},
	}

	for _, tc := range cases {
		drafts := tc.drafts
		if drafts == nil {
			drafts = versions
		}
		for _, v := range drafts {
			t.Run(tc.name+"/"+v.String(), func(t *testing.T) {
				buf, err := appendBody(nil, tc.msg, v)
				require.NoError(t, err)
				got := newMessage(tc.msg)
				require.NoError(t, parseBody(got, buf, v))
				assert.Equal(t, tc.msg, got)
				assert.Equal(t, tc.msg.Type(), got.Type())
			})
		}
	}
}

// A message is sendable only in the drafts that define it.
func TestMessagesOutsideTheirDraft(t *testing.T) {
	_, err := appendBody(nil, &PublishStateNotify{}, Version18)
	assert.ErrorAs(t, err, &messageNotInDraftError{})
	_, err = appendBody(nil, &PublishOk{}, Version22)
	assert.ErrorAs(t, err, &messageNotInDraftError{})

	_, err = appendBody(nil, &Fetch{FetchType: FetchTypeRelativeJoining, Joining: &JoiningFetch{}}, Version22)
	assert.ErrorIs(t, err, errJoiningFetchNotInDraft)

	_, err = appendBody(nil, &Setup{}, Version(20))
	assert.ErrorAs(t, err, &unsupportedVersionError{})
}

// GOAWAY on the control stream lost its Request ID in draft-19.
func TestGoAwayCtrlRequestIDIsDraft18Only(t *testing.T) {
	msg := &GoAwayCtrl{Timeout: 1, RequestID: 7}
	b18, err := appendBody(nil, msg, Version18)
	require.NoError(t, err)
	b22, err := appendBody(nil, msg, Version22)
	require.NoError(t, err)
	assert.Equal(t, []byte{0x00, 0x01, 0x07}, b18)
	assert.Equal(t, []byte{0x00, 0x01}, b22)
}

// TestFetchWireFormatV22 pins draft-22's FETCH body: no Fetch Type and no
// range fields, the range being a LOCATION_FILTER parameter.
func TestFetchWireFormatV22(t *testing.T) {
	msg := &Fetch{
		RequestID:      2,
		TrackNamespace: [][]byte{[]byte("ns")},
		TrackName:      []byte("t"),
		Parameters: Parameters{LocationFilterParameter(LocationFilter{
			Type: LocationFilterAbsoluteGroupEnd, Start: Location{Group: 3}, EndGroupDelta: 1,
		})},
	}
	buf, err := appendBody(nil, msg, Version22)
	require.NoError(t, err)
	assert.Equal(t, []byte{
		0x02,                 // Request ID
		0x01, 0x02, 'n', 's', // Track Namespace
		0x01, 't', // Track Name
		0x01,       // Number of Parameters
		0x21,       // LOCATION_FILTER
		0x03,       // Absolute Start, Group End
		0x03, 0x00, // StartGroup, StartObject
		0x01, // EndGroupDelta
	}, buf)
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
	_, err := (&Fetch{RequestID: 2, FetchType: FetchType(0x9), Parameters: Parameters{}}).appendV18(nil)
	assert.ErrorIs(t, err, errInvalidFetchType)
	// Request ID 2, Fetch Type 9, no parameters.
	assert.ErrorIs(t, (&Fetch{}).parseV18([]byte{0x02, 0x09, 0x00}), errInvalidFetchType)
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
