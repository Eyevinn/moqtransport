package wire2

import (
	"io"

	"github.com/Eyevinn/locmaf/vi64"
)

// FetchType selects which optional structure a draft-18 FETCH message body
// carries (draft-ietf-moq-transport-18, Section 10.12). Draft-22 has no such
// field; every draft-22 FETCH is standalone.
type FetchType uint64

const (
	FetchTypeStandalone      FetchType = 0x1
	FetchTypeRelativeJoining FetchType = 0x2
	FetchTypeAbsoluteJoining FetchType = 0x3
)

func (t FetchType) String() string {
	switch t {
	case FetchTypeStandalone:
		return "STANDALONE"
	case FetchTypeRelativeJoining:
		return "RELATIVE_JOINING"
	case FetchTypeAbsoluteJoining:
		return "ABSOLUTE_JOINING"
	}
	return "UNKNOWN"
}

// AppendFetchHeader writes the stream type and Request ID that together open a
// FETCH response stream (Section 11.4.4). In draft-22 the same header opens a
// fill fetch stream, carrying the Request ID of the SUBSCRIBE or
// REQUEST_UPDATE that asked for the fill.
//
// It is not a control message: a data stream header carries no Message Length,
// so it cannot go through AppendControlMessage even though its codepoint sits
// in the same numeric space.
func AppendFetchHeader(buf []byte, requestID uint64) []byte {
	buf = vi64.Append(buf, uint64(StreamTypeFetchHeader))
	return vi64.Append(buf, requestID)
}

// ParseFetchHeader reads the Request ID that follows a FETCH_HEADER stream
// type the caller has already read.
func ParseFetchHeader(r io.ByteReader) (uint64, error) {
	requestID, err := vi64.Read(r)
	if err != nil {
		return 0, unexpectedEOF(err)
	}
	return requestID, nil
}

// JoiningFetch references a subscription on the same session, and is present
// when FetchType is FetchTypeRelativeJoining or FetchTypeAbsoluteJoining. The
// publisher derives the range from that subscription, so the fetched and
// subscribed Objects are contiguous and do not overlap. Draft-18 only:
// draft-20 replaced joining with fill fetch streams.
type JoiningFetch struct {
	JoiningRequestID uint64
	JoiningStart     uint64
}

// Fetch is the FETCH message (draft-18 Section 10.12, draft-22 Section 9.11).
//
// The two drafts' bodies differ. Draft-18 has a Fetch Type selecting either a
// standalone body -- track name and explicit Start and End Locations -- or a
// joining one naming a subscription. Draft-22 has no Fetch Type and no range
// fields: every FETCH is standalone, and its range is the LOCATION_FILTER
// parameter. The struct is the union; each codec writes its own part, and the
// draft-22 codec reports FetchTypeStandalone on parse so callers can switch on
// FetchType regardless of draft.
//
// Its codecs are hand-written rather than generated: the draft-18 body is
// conditional on a sibling field, and wiregen codecs see one field at a time.
// Do not add Fetch to wiregen's message list.
type Fetch struct {
	RequestID uint64
	FetchType FetchType

	// TrackNamespace and TrackName name the track of a standalone FETCH.
	TrackNamespace [][]byte
	TrackName      []byte

	// StartLocation and EndLocation are draft-18's explicit range. EndLocation
	// is the last Object plus one; an Object of 0 means the whole Group.
	// Draft-22 carries the range in LOCATION_FILTER instead, and ignores
	// these.
	StartLocation Location
	EndLocation   Location

	// Joining is present for draft-18's two joining types.
	Joining *JoiningFetch

	Parameters Parameters
}

func (m *Fetch) Type() ControlMessageType { return ControlMessageTypeFetch }

func (m *Fetch) appendV18(buf []byte) ([]byte, error) {
	buf = vi64.Append(buf, m.RequestID)
	buf = vi64.Append(buf, uint64(m.FetchType))
	switch m.FetchType {
	case FetchTypeStandalone:
		buf = appendTrackName(buf, m.TrackNamespace, m.TrackName)
		buf = m.StartLocation.append(buf)
		buf = m.EndLocation.append(buf)
	case FetchTypeRelativeJoining, FetchTypeAbsoluteJoining:
		if m.Joining == nil {
			return nil, errJoiningFetchWithoutSubscription
		}
		buf = vi64.Append(buf, m.Joining.JoiningRequestID)
		buf = vi64.Append(buf, m.Joining.JoiningStart)
	default:
		return nil, errInvalidFetchType
	}
	return m.Parameters.appendNum(buf, Version18)
}

