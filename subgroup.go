package moqtransport

import (
	"errors"
	"io"
	"sync"

	"github.com/Eyevinn/moqtransport/internal/wire2"
)

// Subgroup is an open subgroup stream: one unidirectional stream carrying
// Objects that share a Track, a Group and a Subgroup
// (draft-ietf-moq-transport-18, Section 11.4.2).
//
// Objects must be written in increasing Object ID order. The wire format
// encodes each ID as a delta against the previous one and has no way to go
// backwards, so this is a property of the stream rather than a rule the
// application is asked to remember.
type Subgroup struct {
	writeMu sync.Mutex
	stream  SendStream
	writer  *wire2.SubgroupWriter
	done    bool
}

// newSubgroup opens a subgroup stream by writing its header, which is also the
// stream's type varint.
func newSubgroup(stream SendStream, header *wire2.SubgroupHeader) (*Subgroup, error) {
	buf, err := wire2.AppendSubgroupHeader(nil, header)
	if err != nil {
		return nil, err
	}
	if _, err := stream.Write(buf); err != nil {
		return nil, err
	}
	return &Subgroup{
		stream: stream,
		writer: wire2.NewSubgroupWriter(header),
	}, nil
}

// GroupID returns the Group every Object on this stream belongs to.
func (s *Subgroup) GroupID() uint64 { return s.writer.Header.GroupID }

// SubgroupID returns the Subgroup every Object on this stream belongs to.
//
// Under SUBGROUP_ID_MODE 0b01 it is the first Object's ID, so it reads 0 until
// the first Object has been written.
func (s *Subgroup) SubgroupID() uint64 { return s.writer.Header.SubgroupID }

// StreamID returns the ID of the underlying stream.
func (s *Subgroup) StreamID() uint64 { return s.stream.StreamID() }

// WriteObject writes an Object with a payload. It returns the number of
// payload bytes written.
func (s *Subgroup) WriteObject(objectID uint64, payload []byte) (int, error) {
	return s.WriteObjectWithProperties(objectID, nil, payload)
}

// WriteObjectWithProperties writes an Object carrying Object Properties.
//
// Properties can only be written on a subgroup opened for them: the
// PROPERTIES bit is in the stream's header and covers every Object on it. Ask
// for properties when opening the subgroup, with [WithObjectProperties].
func (s *Subgroup) WriteObjectWithProperties(objectID uint64, properties KVPList, payload []byte) (int, error) {
	if err := s.write(&wire2.SubgroupObject{
		ObjectID:   objectID,
		Properties: properties,
		Payload:    payload,
	}); err != nil {
		return 0, err
	}
	return len(payload), nil
}

// WriteStatus writes an Object that carries a status instead of a payload,
// marking the end of the Group or of the Track.
func (s *Subgroup) WriteStatus(objectID uint64, status ObjectStatus) error {
	return s.write(&wire2.SubgroupObject{ObjectID: objectID, Status: status})
}

func (s *Subgroup) write(obj *wire2.SubgroupObject) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if s.done {
		return errSubgroupClosed
	}
	// The writer holds the delta state, so encoding has to happen under the
	// same lock as the write: two goroutines encoding concurrently would
	// produce deltas against the same previous Object.
	buf, err := s.writer.AppendObject(nil, obj)
	if err != nil {
		return err
	}
	_, err = s.stream.Write(buf)
	return err
}

// Close finishes the subgroup with a FIN, which tells the subscriber that
// every Object in the subgroup was delivered (Section 11.4.3). Use Reset
// instead if that is not true -- a FIN over an incomplete subgroup is a lie
// the subscriber has no way to detect.
func (s *Subgroup) Close() error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if s.done {
		return nil
	}
	s.done = true
	return s.stream.Close()
}

// Reset abandons the subgroup, telling the subscriber that Objects are
// missing. Section 11.4.3 requires it whenever the stream ends before every
// Object has been delivered: a delivery timeout, a cancelled subscription, an
// update that moved the range, or an Object skipped for the subscriber's
// Forward State.
func (s *Subgroup) Reset(code StreamErrorCode) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if s.done {
		return
	}
	s.done = true
	s.stream.Reset(uint32(code))
}

// subgroupReceiver reads one incoming subgroup stream.
//
// The stream's header is parsed before the receiver exists, because the header
// is what says which subscription the stream belongs to; the receiver takes it
// back so that the Objects can be resolved against it.
type subgroupReceiver struct {
	stream ReceiveStream
	reader *wire2.SubgroupReader

	// defaultPriority is what Objects inherit when the header omitted the
	// Publisher Priority field: the priority from the control message that
	// established the subscription.
	defaultPriority uint8
}

func newSubgroupReceiver(stream ReceiveStream, header *wire2.SubgroupHeader, r dataStreamReader, defaultPriority uint8) *subgroupReceiver {
	return &subgroupReceiver{
		stream:          stream,
		reader:          wire2.NewSubgroupReader(header, r),
		defaultPriority: defaultPriority,
	}
}

// receive reads Objects until the stream ends, handing each to deliver.
//
// It returns nil when the stream ends with a FIN, which for a subgroup whose
// header set the END_OF_GROUP bit is itself information: no Object in the
// Group beyond the last one received exists. A reset says nothing of the kind,
// and surfaces here as an error.
func (s *subgroupReceiver) receive(deliver func(*Object) error) error {
	header := s.reader.Header
	priority := header.Priority
	if header.DefaultPriority {
		priority = s.defaultPriority
	}

	for {
		obj, err := s.reader.Next()
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
		if err := deliver(&Object{
			GroupID:              header.GroupID,
			ObjectID:             obj.ObjectID,
			SubgroupID:           header.SubgroupID,
			ForwardingPreference: ObjectForwardingPreferenceSubgroup,
			Priority:             priority,
			Properties:           obj.Properties,
			Status:               obj.Status,
			Payload:              obj.Payload,
		}); err != nil {
			return err
		}
	}
}

// dataStreamReader is what a data stream parser reads from: whole payloads in
// bulk and varints a byte at a time. *bufio.Reader is the implementation.
type dataStreamReader interface {
	io.Reader
	io.ByteReader
}

var errSubgroupClosed = errors.New("subgroup is closed")
