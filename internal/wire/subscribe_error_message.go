package wire

import (
	"log/slog"

	"github.com/quic-go/quic-go/quicvarint"
)

type SubscribeErrorMessage struct {
	WireVersion   Version
	RequestID     uint64
	ErrorCode     uint64
	RetryInterval uint64 // draft-16+: minimum ms before retry (0 = don't retry)
	ReasonPhrase  string
}

func (m *SubscribeErrorMessage) LogValue() slog.Value {
	return slog.GroupValue(
		slog.String("type", "subscribe_error"),
		slog.Uint64("request_id", m.RequestID),
		slog.Uint64("error_code", m.ErrorCode),
		slog.String("reason", m.ReasonPhrase),
		slog.Any("reason_bytes", []byte(m.ReasonPhrase)),
	)
}

func (m SubscribeErrorMessage) Type() controlMessageType {
	return messageTypeSubscribeError
}

func (m *SubscribeErrorMessage) Append(buf []byte) []byte {
	buf = quicvarint.Append(buf, m.RequestID)
	buf = quicvarint.Append(buf, uint64(m.ErrorCode))
	if m.WireVersion.NegotiatedViaALPN() {
		buf = quicvarint.Append(buf, m.RetryInterval)
	}
	buf = appendVarIntBytes(buf, []byte(m.ReasonPhrase))
	return buf
}

func (m *SubscribeErrorMessage) parse(v Version, data []byte) (err error) {
	var n int
	m.RequestID, n, err = quicvarint.Parse(data)
	if err != nil {
		return err
	}
	data = data[n:]

	m.ErrorCode, n, err = quicvarint.Parse(data)
	if err != nil {
		return err
	}
	data = data[n:]

	if v.NegotiatedViaALPN() {
		// Draft-16 REQUEST_ERROR: includes RetryInterval
		m.RetryInterval, n, err = quicvarint.Parse(data)
		if err != nil {
			return err
		}
		data = data[n:]
	}

	reasonPhrase, _, err := parseVarIntBytes(data)
	if err != nil {
		return err
	}
	m.ReasonPhrase = string(reasonPhrase)
	return nil
}
