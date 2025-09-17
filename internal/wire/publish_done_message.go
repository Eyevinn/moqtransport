package wire

import (
	"log/slog"

	"github.com/quic-go/quic-go/quicvarint"
)

type PublishDoneMessage struct {
	RequestID    uint64
	StatusCode   uint64
	StreamCount  uint64
	ReasonPhrase string
}

func (m *PublishDoneMessage) LogValue() slog.Value {
	return slog.GroupValue(
		slog.String("type", "publish_done"),
		slog.Uint64("request_id", m.RequestID),
		slog.Uint64("status_code", m.StatusCode),
		slog.Uint64("stream_count", m.StreamCount),
		slog.String("reason", m.ReasonPhrase),
	)
}

func (m PublishDoneMessage) Type() controlMessageType {
	return messageTypePublishDone
}

func (m *PublishDoneMessage) Append(buf []byte) []byte {
	buf = quicvarint.Append(buf, m.RequestID)
	buf = quicvarint.Append(buf, m.StatusCode)
	buf = quicvarint.Append(buf, m.StreamCount)
	buf = appendVarIntBytes(buf, []byte(m.ReasonPhrase))
	return buf
}

func (m *PublishDoneMessage) parse(_ Version, data []byte) (err error) {
	var n int
	m.RequestID, n, err = quicvarint.Parse(data)
	if err != nil {
		return
	}
	data = data[n:]

	m.StatusCode, n, err = quicvarint.Parse(data)
	if err != nil {
		return
	}
	data = data[n:]

	m.StreamCount, n, err = quicvarint.Parse(data)
	if err != nil {
		return
	}
	data = data[n:]

	reasonPhrase, _, err := parseVarIntBytes(data)
	if err != nil {
		return
	}
	m.ReasonPhrase = string(reasonPhrase)
	return nil
}
