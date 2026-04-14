package moqtransport

import (
	"github.com/Eyevinn/moqtransport/internal/wire"
	"github.com/mengelbart/qlog"
	"github.com/mengelbart/qlog/moqt"
)

type Subgroup struct {
	qlogger *qlog.Logger

	stream       SendStream
	groupID      uint64
	subgroupID   uint64
	objectCount  uint64
	prevObjectID uint64
}

func newSubgroup(stream SendStream, trackAlias, groupID, subgroupID uint64, publisherPriority uint8, endOfGroup bool, qlogger *qlog.Logger) (*Subgroup, error) {
	shgm := &wire.SubgroupHeaderMessage{
		TrackAlias:        trackAlias,
		GroupID:           groupID,
		SubgroupID:        subgroupID,
		PublisherPriority: publisherPriority,
		EndOfGroup:        endOfGroup,
	}
	buf := make([]byte, 0, 40)
	buf = shgm.Append(buf)
	_, err := stream.Write(buf)
	if err != nil {
		return nil, err
	}
	if qlogger != nil {
		qlogger.Log(moqt.StreamTypeSetEvent{
			Owner:      moqt.GetOwner(moqt.OwnerLocal),
			StreamID:   stream.StreamID(),
			StreamType: moqt.StreamTypeSubgroupHeader,
		})
	}
	return &Subgroup{
		qlogger:    qlogger,
		stream:     stream,
		groupID:    groupID,
		subgroupID: subgroupID,
	}, nil
}

func (s *Subgroup) WriteObject(objectID uint64, payload []byte) (int, error) {
	return s.WriteObjectWithHeaders(objectID, nil, payload)
}

func (s *Subgroup) WriteObjectWithHeaders(objectID uint64, headers KVPList, payload []byte) (int, error) {
	// Object IDs are delta-encoded on the wire (draft-14+):
	// First object: delta = objectID
	// Subsequent objects: delta = objectID - prevObjectID - 1
	delta := objectID
	if s.objectCount > 0 {
		delta = objectID - s.prevObjectID - 1
	}
	s.prevObjectID = objectID
	s.objectCount++

	var buf []byte
	if len(payload) > 0 {
		buf = make([]byte, 0, 16+len(payload))
	} else {
		buf = make([]byte, 0, 24)
	}
	o := wire.ObjectMessage{
		ObjectID:               delta,
		ObjectExtensionHeaders: headers.ToWire(),
		ObjectPayload:          payload,
	}
	buf = o.AppendSubgroup(buf)
	_, err := s.stream.Write(buf)
	if err != nil {
		return 0, err
	}
	if s.qlogger != nil {
		eth := extensionHeadersToQlog(o.ObjectExtensionHeaders)
		gid := new(uint64)
		sid := new(uint64)
		*gid = s.groupID
		*sid = s.subgroupID
		s.qlogger.Log(moqt.SubgroupObjectEvent{
			EventName:              moqt.SubgroupObjectEventCreated,
			StreamID:               s.stream.StreamID(),
			GroupID:                gid,
			SubgroupID:             sid,
			ObjectID:               objectID,
			ExtensionHeadersLength: uint64(len(eth)),
			ExtensionHeaders:       eth,
			ObjectPayloadLength:    uint64(len(payload)),
			ObjectStatus:           0,
			ObjectPayload: qlog.RawInfo{
				Length:        uint64(len(payload)),
				PayloadLength: uint64(len(payload)),
				Data:          payload,
			},
		})
	}
	return len(payload), nil
}

// Close closes the subgroup.
func (s *Subgroup) Close() error {
	return s.stream.Close()
}
