package wire

import (
	"log/slog"

	"github.com/quic-go/quic-go/quicvarint"
)

type SubscribeUpdateMessage struct {
	WireVersion           Version
	RequestID             uint64
	SubscriptionRequestID uint64
	StartLocation         Location
	EndGroup              uint64
	SubscriberPriority    uint8
	Forward               uint8
	Parameters            KVPList
}

func (m *SubscribeUpdateMessage) LogValue() slog.Value {
	attrs := []slog.Attr{
		slog.String("type", "subscribe_update"),
		slog.Uint64("request_id", m.RequestID),
		slog.Uint64("subscription_request_id", m.SubscriptionRequestID),
		slog.Uint64("start_group", m.StartLocation.Group),
		slog.Uint64("start_object", m.StartLocation.Object),
		slog.Uint64("end_group", m.EndGroup),
		slog.Uint64("subscriber_priority", uint64(m.SubscriberPriority)),
		slog.Uint64("forward", uint64(m.Forward)),
		slog.Uint64("number_of_parameters", uint64(len(m.Parameters))),
	}
	if len(m.Parameters) > 0 {
		attrs = append(attrs,
			slog.Any("setup_parameters", m.Parameters),
		)
	}
	return slog.GroupValue(attrs...)
}

func (m SubscribeUpdateMessage) Type() controlMessageType {
	return messageTypeSubscribeUpdate
}

func (m *SubscribeUpdateMessage) Append(buf []byte) []byte {
	buf = quicvarint.Append(buf, m.RequestID)
	buf = quicvarint.Append(buf, m.SubscriptionRequestID)

	if m.WireVersion.NegotiatedViaALPN() {
		// Draft-16 REQUEST_UPDATE: all fields in parameters
		params := make(KVPList, len(m.Parameters))
		copy(params, m.Parameters)
		params = append(params, KeyValuePair{
			Type:        ForwardParamKey,
			ValueVarInt: uint64(m.Forward),
		})
		params = append(params, KeyValuePair{
			Type:        SubscriberPriorityParamKey,
			ValueVarInt: uint64(m.SubscriberPriority),
		})
		// SUBSCRIPTION_FILTER with start/end
		filterBuf := quicvarint.Append(nil, uint64(FilterTypeAbsoluteStart))
		filterBuf = m.StartLocation.append(filterBuf)
		if m.EndGroup > 0 {
			filterBuf = filterBuf[:0]
			filterBuf = quicvarint.Append(filterBuf, uint64(FilterTypeAbsoluteRange))
			filterBuf = m.StartLocation.append(filterBuf)
			filterBuf = quicvarint.Append(filterBuf, m.EndGroup)
		}
		params = append(params, KeyValuePair{
			Type:       SubscriptionFilterParamKey,
			ValueBytes: filterBuf,
		})
		return params.AppendNumVersioned(m.WireVersion, buf)
	}

	// Draft-14: inline fields
	buf = m.StartLocation.append(buf)
	buf = quicvarint.Append(buf, m.EndGroup)
	buf = append(buf, m.SubscriberPriority)
	buf = append(buf, m.Forward)
	return m.Parameters.appendNum(buf)
}

func (m *SubscribeUpdateMessage) parse(v Version, data []byte) (err error) {
	var n int

	m.RequestID, n, err = quicvarint.Parse(data)
	if err != nil {
		return err
	}
	data = data[n:]

	m.SubscriptionRequestID, n, err = quicvarint.Parse(data)
	if err != nil {
		return err
	}
	data = data[n:]

	if v.NegotiatedViaALPN() {
		// Draft-16 REQUEST_UPDATE: fields in parameters
		m.Parameters = KVPList{}
		if err := m.Parameters.ParseNumVersioned(v, data); err != nil {
			return err
		}
		m.SubscriberPriority = 128
		m.Forward = 1
		for _, p := range m.Parameters {
			switch p.Type {
			case SubscriberPriorityParamKey:
				m.SubscriberPriority = uint8(p.ValueVarInt)
			case ForwardParamKey:
				m.Forward = uint8(p.ValueVarInt)
			case SubscriptionFilterParamKey:
				if len(p.ValueBytes) > 0 {
					ft, fn, ferr := quicvarint.Parse(p.ValueBytes)
					if ferr != nil {
						return ferr
					}
					filterData := p.ValueBytes[fn:]
					if FilterType(ft) == FilterTypeAbsoluteStart || FilterType(ft) == FilterTypeAbsoluteRange {
						fn, ferr = m.StartLocation.parse(v, filterData)
						if ferr != nil {
							return ferr
						}
						filterData = filterData[fn:]
					}
					if FilterType(ft) == FilterTypeAbsoluteRange {
						m.EndGroup, _, ferr = quicvarint.Parse(filterData)
						if ferr != nil {
							return ferr
						}
					}
				}
			}
		}
		return nil
	}

	// Draft-14: inline fields
	n, err = m.StartLocation.parse(v, data)
	if err != nil {
		return err
	}
	data = data[n:]

	m.EndGroup, n, err = quicvarint.Parse(data)
	if err != nil {
		return err
	}
	data = data[n:]

	if len(data) < 2 {
		return errLengthMismatch
	}
	m.SubscriberPriority = data[0]
	m.Forward = data[1]
	if m.Forward > 1 {
		return errInvalidForwardFlag
	}
	data = data[2:]

	m.Parameters = KVPList{}
	return m.Parameters.parseNum(data)
}