func (m *Fetch) parseV18(data []byte) error {
	requestID, parsed, err := vi64.Parse(data)
	if err != nil {
		return err
	}
	m.RequestID = requestID

	fetchType, n, err := vi64.Parse(data[parsed:])
	parsed += n
	if err != nil {
		return err
	}
	m.FetchType = FetchType(fetchType)

	switch m.FetchType {
	case FetchTypeStandalone:
		n, err := m.parseTrackName(data[parsed:])
		parsed += n
		if err != nil {
			return err
		}
		n, err = m.StartLocation.parse(data[parsed:])
		parsed += n
		if err != nil {
			return err
		}
		n, err = m.EndLocation.parse(data[parsed:])
		parsed += n
		if err != nil {
			return err
		}
	case FetchTypeRelativeJoining, FetchTypeAbsoluteJoining:
		n, err := m.parseJoining(data[parsed:])
		parsed += n
		if err != nil {
			return err
		}
	default:
		return errInvalidFetchType
	}

	n, err = m.Parameters.parseNum(data[parsed:], Version18)
	parsed += n
	if err != nil {
		return err
	}
	if parsed != len(data) {
		return errTrailingBytes
	}
	return nil
}

// appendV22 writes draft-22's body: Request ID, track name, Parameters. A
// joining FETCH has no draft-22 form.
func (m *Fetch) appendV22(buf []byte) ([]byte, error) {
	if m.FetchType != FetchTypeStandalone && m.FetchType != 0 {
		return nil, errJoiningFetchNotInDraft
	}
	buf = vi64.Append(buf, m.RequestID)
	buf = appendTrackName(buf, m.TrackNamespace, m.TrackName)
	return m.Parameters.appendNum(buf, Version22)
}

func (m *Fetch) parseV22(data []byte) error {
	requestID, parsed, err := vi64.Parse(data)
	if err != nil {
		return err
	}
	m.RequestID = requestID
	m.FetchType = FetchTypeStandalone

	n, err := m.parseTrackName(data[parsed:])
	parsed += n
	if err != nil {
		return err
	}
	n, err = m.Parameters.parseNum(data[parsed:], Version22)
	parsed += n
	if err != nil {
		return err
	}
	if parsed != len(data) {
		return errTrailingBytes
	}
	return nil
}

func appendTrackName(buf []byte, namespace [][]byte, name []byte) []byte {
	buf = vi64.Append(buf, uint64(len(namespace)))
	for _, f := range namespace {
		buf = vi64.Append(buf, uint64(len(f)))
		buf = append(buf, f...)
	}
	buf = vi64.Append(buf, uint64(len(name)))
	return append(buf, name...)
}

// parseTrackName reads a Track Namespace and a Track Name.
func (m *Fetch) parseTrackName(data []byte) (int, error) {
	count, parsed, err := vi64.Parse(data)
	if err != nil {
		return parsed, err
	}
	if count > maxNamespaceFields {
		return parsed, errTooManyFields
	}
	if count > uint64(len(data)-parsed) {
		return parsed, io.ErrUnexpectedEOF
	}
	m.TrackNamespace = make([][]byte, 0, count)
	for range count {
		field, n, err := parseByteString(data[parsed:], 0)
		parsed += n
		if err != nil {
			return parsed, err
		}
		m.TrackNamespace = append(m.TrackNamespace, field)
	}

	name, n, err := parseByteString(data[parsed:], 0)
	parsed += n
	if err != nil {
		return parsed, err
	}
	m.TrackName = name
	return parsed, nil
}

func (m *Fetch) parseJoining(data []byte) (int, error) {
	var j JoiningFetch

	requestID, parsed, err := vi64.Parse(data)
	if err != nil {
		return parsed, err
	}
	j.JoiningRequestID = requestID

	start, n, err := vi64.Parse(data[parsed:])
	parsed += n
	if err != nil {
		return parsed, err
	}
	j.JoiningStart = start

	m.Joining = &j
	return parsed, nil
}
