package wire

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"iter"

	"github.com/Eyevinn/moqtransport/internal/slices"
	"github.com/mengelbart/qlog"
	"github.com/mengelbart/qlog/moqt"
	"github.com/quic-go/quic-go/quicvarint"
)

type StreamType uint64

const (
	StreamTypeFetch StreamType = 0x05

	// Subgroup header types without End of Group (0x10-0x15)
	StreamTypeSubgroupZeroSIDNoExt StreamType = 0x10
	StreamTypeSubgroupZeroSIDExt   StreamType = 0x11
	StreamTypeSubgroupNoSIDNoExt   StreamType = 0x12
	StreamTypeSubgroupNoSIDExt     StreamType = 0x13
	StreamTypeSubgroupSIDNoExt     StreamType = 0x14
	StreamTypeSubgroupSIDExt       StreamType = 0x15

	// Subgroup header types with End of Group (0x18-0x1D)
	StreamTypeSubgroupZeroSIDNoExtEOG StreamType = 0x18
	StreamTypeSubgroupZeroSIDExtEOG   StreamType = 0x19
	StreamTypeSubgroupNoSIDNoExtEOG   StreamType = 0x1A
	StreamTypeSubgroupNoSIDExtEOG     StreamType = 0x1B
	StreamTypeSubgroupSIDNoExtEOG     StreamType = 0x1C
	StreamTypeSubgroupSIDExtEOG       StreamType = 0x1D
)

var (
	errInvalidStreamType = errors.New("invalid stream type")
)

func isSubgroupStreamType(st StreamType) bool {
	// 0x10-0x15, 0x18-0x1D (without DEFAULT_PRIORITY)
	// 0x30-0x35, 0x38-0x3D (with DEFAULT_PRIORITY, draft-16+)
	low := st & 0x1F // strip DEFAULT_PRIORITY bit
	return (low >= 0x10 && low <= 0x15) || (low >= 0x18 && low <= 0x1D)
}

func subgroupHasExplicitSID(st StreamType) bool {
	low := st & 0x1F
	return low == 0x14 || low == 0x15 || low == 0x1C || low == 0x1D
}

func subgroupSIDIsFirstObjectID(st StreamType) bool {
	low := st & 0x1F
	return low == 0x12 || low == 0x13 || low == 0x1A || low == 0x1B
}

func subgroupContainsEndOfGroup(st StreamType) bool {
	low := st & 0x1F
	return low >= 0x18 && low <= 0x1D
}

// subgroupHasDefaultPriority returns true when the DEFAULT_PRIORITY bit (0x20) is set,
// meaning the Priority field is omitted from the header (draft-16+).
func subgroupHasDefaultPriority(st StreamType) bool {
	return st&0x20 != 0
}

type ObjectStreamParser struct {
	qlogger  *qlog.Logger
	streamID uint64

	reader        messageReader
	typ           StreamType
	identifier    uint64 // Track Alias (Subgroup) or Request ID (Fetch)
	hasSubgroupID bool
	hasExtensions bool
	objectCount   uint64 // number of objects parsed so far in this subgroup
	prevObjectID  uint64 // previous object ID for delta decoding

	PublisherPriority uint8
	GroupID           uint64
	SubgroupID        uint64
	EndOfGroup        bool
}

func (p *ObjectStreamParser) Type() StreamType {
	return p.typ
}

func (p *ObjectStreamParser) Identifier() uint64 {
	return p.identifier
}

func NewObjectStreamParser(r io.Reader, streamID uint64, qlogger *qlog.Logger) (*ObjectStreamParser, error) {
	br := bufio.NewReader(r)
	st, err := quicvarint.Read(br)
	if err != nil {
		return nil, err
	}
	streamType := StreamType(st)

	if streamType == StreamTypeFetch {
		if qlogger != nil {
			qlogger.Log(moqt.StreamTypeSetEvent{
				Owner:      moqt.GetOwner(moqt.OwnerRemote),
				StreamID:   streamID,
				StreamType: moqt.StreamTypeFetchHeader,
			})
		}
		var fhm FetchHeaderMessage
		if err := fhm.parse(br); err != nil {
			return nil, err
		}
		return &ObjectStreamParser{
			qlogger:           qlogger,
			streamID:          streamID,
			reader:            br,
			typ:               streamType,
			identifier:        fhm.RequestID,
			PublisherPriority: 0,
			GroupID:           0,
			SubgroupID:        0,
		}, nil
	}
	if isSubgroupStreamType(streamType) {
		if qlogger != nil {
			qlogger.Log(moqt.StreamTypeSetEvent{
				Owner:      moqt.GetOwner(moqt.OwnerRemote),
				StreamID:   streamID,
				StreamType: moqt.StreamTypeSubgroupHeader,
			})
		}
		// least significant bit indicates if we have to read extensions on
		// objects
		ext := streamType&0x01 > 0

		// Only read subgroup ID from header if type has explicit SID field.
		// In all other cases, it is either zero or will be read from the first object.
		sid := subgroupHasExplicitSID(streamType)
		defPri := subgroupHasDefaultPriority(streamType)

		var shsm SubgroupHeaderMessage
		if err := shsm.parse(br, sid, defPri); err != nil {
			return nil, err
		}
		return &ObjectStreamParser{
			qlogger:    qlogger,
			streamID:   streamID,
			reader:     br,
			typ:        streamType,
			identifier: shsm.TrackAlias,
			// if subgroup ID comes from first object ID, we don't yet know it
			// because it will only be read when the first object is parsed.
			hasSubgroupID:     !subgroupSIDIsFirstObjectID(streamType),
			hasExtensions:     ext,
			PublisherPriority: shsm.PublisherPriority,
			GroupID:           shsm.GroupID,
			SubgroupID:        shsm.SubgroupID,
			EndOfGroup:        subgroupContainsEndOfGroup(streamType),
		}, nil
	}
	return nil, fmt.Errorf("%w: %v", errInvalidStreamType, st)
}

