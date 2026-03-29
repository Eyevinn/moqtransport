package wire

import (
	"log/slog"

	"github.com/mengelbart/qlog"
	"github.com/quic-go/quic-go/quicvarint"
)

type TrackStatusMessage struct {
	RequestID          uint64
	TrackNamespace     Tuple
	TrackName          []byte
	SubscriberPriority uint8
	GroupOrder         GroupOrder
	Forward            uint8
	FilterType         FilterType
	StartLocation      Location
	EndGroup           uint64
	Parameters         KVPList
}

func (m *TrackStatusMessage) LogValue() slog.Value {
	return slog.GroupValue(
		slog.String("type", "track_status"),
		slog.Uint64("request_id", m.RequestID),
		slog.Any("track_namespace", m.TrackNamespace),
		slog.Any("track_name", qlog.RawInfo{
			Length:        uint64(len(m.TrackName)),
			PayloadLength: uint64(len(m.TrackName)),
			Data:          m.TrackName,
		}),
	)
}

func (m TrackStatusMessage) Type() controlMessageType {
	return messageTypeTrackStatus
}

func (m *TrackStatusMessage) Append(buf []byte) []byte {
	buf = quicvarint.Append(buf, m.RequestID)
	buf = m.TrackNamespace.append(buf)
	buf = appendVarIntBytes(buf, m.TrackName)
	buf = append(buf, m.SubscriberPriority)
	buf = append(buf, byte(m.GroupOrder))
	buf = append(buf, m.Forward)
	buf = m.FilterType.append(buf)
	if m.FilterType == FilterTypeAbsoluteStart || m.FilterType == FilterTypeAbsoluteRange {
		buf = m.StartLocation.append(buf)
	}
	if m.FilterType == FilterTypeAbsoluteRange {
		buf = quicvarint.Append(buf, m.EndGroup)
	}
	return m.Parameters.appendNum(buf)
}

func (m *TrackStatusMessage) parse(v Version, data []byte) (err error) {
	var n int
	m.RequestID, n, err = quicvarint.Parse(data)
	if err != nil {
		return err
	}
	data = data[n:]

	m.TrackNamespace, n, err = parseTuple(data)
	if err != nil {
		return err
	}
	data = data[n:]

	m.TrackName, n, err = parseVarIntBytes(data)
	if err != nil {
		return err
	}
	data = data[n:]

	if len(data) < 3 {
		return errLengthMismatch
	}
	m.SubscriberPriority = data[0]
	m.GroupOrder = GroupOrder(data[1])
	if m.GroupOrder > 2 {
		return errInvalidGroupOrder
	}
	m.Forward = data[2]
	if m.Forward > 1 {
		return errInvalidForwardFlag
	}
	data = data[3:]

	filterType, n, err := quicvarint.Parse(data)
	if err != nil {
		return err
	}
	m.FilterType = FilterType(filterType)
	if m.FilterType == 0 || m.FilterType > 4 {
		return errInvalidFilterType
	}
	data = data[n:]

	if m.FilterType == FilterTypeAbsoluteStart || m.FilterType == FilterTypeAbsoluteRange {
		n, err = m.StartLocation.parse(v, data)
		if err != nil {
			return err
		}
		data = data[n:]
	}

	if m.FilterType == FilterTypeAbsoluteRange {
		m.EndGroup, n, err = quicvarint.Parse(data)
		if err != nil {
			return err
		}
		data = data[n:]
	}
	m.Parameters = KVPList{}
	return m.Parameters.parseNum(data)
}
