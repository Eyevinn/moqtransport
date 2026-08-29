package wire2

import (
	"bufio"
	"encoding/binary"
	"io"

	"github.com/Eyevinn/locmaf/vi64"
)

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

// ControlMessageParser reads length-framed control messages from one stream.
type ControlMessageParser struct {
	reader *bufio.Reader
	scope  StreamScope
}

// NewControlMessageParser returns a parser reading messages of scope from r.
func NewControlMessageParser(r io.Reader, scope StreamScope) *ControlMessageParser {
	return &ControlMessageParser{
		reader: bufio.NewReader(r),
		scope:  scope,
	}
}

// Parse reads the next control message. It returns io.EOF, and only io.EOF,
// when the stream ends cleanly between messages; a stream that ends part-way
// through one gives io.ErrUnexpectedEOF.
//
// Every other error is a MUST-close condition: an unknown message type, a body
// that does not decode, or a body whose length disagrees with the Message
// Length field, all of which draft-18 makes a PROTOCOL_VIOLATION.
func (p *ControlMessageParser) Parse() (ControlMessage, error) {
	messageType, err := vi64.Read(p.reader)
	if err != nil {
		return nil, err
	}

	var lengthBytes [2]byte
	if _, err := io.ReadFull(p.reader, lengthBytes[:]); err != nil {
		return nil, unexpectedEOF(err)
	}
	length := binary.BigEndian.Uint16(lengthBytes[:])

	// The body is read into a buffer of exactly the declared length, so a
	// message that wants more bytes than were declared fails with
	// io.ErrUnexpectedEOF and one that leaves bytes over fails with
	// errTrailingBytes. Both are the length check draft-18 requires.
	body := make([]byte, length)
	if _, err := io.ReadFull(p.reader, body); err != nil {
		return nil, unexpectedEOF(err)
	}

	msg, err := newControlMessage(p.scope, ControlMessageType(messageType))
	if err != nil {
		return nil, err
	}
	if err := msg.parseV18(body); err != nil {
		return nil, err
	}
	return msg, nil
}

// unexpectedEOF maps a clean end of stream inside a message to
// io.ErrUnexpectedEOF, keeping io.EOF meaning "cleanly between messages".
func unexpectedEOF(err error) error {
	if err == io.EOF {
		return io.ErrUnexpectedEOF
	}
	return err
}

// newControlMessage returns an empty message of the type t names within scope.
// An unknown type is an error: draft-18 requires the session to be closed,
// because no control message is intended to be ignorable.
func newControlMessage(scope StreamScope, t ControlMessageType) (MessageV18, error) {
	if scope == ScopeControl {
		switch t {
		case ControlMessageTypeSetup:
			return &Setup{}, nil
		case ControlMessageTypeGoAway:
			return &GoAwayCtrl{}, nil
		}
		return nil, unknownControlMessageTypeError{scope: scope, messageType: t}
	}

	switch t {
	case ControlMessageTypeGoAway:
		// On a request stream GOAWAY migrates that one request and carries no
		// Request ID.
		return &GoAwayReq{}, nil

	case ControlMessageTypeSubscribe:
		return &Subscribe{}, nil
	case ControlMessageTypeSubscribeOk:
		return &SubscribeOk{}, nil
	case ControlMessageTypePublish:
		return &Publish{}, nil
	case ControlMessageTypePublishOk:
		return &PublishOk{}, nil
	case ControlMessageTypePublishDone:
		return &PublishDone{}, nil
	case ControlMessageTypeFetch:
		return &Fetch{}, nil
	case ControlMessageTypeFetchOk:
		return &FetchOk{}, nil
	case ControlMessageTypeTrackStatus:
		return &TrackStatus{}, nil
	case ControlMessageTypePublishNamespace:
		return &PublishNamespace{}, nil
	case ControlMessageTypeSubscribeNamespace:
		return &SubscribeNamespace{}, nil
	case ControlMessageTypeSubscribeTracks:
		return &SubscribeTracks{}, nil
	case ControlMessageTypeNamespace:
		return &Namespace{}, nil
	case ControlMessageTypeNamespaceDone:
		return &NamespaceDone{}, nil
	case ControlMessageTypePublishBlocked:
		return &PublishBlocked{}, nil
	case ControlMessageTypeRequestUpdate:
		return &RequestUpdate{}, nil
	case ControlMessageTypeRequestOk:
		return &RequestOk{}, nil
	case ControlMessageTypeRequestError:
		return &RequestError{}, nil
	}
	return nil, unknownControlMessageTypeError{scope: scope, messageType: t}
}

// UniStreamKind is what the leading varint of a unidirectional stream says the
// stream is. This is the third dispatch table, alongside the two control
// scopes, and it does not overlap with them: 0x5 here is FETCH_HEADER, not
// REQUEST_ERROR.
type UniStreamKind uint8

const (
	// UniStreamControl is the peer's half of the control stream pair, which
	// opens with SETUP.
	UniStreamControl UniStreamKind = iota
	UniStreamFetch
	UniStreamSubgroup
	UniStreamPadding
)

func (k UniStreamKind) String() string {
	switch k {
	case UniStreamControl:
		return "control"
	case UniStreamFetch:
		return "fetch"
	case UniStreamSubgroup:
		return "subgroup"
	case UniStreamPadding:
		return "padding"
	}
	return "unknown"
}

// ClassifyStreamType maps a unidirectional stream's leading varint to the kind
// of stream it opens (draft-ietf-moq-transport-18, Table 3). An unknown stream
// type is an error; the session MUST be closed.
//
// Classification lives here rather than in the session so that the whole
// stream-type table sits next to the message tables it shares codepoints with.
func ClassifyStreamType(t StreamType) (UniStreamKind, error) {
	switch t {
	case StreamTypeSetup:
		return UniStreamControl, nil
	case StreamTypeFetchHeader:
		return UniStreamFetch, nil
	case StreamTypePadding:
		return UniStreamPadding, nil
	}
	if t.IsSubgroupHeader() {
		return UniStreamSubgroup, nil
	}
	return 0, unknownStreamTypeError{streamType: t}
}

// ReadStreamType reads and classifies the leading varint of a unidirectional
// stream. The stream type is returned as well, since a SUBGROUP_HEADER's type
// is itself the header's bitfield and the header parser needs it.
func ReadStreamType(r io.ByteReader) (StreamType, UniStreamKind, error) {
	v, err := vi64.Read(r)
	if err != nil {
		return 0, 0, err
	}
	t := StreamType(v)
	kind, err := ClassifyStreamType(t)
	return t, kind, err
}
