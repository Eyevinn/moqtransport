package wire

import (
	"github.com/quic-go/quic-go/quicvarint"
)

type SubgroupHeaderMessage struct {
	TrackAlias        uint64
	GroupID           uint64
	SubgroupID        uint64
	PublisherPriority uint8
	EndOfGroup        bool
}

func (m *SubgroupHeaderMessage) Append(buf []byte) []byte {
	st := StreamTypeSubgroupSIDExt
	if m.EndOfGroup {
		st = StreamTypeSubgroupSIDExtEOG
	}
	buf = quicvarint.Append(buf, uint64(st))
	buf = quicvarint.Append(buf, m.TrackAlias)
	buf = quicvarint.Append(buf, m.GroupID)
	buf = quicvarint.Append(buf, m.SubgroupID)
	return append(buf, m.PublisherPriority)
}

func (m *SubgroupHeaderMessage) parse(reader messageReader, sid bool, defaultPriority bool) (err error) {
	m.TrackAlias, err = quicvarint.Read(reader)
	if err != nil {
		return
	}
	m.GroupID, err = quicvarint.Read(reader)
	if err != nil {
		return
	}
	if sid {
		m.SubgroupID, err = quicvarint.Read(reader)
		if err != nil {
			return
		}
	}
	if !defaultPriority {
		m.PublisherPriority, err = reader.ReadByte()
	}
	// When defaultPriority is true, PublisherPriority stays at zero value;
	// the caller inherits it from the control message that established the subscription.
	return
}
