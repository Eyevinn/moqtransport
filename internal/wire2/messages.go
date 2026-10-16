package wire2

//go:generate go run ../../wiregen -draft 18 -decl messages.go -dir .
//go:generate go run ../../wiregen -draft 22 -decl messages.go -dir .

// This file declares the message sets of draft-ietf-moq-transport-18 and -22.
// Each struct mirrors one figure in the drafts' control message and data
// stream sections; the `proto` tags drive wiregen, which writes one codec pair
// per draft into <message>_v18.go and <message>_v22.go. Fields without a
// `proto` tag are not serialized.
//
// The two drafts share these structs. Their bodies are nearly identical --
// what changed between them is almost entirely in the parameter registry,
// which the generated codecs consult per draft -- so a difference is recorded
// where it occurs rather than by declaring everything twice:
//
//   - a `//wiregen:drafts N` line limits a message to the drafts it exists in
//     (PUBLISH_STATE_NOTIFY is draft-22 only, PUBLISH_OK draft-18 only);
//   - a `drafts:"N"` tag limits a field the same way (GOAWAY's Request ID).
//
// A `max` tag records a bound the draft makes a MUST-close condition, so the
// generated parser enforces it: 1024 bytes for a Reason Phrase, 8192 for a New
// Session URI, and 32 fields for a Track Namespace.
//
// Section numbers below are draft-18's unless a draft is named: draft-21
// renumbered the document without changing these messages.
//
// FETCH is hand-written: its draft-18 body is conditional on a Fetch Type the
// generator cannot see, and draft-22 has a different body altogether. So are
// SUBGROUP_HEADER and the rest of the data plane.
//
// Both drafts keep almost nothing in the message bodies themselves:
// subscriber priority, group order, forwarding and the location filter all
// live in Parameters. That makes the parameter registry, not this file, where
// the semantics sit.

// Setup is the SETUP message (draft-18 Section 10.3, draft-22 Section 9.1).
// Setup Options span the whole message body and use a registry separate from
// Message Parameters. There is no version field: the version is negotiated
// purely through ALPN.
type Setup struct {
	Options KVPList `proto:"moq_kvp_list_no_length"`
}

func (m *Setup) Type() ControlMessageType { return ControlMessageTypeSetup }

// GoAwayCtrl is GOAWAY as sent on the control stream (draft-18 Section 10.4).
// Draft-18 gives it a Request ID field there; draft-19 removed it, so in
// draft-22 the two GOAWAY forms have the same body.
type GoAwayCtrl struct {
	NewSessionURI string `proto:"tlv_string" max:"8192"`
	Timeout       uint64 `proto:"varint"`
	RequestID     uint64 `proto:"varint" drafts:"18"`
}

func (m *GoAwayCtrl) Type() ControlMessageType { return ControlMessageTypeGoAway }

// GoAwayReq is GOAWAY as sent on a request stream, which migrates that single
// request and carries no Request ID.
type GoAwayReq struct {
	NewSessionURI string `proto:"tlv_string" max:"8192"`
	Timeout       uint64 `proto:"varint"`
}

func (m *GoAwayReq) Type() ControlMessageType { return ControlMessageTypeGoAway }

// Subscribe is the SUBSCRIBE message (Section 10.7), the first message on a
// subscribe request stream.
type Subscribe struct {
	RequestID      uint64     `proto:"varint"`
	TrackNamespace [][]byte   `proto:"ntlv_bytes" max:"32"`
	TrackName      []byte     `proto:"tlv_bytes"`
	Parameters     Parameters `proto:"moq_params"`
}

func (m *Subscribe) Type() ControlMessageType { return ControlMessageTypeSubscribe }

// SubscribeOk is the SUBSCRIBE_OK message (Section 10.8). Track Properties
// have no count or length prefix and run to the end of the body, which is what
// makes the 16-bit message length load-bearing.
type SubscribeOk struct {
	TrackAlias      uint64     `proto:"varint"`
	Parameters      Parameters `proto:"moq_params"`
	TrackProperties KVPList    `proto:"moq_kvp_list_no_length"`
}

func (m *SubscribeOk) Type() ControlMessageType { return ControlMessageTypeSubscribeOk }

// TrackStatus is the TRACK_STATUS message (Section 10.14). Its body is
// identical to SUBSCRIBE; only the codepoint and the response differ.
type TrackStatus struct {
	RequestID      uint64     `proto:"varint"`
	TrackNamespace [][]byte   `proto:"ntlv_bytes" max:"32"`
	TrackName      []byte     `proto:"tlv_bytes"`
	Parameters     Parameters `proto:"moq_params"`
}

func (m *TrackStatus) Type() ControlMessageType { return ControlMessageTypeTrackStatus }

