package wire2

import (
	"io"

	"github.com/Eyevinn/locmaf/vi64"
)

// FetchType selects which optional structure a FETCH message body carries
// (draft-ietf-moq-transport-18, Section 10.12).
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
// FETCH response stream (Section 11.4.4).
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

// StandaloneFetch names a track and an explicit range, and is present when
// FetchType is FetchTypeStandalone.
type StandaloneFetch struct {
	TrackNamespace [][]byte
	TrackName      []byte
	StartLocation  Location
	// EndLocation is the last Object plus one; an Object of 0 means the whole
	// group is requested.
	EndLocation Location
}

// JoiningFetch references a subscription on the same session, and is present
// when FetchType is FetchTypeRelativeJoining or FetchTypeAbsoluteJoining. The
// publisher derives the range from that subscription, so the fetched and
// subscribed Objects are contiguous and do not overlap.
type JoiningFetch struct {
	JoiningRequestID uint64
	JoiningStart     uint64
}

// Fetch is the FETCH message (Section 10.12).
//
// Its codec is hand-written rather than generated: which of the two optional
// structures appears is decided by FetchType, a sibling field, and wiregen
// codecs see one field at a time. Do not add Fetch to wiregen's message list.
type Fetch struct {
	RequestID  uint64
	FetchType  FetchType
	Standalone *StandaloneFetch
	Joining    *JoiningFetch
	Parameters Parameters
}

func (m *Fetch) Type() ControlMessageType { return ControlMessageTypeFetch }

func (m *Fetch) appendV18(buf []byte) ([]byte, error) {
	buf = vi64.Append(buf, m.RequestID)
	buf = vi64.Append(buf, uint64(m.FetchType))
	if m.FetchType == FetchTypeStandalone && m.Standalone != nil {
		s := m.Standalone
		buf = vi64.Append(buf, uint64(len(s.TrackNamespace)))
		for _, f := range s.TrackNamespace {
			buf = vi64.Append(buf, uint64(len(f)))
			buf = append(buf, f...)
		}
		buf = vi64.Append(buf, uint64(len(s.TrackName)))
		buf = append(buf, s.TrackName...)
		buf = s.StartLocation.append(buf)
		buf = s.EndLocation.append(buf)
	}
	if m.FetchType != FetchTypeStandalone && m.Joining != nil {
		buf = vi64.Append(buf, m.Joining.JoiningRequestID)
		buf = vi64.Append(buf, m.Joining.JoiningStart)
	}
	return m.Parameters.appendNum(buf)
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
		n, err := m.parseStandalone(data[parsed:])
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

	n, err = m.Parameters.parseNum(data[parsed:])
	parsed += n
	if err != nil {
		return err
	}
	if parsed != len(data) {
		return errTrailingBytes
	}
	return nil
}

func (m *Fetch) parseStandalone(data []byte) (int, error) {
	var s StandaloneFetch

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
	s.TrackNamespace = make([][]byte, 0, count)
	for range count {
		field, n, err := parseByteString(data[parsed:], 0)
		parsed += n
		if err != nil {
			return parsed, err
		}
		s.TrackNamespace = append(s.TrackNamespace, field)
	}

	name, n, err := parseByteString(data[parsed:], 0)
	parsed += n
	if err != nil {
		return parsed, err
	}
	s.TrackName = name

	n, err = s.StartLocation.parse(data[parsed:])
	parsed += n
	if err != nil {
		return parsed, err
	}
	n, err = s.EndLocation.parse(data[parsed:])
	parsed += n
	if err != nil {
		return parsed, err
	}

	m.Standalone = &s
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
