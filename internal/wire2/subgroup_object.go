package wire2

import (
	"github.com/Eyevinn/locmaf/vi64"
)

// SubgroupObject is one Object on a subgroup stream, with the wire format's
// delta encoding already resolved (Section 11.4.2, Figure 25).
type SubgroupObject struct {
	ObjectID uint64
	// Properties is present only on streams whose header set the PROPERTIES
	// bit, and only Normal Objects may carry any.
	Properties KVPList
	// Status is meaningful only for an Object with an empty payload; a
	// non-empty payload is Normal by construction and carries no status field.
	Status  ObjectStatus
	Payload []byte
}

// SubgroupReader reads Objects from a stream opened with SUBGROUP_HEADER.
//
// It holds the state the wire format spreads across the whole stream: Object
// IDs are deltas against the previous Object, the Properties block is present
// only if the *header* said so, and under SubgroupIDFirstObject the Subgroup ID
// is unknown until the first Object has been read. Keeping all three in one
// place is what stops each of them becoming a bug in the session layer.
type SubgroupReader struct {
	// Header is the stream's header. Its SubgroupID is filled in once the
	// first Object arrives, when the mode says the first Object ID is it.
	Header *SubgroupHeader

	r            byteReader
	prevObjectID uint64
	haveObject   bool
}

// NewSubgroupReader returns a reader for the Objects that follow h on r.
func NewSubgroupReader(h *SubgroupHeader, r byteReader) *SubgroupReader {
	return &SubgroupReader{Header: h, r: r}
}

// Next reads the next Object. It returns io.EOF, and only io.EOF, when the
// stream ends cleanly between Objects; a stream that ends part-way through one
// gives io.ErrUnexpectedEOF, which Section 11.4 makes a PROTOCOL_VIOLATION.
func (s *SubgroupReader) Next() (*SubgroupObject, error) {
	delta, err := vi64.Read(s.r)
	if err != nil {
		// A clean end here is the end of the subgroup, not a truncation.
		return nil, err
	}

	obj := &SubgroupObject{}
	if s.haveObject {
		if obj.ObjectID, err = addObjectID(s.prevObjectID, delta); err != nil {
			return nil, err
		}
	} else {
		// The first Object's delta is the Object ID itself, so a subgroup of
		// sequential IDs starting at 0 writes 0 for every delta.
		obj.ObjectID = delta
	}

	if s.Header.HasProperties {
		if obj.Properties, err = readProperties(s.r); err != nil {
			return nil, err
		}
	}

	payloadLen, err := vi64.Read(s.r)
	if err != nil {
		return nil, unexpectedEOF(err)
	}
	if payloadLen == 0 {
		status, err := vi64.Read(s.r)
		if err != nil {
			return nil, unexpectedEOF(err)
		}
		obj.Status = ObjectStatus(status)
		if !obj.Status.valid() {
			return nil, errInvalidObjectStatus
		}
		if obj.Status != ObjectStatusNormal && len(obj.Properties) > 0 {
			return nil, errPropertiesOnNonNormalObject
		}
	} else if obj.Payload, err = readPayload(s.r, payloadLen); err != nil {
		return nil, err
	}

	if !s.haveObject && s.Header.SubgroupIDMode == SubgroupIDFirstObject {
		s.Header.SubgroupID = obj.ObjectID
	}
	s.prevObjectID = obj.ObjectID
	s.haveObject = true
	return obj, nil
}

// SubgroupWriter serializes Objects onto a subgroup stream, carrying the same
// state SubgroupReader does so that the two stay symmetric.
type SubgroupWriter struct {
	Header *SubgroupHeader

	prevObjectID uint64
	haveObject   bool
}

// NewSubgroupWriter returns a writer for Objects on a stream opened with h.
func NewSubgroupWriter(h *SubgroupHeader) *SubgroupWriter {
	return &SubgroupWriter{Header: h}
}

// AppendObject writes obj onto buf. Object IDs must increase: the delta is
// obj.ObjectID minus the previous ID minus one, and there is no encoding for
// repeating or going backwards.
func (s *SubgroupWriter) AppendObject(buf []byte, obj *SubgroupObject) ([]byte, error) {
	if len(obj.Payload) > 0 && obj.Status != ObjectStatusNormal {
		return nil, errStatusWithPayload
	}
	if len(obj.Properties) > 0 {
		if !s.Header.HasProperties {
			return nil, errPropertiesNotDeclared
		}
		if obj.Status != ObjectStatusNormal {
			return nil, errPropertiesOnNonNormalObject
		}
	}

	delta := obj.ObjectID
	if s.haveObject {
		if obj.ObjectID <= s.prevObjectID {
			return nil, errObjectIDNotIncreasing
		}
		delta = obj.ObjectID - s.prevObjectID - 1
	}
	buf = vi64.Append(buf, delta)

	if s.Header.HasProperties {
		// Every Object on the stream carries the block, empty or not.
		buf = obj.Properties.appendLength(buf)
	}

	buf = vi64.Append(buf, uint64(len(obj.Payload)))
	if len(obj.Payload) == 0 {
		if !obj.Status.valid() {
			return nil, errInvalidObjectStatus
		}
		buf = vi64.Append(buf, uint64(obj.Status))
	} else {
		buf = append(buf, obj.Payload...)
	}

	if !s.haveObject && s.Header.SubgroupIDMode == SubgroupIDFirstObject {
		s.Header.SubgroupID = obj.ObjectID
	}
	s.prevObjectID = obj.ObjectID
	s.haveObject = true
	return buf, nil
}
