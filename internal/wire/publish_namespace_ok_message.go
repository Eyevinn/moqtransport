package wire

import (
	"log/slog"

	"github.com/quic-go/quic-go/quicvarint"
)

type PublishNamespaceOkMessage struct {
	RequestID uint64
}

func (m *PublishNamespaceOkMessage) LogValue() slog.Value {
	return slog.GroupValue(
		slog.String("type", "publish_namespace_ok"),
	)
}

func (m PublishNamespaceOkMessage) Type() controlMessageType {
	return messageTypePublishNamespaceOk
}

func (m *PublishNamespaceOkMessage) Append(buf []byte) []byte {
	return quicvarint.Append(buf, m.RequestID)
}

func (m *PublishNamespaceOkMessage) parse(_ Version, data []byte) (err error) {
	m.RequestID, _, err = quicvarint.Parse(data)
	return err
}