// RequestUpdate is the REQUEST_UPDATE message (Section 10.9), sent by the
// originator of a request on that request's own stream.
type RequestUpdate struct {
	RequestID  uint64     `proto:"varint"`
	Parameters Parameters `proto:"moq_params"`
}

func (m *RequestUpdate) Type() ControlMessageType { return ControlMessageTypeRequestUpdate }

// Publish is the PUBLISH message (Section 10.10), the first message on a
// publish request stream.
type Publish struct {
	RequestID       uint64     `proto:"varint"`
	TrackNamespace  [][]byte   `proto:"ntlv_bytes" max:"32"`
	TrackName       []byte     `proto:"tlv_bytes"`
	TrackAlias      uint64     `proto:"varint"`
	Parameters      Parameters `proto:"moq_params"`
	TrackProperties KVPList    `proto:"moq_kvp_list_no_length"`
}

func (m *Publish) Type() ControlMessageType { return ControlMessageTypePublish }

// PublishDone is the PUBLISH_DONE message (Section 10.11).
type PublishDone struct {
	StatusCode  uint64 `proto:"varint"`
	StreamCount uint64 `proto:"varint"`
	ErrorReason string `proto:"tlv_string" max:"1024"`
}

func (m *PublishDone) Type() ControlMessageType { return ControlMessageTypePublishDone }

// PublishStateNotify is the PUBLISH_STATE_NOTIFY message (draft-22 Section
// 9.10): the publisher's unilateral report, on a subscription's own stream,
// that the subscription's state changed for a reason other than the
// subscriber's REQUEST_UPDATE. It carries only the parameters that changed,
// and is never answered.
//
//wiregen:drafts 22
type PublishStateNotify struct {
	Parameters Parameters `proto:"moq_params"`
}

func (m *PublishStateNotify) Type() ControlMessageType {
	return ControlMessageTypePublishStateNotify
}

// FetchOk is the FETCH_OK message (Section 10.13). Its End Location is
// encoded the same way in both drafts but means different things: the last
// Object plus one in draft-18, the last Object itself in draft-22.
type FetchOk struct {
	EndOfTrack      bool       `proto:"bool"`
	EndLocation     Location   `proto:"moq_location"`
	Parameters      Parameters `proto:"moq_params"`
	TrackProperties KVPList    `proto:"moq_kvp_list_no_length"`
}

func (m *FetchOk) Type() ControlMessageType { return ControlMessageTypeFetchOk }

// PublishNamespace is the PUBLISH_NAMESPACE message (Section 10.15).
type PublishNamespace struct {
	RequestID      uint64     `proto:"varint"`
	TrackNamespace [][]byte   `proto:"ntlv_bytes" max:"32"`
	Parameters     Parameters `proto:"moq_params"`
}

func (m *PublishNamespace) Type() ControlMessageType { return ControlMessageTypePublishNamespace }

// Namespace is the NAMESPACE message (Section 10.16), announcing a namespace
// on an established SUBSCRIBE_NAMESPACE request stream. The suffix is relative
// to that request's Track Namespace Prefix.
type Namespace struct {
	TrackNamespaceSuffix [][]byte `proto:"ntlv_bytes" max:"32"`
}

func (m *Namespace) Type() ControlMessageType { return ControlMessageTypeNamespace }

// NamespaceDone is the NAMESPACE_DONE message (Section 10.17).
type NamespaceDone struct {
	TrackNamespaceSuffix [][]byte `proto:"ntlv_bytes" max:"32"`
}

func (m *NamespaceDone) Type() ControlMessageType { return ControlMessageTypeNamespaceDone }

// SubscribeNamespace is the SUBSCRIBE_NAMESPACE message (Section 10.18), which
// asks for the set of matching namespaces and future updates to it.
type SubscribeNamespace struct {
	RequestID            uint64     `proto:"varint"`
	TrackNamespacePrefix [][]byte   `proto:"ntlv_bytes" max:"32"`
	Parameters           Parameters `proto:"moq_params"`
}

func (m *SubscribeNamespace) Type() ControlMessageType {
	return ControlMessageTypeSubscribeNamespace
}

// SubscribeTracks is the SUBSCRIBE_TRACKS message (Section 10.19). Draft-18
// split it out of SUBSCRIBE_NAMESPACE: the namespace form announces
// namespaces, this one asks the publisher to publish the matching tracks.
type SubscribeTracks struct {
	RequestID            uint64     `proto:"varint"`
	TrackNamespacePrefix [][]byte   `proto:"ntlv_bytes" max:"32"`
	Parameters           Parameters `proto:"moq_params"`
}

func (m *SubscribeTracks) Type() ControlMessageType { return ControlMessageTypeSubscribeTracks }

// PublishSkipped is the PUBLISH_SKIPPED message (draft-22 Section 9.19), sent
// on a SUBSCRIBE_TRACKS request stream for a track the publisher will not
// publish. Draft-18 calls it PUBLISH_BLOCKED; the codepoint and body are the
// same.
type PublishSkipped struct {
	TrackNamespaceSuffix [][]byte `proto:"ntlv_bytes" max:"32"`
	TrackName            []byte   `proto:"tlv_bytes"`
}