func (p *ObjectStreamParser) Messages() iter.Seq2[*ObjectMessage, error] {
	return func(yield func(*ObjectMessage, error) bool) {
		for {
			if !yield(p.Parse()) {
				return
			}
		}
	}
}

func (p *ObjectStreamParser) parseSubgroupObject() (*ObjectMessage, error) {
	var ext KVPList
	if p.hasExtensions {
		ext = KVPList{}
	}
	m := &ObjectMessage{
		TrackAlias:             p.identifier,
		GroupID:                p.GroupID,
		SubgroupID:             p.SubgroupID,
		ObjectID:               0,
		PublisherPriority:      p.PublisherPriority,
		ObjectExtensionHeaders: ext,
		ObjectStatus:           0,
		ObjectPayload:          nil,
	}
	if err := m.readSubgroup(p.reader); err != nil {
		return nil, err
	}
	// Object IDs are delta-encoded within subgroup streams (draft-14+):
	// "Object ID Delta + 1 is added to the previous Object ID if there was one.
	//  The Object ID is the Object ID Delta if it's the first Object."
	if p.objectCount > 0 {
		m.ObjectID = p.prevObjectID + m.ObjectID + 1
	}
	p.prevObjectID = m.ObjectID
	p.objectCount++
	if !p.hasSubgroupID {
		p.SubgroupID = m.SubgroupID
		p.hasSubgroupID = true
	}
	if p.qlogger != nil {
		eth := slices.Collect(slices.Map(
			m.ObjectExtensionHeaders,
			func(e KeyValuePair) moqt.ExtensionHeader {
				return moqt.ExtensionHeader{
					HeaderType:   e.Type,
					HeaderValue:  0, // TODO
					HeaderLength: 0, // TODO
					Payload:      qlog.RawInfo{},
				}
			}),
		)
		gid := new(uint64)
		sid := new(uint64)
		*gid = p.GroupID
		*sid = p.SubgroupID
		p.qlogger.Log(moqt.SubgroupObjectEvent{
			EventName:              moqt.SubgroupObjectEventParsed,
			StreamID:               p.streamID,
			GroupID:                gid,
			SubgroupID:             sid,
			ObjectID:               m.ObjectID,
			ExtensionHeadersLength: uint64(len(m.ObjectExtensionHeaders)),
			ExtensionHeaders:       eth,
			ObjectPayloadLength:    uint64(len(m.ObjectPayload)),
			ObjectStatus:           uint64(m.ObjectStatus),
			ObjectPayload: qlog.RawInfo{
				Length:        uint64(len(m.ObjectPayload)),
				PayloadLength: uint64(len(m.ObjectPayload)),
				Data:          m.ObjectPayload,
			},
		})
	}
	return m, nil
}

func (p *ObjectStreamParser) parseFetchObject() (*ObjectMessage, error) {
	m := &ObjectMessage{
		TrackAlias:        0,
		GroupID:           p.GroupID,
		SubgroupID:        0,
		ObjectID:          0,
		PublisherPriority: p.PublisherPriority,
		ObjectStatus:      0,
		ObjectPayload:     nil,
	}
	if err := m.readFetch(p.reader); err != nil {
		return nil, err
	}
	if p.qlogger != nil {
		eth := slices.Collect(slices.Map(
			m.ObjectExtensionHeaders,
			func(e KeyValuePair) moqt.ExtensionHeader {
				return moqt.ExtensionHeader{
					HeaderType:   e.Type,
					HeaderValue:  0, // TODO
					HeaderLength: 0, // TODO
					Payload:      qlog.RawInfo{},
				}
			}),
		)
		p.qlogger.Log(moqt.FetchObjectEvent{
			EventName:              moqt.FetchObjectEventParsed,
			StreamID:               p.streamID,
			GroupID:                m.GroupID,
			SubgroupID:             m.SubgroupID,
			ObjectID:               m.ObjectID,
			PublisherPriority:      m.PublisherPriority,
			ExtensionHeadersLength: uint64(len(m.ObjectExtensionHeaders)),
			ExtensionHeaders:       eth,
			ObjectPayloadLength:    uint64(len(m.ObjectPayload)),
			ObjectStatus:           uint64(m.ObjectStatus),
			ObjectPayload: qlog.RawInfo{
				Length:        uint64(len(m.ObjectPayload)),
				PayloadLength: uint64(len(m.ObjectPayload)),
				Data:          m.ObjectPayload,
			},
		})
	}
	return m, nil
}

func (p *ObjectStreamParser) Parse() (*ObjectMessage, error) {
	if p.typ == StreamTypeFetch {
		return p.parseFetchObject()
	}
	if isSubgroupStreamType(p.typ) {
		return p.parseSubgroupObject()
	}
	return nil, errInvalidStreamType
}
