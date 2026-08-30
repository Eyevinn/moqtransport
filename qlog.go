package moqtransport

import (
	"log/slog"
	"reflect"
	"strings"

	"github.com/Eyevinn/moqtransport/internal/wire2"
	"github.com/mengelbart/qlog"
	"github.com/mengelbart/qlog/moqt"
)

// QlogSchema is the schema identifier to pass to [qlog.NewQLOGHandler] for a
// logger given to [Session.Qlogger].
const QlogSchema = moqt.Schema

// qlogger is the optional event sink, wrapped so that every call site can log
// unconditionally instead of guarding each one.
//
// It is a value, not a pointer: it is copied into every stream and request
// that logs, and copying a single pointer field is cheaper than reaching back
// through the session.
type qlogger struct {
	logger *qlog.Logger
}

func (q qlogger) log(event qlog.Event) {
	if q.logger != nil {
		q.logger.Log(event)
	}
}

// enabled reports whether anything is listening. Callers use it to skip work
// that only exists to produce an event, such as copying a payload.
func (q qlogger) enabled() bool { return q.logger != nil }

// streamIdentifier is the part of a stream an event needs.
//
// The helpers take the stream rather than its ID so that StreamID is never
// called when nothing is listening: it is a real call into the transport, and
// a test's mock has no reason to expect one it did not ask for.
type streamIdentifier interface {
	StreamID() uint64
}

// streamIDOf reads a stream's ID, tolerating the nil a caller that has only a
// parser passes.
func streamIDOf(stream streamIdentifier) uint64 {
	if stream == nil {
		return 0
	}
	// A typed nil -- an interface holding a (*T)(nil) -- is not == nil and
	// would panic on the call.
	if v := reflect.ValueOf(stream); v.Kind() == reflect.Pointer && v.IsNil() {
		return 0
	}
	return stream.StreamID()
}

// logControlMessage records a control message in either direction.
func (q qlogger) logControlMessage(name moqt.ControlMessageEventName, stream streamIdentifier, msg wire2.ControlMessage) {
	if !q.enabled() {
		return
	}
	q.log(moqt.ControlMessageEvent{
		EventName: name,
		StreamID:  streamIDOf(stream),
		Message:   controlMessageValue{msg},
	})
}

// logStreamType records that a stream's type is known, which for a data stream
// is the first thing that happens on it.
func (q qlogger) logStreamType(owner moqt.Owner, stream streamIdentifier, streamType moqt.StreamType) {
	if !q.enabled() {
		return
	}
	q.log(moqt.StreamTypeSetEvent{
		Owner:      moqt.GetOwner(owner),
		StreamID:   streamIDOf(stream),
		StreamType: streamType,
	})
}

// logSubgroupObject records one Object on a subgroup stream, in either
// direction.
func (q qlogger) logSubgroupObject(name moqt.SubgroupObjectEventName, stream streamIdentifier,
	header *wire2.SubgroupHeader, obj *wire2.SubgroupObject) {
	if !q.enabled() {
		return
	}
	groupID, subgroupID := header.GroupID, header.SubgroupID
	q.log(moqt.SubgroupObjectEvent{
		EventName:              name,
		StreamID:               streamIDOf(stream),
		GroupID:                &groupID,
		SubgroupID:             &subgroupID,
		ObjectID:               obj.ObjectID,
		ExtensionHeadersLength: uint64(len(obj.Properties)),
		ExtensionHeaders:       propertiesToQlog(obj.Properties),
		ObjectPayloadLength:    uint64(len(obj.Payload)),
		ObjectStatus:           uint64(obj.Status),
		ObjectPayload:          rawInfo(obj.Payload),
	})
}

// logDatagram records one Object sent or received as a datagram.
func (q qlogger) logDatagram(name moqt.ObjectDatagramEventName, d *wire2.ObjectDatagram) {
	if !q.enabled() {
		return
	}
	q.log(moqt.ObjectDatagramEvent{
		EventName:              name,
		TrackAlias:             d.TrackAlias,
		GroupID:                d.GroupID,
		ObjectID:               d.ObjectID,
		PublisherPriority:      d.Priority,
		ExtensionHeadersLength: uint64(len(d.Properties)),
		ExtensionHeaders:       propertiesToQlog(d.Properties),
		ObjectStatus:           uint64(d.Status),
		Payload:                rawInfo(d.Payload),
	})
}

// logFetchObject records one record of a FETCH response.
func (q qlogger) logFetchObject(name moqt.FetchObjectEventName, stream streamIdentifier, obj *wire2.FetchObject) {
	if !q.enabled() {
		return
	}
	q.log(moqt.FetchObjectEvent{
		EventName:              name,
		StreamID:               streamIDOf(stream),
		GroupID:                obj.GroupID,
		SubgroupID:             obj.SubgroupID,
		ObjectID:               obj.ObjectID,
		PublisherPriority:      obj.Priority,
		ExtensionHeadersLength: uint64(len(obj.Properties)),
		ExtensionHeaders:       propertiesToQlog(obj.Properties),
		ObjectPayloadLength:    uint64(len(obj.Payload)),
		ObjectPayload:          rawInfo(obj.Payload),
	})
}