func (m *PublishSkipped) Type() ControlMessageType { return ControlMessageTypePublishSkipped }

// RequestOk is the REQUEST_OK message (Section 10.5), the success response to
// PUBLISH, REQUEST_UPDATE, TRACK_STATUS, SUBSCRIBE_NAMESPACE, SUBSCRIBE_TRACKS
// and PUBLISH_NAMESPACE. It carries no Request ID: the request stream it
// arrives on identifies the request.
//
// Track Properties are populated only in the TRACK_STATUS_OK case; receiving
// them in any of the others is a PROTOCOL_VIOLATION, which is a check for the
// handler that knows which request the stream belongs to.
type RequestOk struct {
	Parameters      Parameters `proto:"moq_params"`
	TrackProperties KVPList    `proto:"moq_kvp_list_no_length"`
}

func (m *RequestOk) Type() ControlMessageType { return ControlMessageTypeRequestOk }

// PublishOk is REQUEST_OK under draft-18's leftover 0x1E codepoint. See
// ControlMessageTypePublishOk: it exists so a peer that still sends 0x1E can
// be parsed, and is never sent by this implementation. Draft-19 reserved the
// codepoint, so in draft-22 it is an unknown message.
//
//wiregen:drafts 18
type PublishOk struct {
	Parameters      Parameters `proto:"moq_params"`
	TrackProperties KVPList    `proto:"moq_kvp_list_no_length"`
}

func (m *PublishOk) Type() ControlMessageType { return ControlMessageTypePublishOk }

// RequestError is the REQUEST_ERROR message (Section 10.6). Redirect is
// present only when ErrorCode is REDIRECT, and being the last field, the
// message length tells the parser whether it is there.
type RequestError struct {
	ErrorCode     uint64    `proto:"varint"`
	RetryInterval uint64    `proto:"varint"`
	ErrorReason   string    `proto:"tlv_string" max:"1024"`
	Redirect      *Redirect `proto:"moq_opt_redirect"`
}

func (m *RequestError) Type() ControlMessageType { return ControlMessageTypeRequestError }

// FetchHeader opens the unidirectional stream carrying a FETCH response
// (Section 11.4.4). It is a stream type, not a control message, and shares the
// codepoint 0x5 with REQUEST_ERROR on request streams.
type FetchHeader struct {
	RequestID uint64 `proto:"varint"`
}

func (m *FetchHeader) Type() ControlMessageType {
	return ControlMessageType(StreamTypeFetchHeader)
}

// Padding opens a PADDING stream (Section 11.5.1). Everything after the stream
// type is padding to be discarded, so the message itself has no fields.
type Padding struct{}

func (m *Padding) Type() ControlMessageType {
	return ControlMessageType(StreamTypePadding)
}

// Every declaration must carry a complete codec for each draft it exists in.
// FetchHeader and Padding are stream headers rather than control messages,
// but they are framed and parsed the same way, so they satisfy the same
// interface.
var _ = []messageV18{
	(*Setup)(nil),
	(*GoAwayCtrl)(nil),
	(*GoAwayReq)(nil),
	(*Subscribe)(nil),
	(*SubscribeOk)(nil),
	(*TrackStatus)(nil),
	(*RequestUpdate)(nil),
	(*Publish)(nil),
	(*PublishDone)(nil),
	(*Fetch)(nil),
	(*FetchOk)(nil),
	(*PublishNamespace)(nil),
	(*Namespace)(nil),
	(*NamespaceDone)(nil),
	(*SubscribeNamespace)(nil),
	(*SubscribeTracks)(nil),
	(*PublishSkipped)(nil),
	(*RequestOk)(nil),
	(*PublishOk)(nil),
	(*RequestError)(nil),
	(*FetchHeader)(nil),
	(*Padding)(nil),
}

var _ = []messageV22{
	(*Setup)(nil),
	(*GoAwayCtrl)(nil),
	(*GoAwayReq)(nil),
	(*Subscribe)(nil),
	(*SubscribeOk)(nil),
	(*TrackStatus)(nil),
	(*RequestUpdate)(nil),
	(*Publish)(nil),
	(*PublishDone)(nil),
	(*PublishStateNotify)(nil),
	(*Fetch)(nil),
	(*FetchOk)(nil),
	(*PublishNamespace)(nil),
	(*Namespace)(nil),
	(*NamespaceDone)(nil),
	(*SubscribeNamespace)(nil),
	(*SubscribeTracks)(nil),
	(*PublishSkipped)(nil),
	(*RequestOk)(nil),
	(*RequestError)(nil),
	(*FetchHeader)(nil),
	(*Padding)(nil),
}
