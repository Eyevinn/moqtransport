package moqtransport

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/Eyevinn/moqtransport/internal/wire2"
	"github.com/mengelbart/qlog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// qlogEvents runs a session pair with logging on and returns every event the
// given side wrote, decoded.
//
// qlog output is JSON-SEQ: each record is preceded by an RS (0x1e), so the
// records are split on it rather than on newlines.
func qlogEvents(t *testing.T, raw []byte) []map[string]any {
	t.Helper()
	var events []map[string]any
	for _, record := range strings.Split(string(raw), "\x1e") {
		record = strings.TrimSpace(record)
		if record == "" {
			continue
		}
		var decoded map[string]any
		require.NoError(t, json.Unmarshal([]byte(record), &decoded), "record: %s", record)
		events = append(events, decoded)
	}
	return events
}

// eventNames returns the name of every event that has one, with the category
// prefix the qlog library writes ("moqt:") stripped off.
func eventNames(events []map[string]any) []string {
	var names []string
	for _, e := range events {
		if name, ok := e["name"].(string); ok {
			names = append(names, strings.TrimPrefix(name, "moqt:"))
		}
	}
	return names
}

// A session with a logger records the whole exchange: the handshake, every
// control message on both sides, the data streams and the Objects on them.
func TestQlogRecordsASession(t *testing.T) {
	var clientLog, serverLog bytes.Buffer

	published := make(chan *Subscription, 1)
	server := &Session{
		Qlogger: qlog.NewQLOGHandler(&serverLog, "test", "test", "server", QlogSchema),
		SubscribeHandler: SubscribeHandlerFunc(func(r *SubscribeRequest) {
			subscription, err := r.Accept(WithLargestObject(Location{Group: 3}))
			if err != nil {
				return
			}
			published <- subscription
		}),
	}
	client := &Session{
		Qlogger: qlog.NewQLOGHandler(&clientLog, "test", "test", "client", QlogSchema),
	}
	runSessions(t, client, server)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	track, err := client.Subscribe(ctx, []string{"example.com"}, "video0")
	require.NoError(t, err)
	subscription := <-published

	sg, err := subscription.OpenSubgroup(3, 0, 128, WithObjectProperties())
	require.NoError(t, err)
	_, err = sg.WriteObjectWithProperties(0,
		KVPList{{Type: wire2.PropertyPriorObjectIDGap, ValueVarInt: 2}}, []byte("payload"))
	require.NoError(t, err)
	require.NoError(t, sg.Close())

	require.NoError(t, subscription.SendDatagram(Object{GroupID: 4, Priority: 9, Payload: []byte("d")}))

	// Two Objects in, so both data paths have been logged by the time this
	// returns.
	for range 2 {
		_, err := track.ReadObject(ctx)
		require.NoError(t, err)
	}

	clientNames := eventNames(qlogEvents(t, clientLog.Bytes()))
	serverNames := eventNames(qlogEvents(t, serverLog.Bytes()))

	// The client sends SETUP and SUBSCRIBE and reads the peer's SETUP and
	// SUBSCRIBE_OK, so it sees control messages in both directions.
	assert.Contains(t, clientNames, "control_message_created")
	assert.Contains(t, clientNames, "control_message_parsed")
	assert.Contains(t, serverNames, "control_message_created")
	assert.Contains(t, serverNames, "control_message_parsed")

	// The publisher opens the subgroup and writes on it; the subscriber reads
	// it. Both ends record the stream and the Object.
	assert.Contains(t, serverNames, "stream_type_set")
	assert.Contains(t, serverNames, "subgroup_object_created")
	assert.Contains(t, clientNames, "stream_type_set")
	assert.Contains(t, clientNames, "subgroup_object_parsed")

	assert.Contains(t, serverNames, "object_datagram_created")
	assert.Contains(t, clientNames, "object_datagram_parsed")
}

// The message body is rendered field by field, so a log says what was actually
// sent rather than only which type it was.
// fakeStream is a stream that is only ever asked for its ID.
type fakeStream struct{ id uint64 }

func (s fakeStream) StreamID() uint64 { return s.id }

