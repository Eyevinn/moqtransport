package wire2

import "fmt"

// ControlMessageType is a MOQT message type codepoint
// (draft-ietf-moq-transport-18, Table 5).
//
// Codepoints are scoped to the kind of stream they arrive on, so this is not a
// flat table: 0x5 is REQUEST_ERROR on a request stream and FETCH_HEADER on a
// unidirectional data stream. Dispatch must know the stream kind first.
type ControlMessageType uint64

const (
	// Sent on the control stream.
	ControlMessageTypeSetup ControlMessageType = 0x2F00

	// Sent on either the control stream or a request stream. The Request ID
	// field is present only in the control-stream form, which is why GoAwayCtrl
	// and GoAwayReq are separate declarations.
	ControlMessageTypeGoAway ControlMessageType = 0x10

	// Open a request stream; they MUST be the first message on it.
	ControlMessageTypeSubscribe          ControlMessageType = 0x3
	ControlMessageTypePublish            ControlMessageType = 0x1D
	ControlMessageTypeFetch              ControlMessageType = 0x16
	ControlMessageTypeTrackStatus        ControlMessageType = 0xD
	ControlMessageTypePublishNamespace   ControlMessageType = 0x6
	ControlMessageTypeSubscribeNamespace ControlMessageType = 0x50
	ControlMessageTypeSubscribeTracks    ControlMessageType = 0x51

	// Sent on an established request stream.
	ControlMessageTypeRequestUpdate  ControlMessageType = 0x2
	ControlMessageTypeRequestOk      ControlMessageType = 0x7
	ControlMessageTypeRequestError   ControlMessageType = 0x5
	ControlMessageTypeSubscribeOk    ControlMessageType = 0x4
	ControlMessageTypeFetchOk        ControlMessageType = 0x18
	ControlMessageTypePublishDone    ControlMessageType = 0xB
	ControlMessageTypeNamespace      ControlMessageType = 0x8
	ControlMessageTypeNamespaceDone  ControlMessageType = 0xE
	ControlMessageTypePublishBlocked ControlMessageType = 0xF

	// ControlMessageTypePublishOk is draft-18's leftover codepoint for
	// PUBLISH_OK. Section 10.5 makes PUBLISH_OK a shorthand for REQUEST_OK and
	// gives it REQUEST_OK's body, but Table 5 still lists 0x1E; draft-19
	// reserves it. A draft-18 peer may send either, so the parser accepts both
	// and this implementation sends 0x7.
	ControlMessageTypePublishOk ControlMessageType = 0x1E
)

// OpensRequestStream reports whether t is one of the seven message types that
// may be the first message on a bidirectional stream
// (draft-ietf-moq-transport-18, Section 3.3).
//
// A bidirectional stream beginning with anything else is a PROTOCOL_VIOLATION,
// including a message type that is perfectly valid later on the same stream:
// REQUEST_OK opening a stream is as much a violation as an unknown codepoint,
// so dispatch has to ask this question separately from "do I know this type".
func (t ControlMessageType) OpensRequestStream() bool {
	switch t {
	case ControlMessageTypeTrackStatus,
		ControlMessageTypeSubscribe,
		ControlMessageTypePublish,
		ControlMessageTypeFetch,
		ControlMessageTypePublishNamespace,
		ControlMessageTypeSubscribeNamespace,
		ControlMessageTypeSubscribeTracks:
		return true
	}
	return false
}

func (t ControlMessageType) String() string {
	switch t {
	case ControlMessageTypeSetup:
		return "SETUP"
	case ControlMessageTypeGoAway:
		return "GOAWAY"
	case ControlMessageTypeSubscribe:
		return "SUBSCRIBE"
	case ControlMessageTypeSubscribeOk:
		return "SUBSCRIBE_OK"
	case ControlMessageTypePublish:
		return "PUBLISH"
	case ControlMessageTypePublishOk:
		return "PUBLISH_OK"
	case ControlMessageTypePublishDone:
		return "PUBLISH_DONE"
	case ControlMessageTypeFetch:
		return "FETCH"
	case ControlMessageTypeFetchOk:
		return "FETCH_OK"
	case ControlMessageTypeTrackStatus:
		return "TRACK_STATUS"
	case ControlMessageTypePublishNamespace:
		return "PUBLISH_NAMESPACE"
	case ControlMessageTypeSubscribeNamespace:
		return "SUBSCRIBE_NAMESPACE"
	case ControlMessageTypeSubscribeTracks:
		return "SUBSCRIBE_TRACKS"
	case ControlMessageTypeNamespace:
		return "NAMESPACE"
	case ControlMessageTypeNamespaceDone:
		return "NAMESPACE_DONE"
	case ControlMessageTypePublishBlocked:
		return "PUBLISH_BLOCKED"
	case ControlMessageTypeRequestUpdate:
		return "REQUEST_UPDATE"
	case ControlMessageTypeRequestOk:
		return "REQUEST_OK"
	case ControlMessageTypeRequestError:
		return "REQUEST_ERROR"
	}
	return fmt.Sprintf("unknown control message type: %v", uint64(t))
}

// StreamScope is the codepoint namespace a control message is dispatched in.
//
// draft-18 reuses codepoints across stream kinds, so there is no single flat
// message table: 0x10 is GOAWAY on both, but carries a Request ID only on the
// control stream, and 0x5 is REQUEST_ERROR on a request stream while naming
// FETCH_HEADER as a unidirectional stream type. A parser that does not know
// which stream it is reading cannot decode correctly.
//
// Unidirectional data streams are not framed this way at all -- their headers
// have no Length field -- so they are classified by ClassifyStreamType rather
// than parsed here.
type StreamScope uint8

const (
	// ScopeControl is the unidirectional control stream pair, which carries
	// SETUP and GOAWAY.
	ScopeControl StreamScope = iota
	// ScopeRequest is a per-request bidirectional stream.
	ScopeRequest
)

func (s StreamScope) String() string {
	if s == ScopeControl {
		return "control"
	}
	return "request"
}

// StreamType is the leading varint of a unidirectional stream
// (draft-ietf-moq-transport-18, Table 3).
type StreamType uint64

const (
	StreamTypeFetchHeader StreamType = 0x05
	StreamTypeSetup       StreamType = 0x2F00
	StreamTypePadding     StreamType = 0x132B3E28
)

// A SUBGROUP_HEADER stream type is not a single codepoint but the bit pattern
// 0b0XX1XXXX -- bit 7 clear and bit 4 set, i.e. 0x10-0x1F, 0x30-0x3F, 0x50-0x5F
// and 0x70-0x7F -- so it is matched by mask rather than compared.
const (
	subgroupHeaderMask  = 0b1001_0000
	subgroupHeaderValue = 0b0001_0000
)

// IsSubgroupHeader reports whether t opens a subgroup data stream. It answers
// only the dispatch question; not every value in the range is a valid header,
// and parsing the header rejects the rest.
func (t StreamType) IsSubgroupHeader() bool {
	return t < 0x80 && uint64(t)&subgroupHeaderMask == subgroupHeaderValue
}

// ControlMessage is a message that carries a type codepoint. The append and
// parse methods are per-draft and generated, so they are not part of this
// interface; see messageV18.
type ControlMessage interface {
	Type() ControlMessageType
}

// MessageV18 is the codec surface every draft-18 message declaration has,
// whether generated by wiregen or hand-written. Its codec methods are
// unexported, so the set of messages is closed to this package while callers
// elsewhere can still name the type.
type MessageV18 interface {
	ControlMessage
	appendV18(buf []byte) ([]byte, error)
	parseV18(data []byte) error
}