// controlMessageValue renders a control message for qlog.
//
// The fields are read by reflection rather than written out per message. There
// are twenty messages, the set changes with every draft, and a hand-written
// renderer per message is the kind of thing that silently stops mentioning a
// field somebody later needs. Reflection covers a draft-20 message set the day
// it is declared, without anyone remembering to.
type controlMessageValue struct {
	msg wire2.ControlMessage
}

func (v controlMessageValue) LogValue() slog.Value {
	attrs := []slog.Attr{slog.String("type", strings.ToLower(v.msg.Type().String()))}

	value := reflect.ValueOf(v.msg)
	for value.Kind() == reflect.Pointer {
		if value.IsNil() {
			return slog.GroupValue(attrs...)
		}
		value = value.Elem()
	}
	if value.Kind() != reflect.Struct {
		return slog.GroupValue(attrs...)
	}

	structType := value.Type()
	for i := range value.NumField() {
		field := structType.Field(i)
		if !field.IsExported() {
			continue
		}
		if attr, ok := fieldAttr(qlogFieldName(field.Name), value.Field(i)); ok {
			attrs = append(attrs, attr)
		}
	}
	return slog.GroupValue(attrs...)
}

// fieldAttr renders one message field, reporting false for one with nothing
// worth saying.
func fieldAttr(name string, value reflect.Value) (slog.Attr, bool) {
	switch typed := value.Interface().(type) {
	case wire2.Location:
		return slog.Any(name, slog.GroupValue(
			slog.Uint64("group", typed.Group),
			slog.Uint64("object", typed.Object),
		)), true

	case wire2.Parameters:
		if len(typed) == 0 {
			return slog.Attr{}, false
		}
		values := make([]any, 0, len(typed))
		for _, p := range typed {
			values = append(values, p.String())
		}
		return slog.Any(name, values), true

	case wire2.KVPList:
		if len(typed) == 0 {
			return slog.Attr{}, false
		}
		values := make([]any, 0, len(typed))
		for _, p := range typed {
			values = append(values, p.String())
		}
		return slog.Any(name, values), true

	case []byte:
		return slog.Any(name, rawInfo(typed)), true

	case [][]byte:
		// A Track Namespace. Its fields are text in every use anyone has, and
		// a list of strings is what makes a log readable.
		fields := make([]string, len(typed))
		for i, f := range typed {
			fields[i] = string(f)
		}
		return slog.Any(name, fields), true

	case string:
		if typed == "" {
			return slog.Attr{}, false
		}
		return slog.String(name, typed), true

	case bool:
		return slog.Bool(name, typed), true

	case uint8:
		return slog.Uint64(name, uint64(typed)), true

	case uint64:
		return slog.Uint64(name, typed), true
	}

	// Anything else -- a pointer to a substructure such as REQUEST_ERROR's
	// Redirect, or a typed enum -- renders through its own formatting.
	if value.Kind() == reflect.Pointer && value.IsNil() {
		return slog.Attr{}, false
	}
	return slog.Any(name, value.Interface()), true
}

// qlogFieldName converts a Go field name to the snake_case qlog uses.
func qlogFieldName(name string) string {
	var out strings.Builder
	runes := []rune(name)
	for i, r := range runes {
		isUpper := r >= 'A' && r <= 'Z'
		if isUpper && i > 0 {
			previousLower := runes[i-1] >= 'a' && runes[i-1] <= 'z'
			nextLower := i+1 < len(runes) && runes[i+1] >= 'a' && runes[i+1] <= 'z'
			// A boundary is lower->upper, or the end of an acronym run such as
			// the "ID" in RequestID or the "URI" in NewSessionURI.
			if previousLower || nextLower {
				out.WriteByte('_')
			}
		}
		if isUpper {
			out.WriteRune(r + ('a' - 'A'))
			continue
		}
		out.WriteRune(r)
	}
	return out.String()
}

func rawInfo(data []byte) qlog.RawInfo {
	return qlog.RawInfo{
		Length:        uint64(len(data)),
		PayloadLength: uint64(len(data)),
		Data:          data,
	}
}

// propertiesToQlog renders Object Properties for the object events.
//
// The qlog schema calls them extension headers, which is what draft-16 called
// them; draft-18 renamed them Properties without changing their shape, so the
// same field carries them.
func propertiesToQlog(properties KVPList) moqt.ExtensionHeaders {
	if len(properties) == 0 {
		return nil
	}
	headers := make(moqt.ExtensionHeaders, len(properties))
	for i, p := range properties {
		headers[i] = moqt.ExtensionHeader{
			HeaderType:   p.Type,
			HeaderValue:  p.ValueVarInt,
			HeaderLength: uint64(len(p.ValueBytes)),
			Payload:      rawInfo(p.ValueBytes),
		}
	}
	return headers
}