func TestQlogRendersMessageFields(t *testing.T) {
	var log bytes.Buffer
	logger := qlogger{logger: qlog.NewQLOGHandler(&log, "test", "test", "client", QlogSchema)}

	logger.logControlMessage("control_message_created", fakeStream{id: 4}, &wire2.Subscribe{
		RequestID:      6,
		TrackNamespace: [][]byte{[]byte("example.com"), []byte("live")},
		TrackName:      []byte("video0"),
		Parameters: wire2.Parameters{
			wire2.Uint8Parameter(wire2.ParamSubscriberPriority, 7),
		},
	})

	events := qlogEvents(t, log.Bytes())
	require.NotEmpty(t, events)
	last := events[len(events)-1]

	data, ok := last["data"].(map[string]any)
	require.True(t, ok, "event has no data: %v", last)
	assert.Equal(t, float64(4), data["stream_id"])

	message, ok := data["message"].(map[string]any)
	require.True(t, ok, "event has no message: %v", data)
	assert.Equal(t, "subscribe", message["type"])
	assert.Equal(t, float64(6), message["request_id"], "field names are snake_case")
	assert.Equal(t, []any{"example.com", "live"}, message["track_namespace"])
	assert.NotNil(t, message["track_name"])
	assert.NotNil(t, message["parameters"], "parameters are rendered, not dropped")
}

// A message type nobody wrote a renderer for still logs its fields, which is
// the point of reading them reflectively: a draft-20 message set is covered
// the day it is declared.
func TestQlogRendersEveryMessage(t *testing.T) {
	messages := []wire2.ControlMessage{
		&wire2.Setup{Options: wire2.KVPList{wire2.ImplementationOption("test/1.0")}},
		&wire2.GoAwayCtrl{NewSessionURI: "moqt://b.example/", Timeout: 100, RequestID: 6},
		&wire2.SubscribeOk{TrackAlias: 9, Parameters: wire2.Parameters{}},
		&wire2.RequestError{ErrorCode: 0x10, ErrorReason: "no such track"},
		&wire2.PublishDone{StatusCode: 2, StreamCount: 5, ErrorReason: "done"},
		&wire2.FetchOk{EndOfTrack: true, EndLocation: wire2.Location{Group: 7, Object: 2}},
		&wire2.Namespace{TrackNamespaceSuffix: [][]byte{[]byte("room=1")}},
		&wire2.PublishBlocked{TrackNamespaceSuffix: [][]byte{[]byte("ns")}, TrackName: []byte("v0")},
	}

	for _, msg := range messages {
		t.Run(msg.Type().String(), func(t *testing.T) {
			var log bytes.Buffer
			logger := qlogger{logger: qlog.NewQLOGHandler(&log, "t", "t", "c", QlogSchema)}
			logger.logControlMessage("control_message_created", fakeStream{}, msg)

			events := qlogEvents(t, log.Bytes())
			require.NotEmpty(t, events)
			data := events[len(events)-1]["data"].(map[string]any)
			message, ok := data["message"].(map[string]any)
			require.True(t, ok)

			assert.Equal(t, strings.ToLower(msg.Type().String()), message["type"])
			assert.Greater(t, len(message), 1, "only the type was rendered; the fields were dropped")
		})
	}
}

// A nil logger is the default, and has to cost nothing rather than panic.
func TestQlogNilLoggerIsInert(t *testing.T) {
	var logger qlogger
	assert.False(t, logger.enabled())
	logger.logControlMessage("control_message_created", fakeStream{}, &wire2.Setup{})
	logger.logStreamType("local", fakeStream{}, "subgroup_header")
	logger.logSubgroupObject("subgroup_object_created", fakeStream{},
		&wire2.SubgroupHeader{}, &wire2.SubgroupObject{})
	logger.logDatagram("object_datagram_created", &wire2.ObjectDatagram{})
	logger.logFetchObject("fetch_object_created", fakeStream{}, &wire2.FetchObject{})

	// A nil stream is what a caller holding only a parser has.
	logger.logControlMessage("control_message_created", nil, &wire2.Setup{})
}

func TestQlogFieldName(t *testing.T) {
	for input, want := range map[string]string{
		"RequestID":       "request_id",
		"TrackNamespace":  "track_namespace",
		"NewSessionURI":   "new_session_uri",
		"EndOfTrack":      "end_of_track",
		"TrackAlias":      "track_alias",
		"Parameters":      "parameters",
		"TrackProperties": "track_properties",
		"StatusCode":      "status_code",
	} {
		assert.Equal(t, want, qlogFieldName(input), "input %q", input)
	}
}
