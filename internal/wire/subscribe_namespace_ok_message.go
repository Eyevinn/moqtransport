package wire

import (
	"log/slog"

	"github.com/quic-go/quic-go/quicvarint"
)

// TODO: Add tests
type SubscribeNamespaceOkMessage struct {
	RequestID uint64
}

func (m *SubscribeNamespaceOkMessage) LogValue() slog.Value {
	return slog.GroupValue(
		slog.String("type", "subscribe_namespace_ok"),
	)
}

func (m SubscribeNamespaceOkMessage) Type() controlMessageType {
	return messageTypeSubscribeNamespaceOk
}

func (m *SubscribeNamespaceOkMessage) Append(buf []byte) []byte {
	return quicvarint.Append(buf, m.RequestID)
}

func (m *SubscribeNamespaceOkMessage) parse(_ Version, data []byte) (err error) {
	m.RequestID, _, err = quicvarint.Parse(data)
	return err
}
