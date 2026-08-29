package wire2

import (
	"io"

	"github.com/Eyevinn/locmaf/vi64"
)

// maxConnectURILength is the largest Connect URI draft-18 allows in a
// Redirect, matching the New Session URI bound in GOAWAY.
const maxConnectURILength = 8192

// Redirect is the structure REQUEST_ERROR carries when the Error Code is
// REDIRECT (draft-ietf-moq-transport-18, Section 10.6.1).
//
// A zero-length ConnectURI means the current session's URI. A zero-length
// TrackNamespace and TrackName together mean the original request's values.
type Redirect struct {
	ConnectURI     string
	TrackNamespace [][]byte
	TrackName      []byte
}

func (r *Redirect) append(buf []byte) []byte {
	buf = vi64.Append(buf, uint64(len(r.ConnectURI)))
	buf = append(buf, r.ConnectURI...)
	buf = vi64.Append(buf, uint64(len(r.TrackNamespace)))
	for _, f := range r.TrackNamespace {
		buf = vi64.Append(buf, uint64(len(f)))
		buf = append(buf, f...)
	}
	buf = vi64.Append(buf, uint64(len(r.TrackName)))
	return append(buf, r.TrackName...)
}

func (r *Redirect) parse(data []byte) (int, error) {
	uri, parsed, err := parseByteString(data, maxConnectURILength)
	if err != nil {
		return parsed, err
	}
	r.ConnectURI = string(uri)

	count, n, err := vi64.Parse(data[parsed:])
	parsed += n
	if err != nil {
		return parsed, err
	}
	if count > maxNamespaceFields {
		return parsed, errTooManyFields
	}
	if count > uint64(len(data)-parsed) {
		return parsed, io.ErrUnexpectedEOF
	}
	r.TrackNamespace = make([][]byte, 0, count)
	for range count {
		field, n, err := parseByteString(data[parsed:], 0)
		parsed += n
		if err != nil {
			return parsed, err
		}
		r.TrackNamespace = append(r.TrackNamespace, field)
	}

	name, n, err := parseByteString(data[parsed:], 0)
	parsed += n
	if err != nil {
		return parsed, err
	}
	r.TrackName = name
	return parsed, nil
}

// maxNamespaceFields is the largest number of fields a Track Namespace may
// have (draft-ietf-moq-transport-18, Section 2.4.1).
const maxNamespaceFields = 32

// parseByteString reads a vi64-length-prefixed byte string and returns it
// along with the bytes consumed. A max of 0 means unbounded. The result
// aliases data, which the caller owns for the lifetime of the message.
func parseByteString(data []byte, max uint64) ([]byte, int, error) {
	length, parsed, err := vi64.Parse(data)
	if err != nil {
		return nil, parsed, err
	}
	if max != 0 && length > max {
		return nil, parsed, errFieldTooLong
	}
	if uint64(len(data)-parsed) < length {
		return nil, parsed, io.ErrUnexpectedEOF
	}
	return data[parsed : parsed+int(length)], parsed + int(length), nil
}
