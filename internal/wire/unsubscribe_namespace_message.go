package wire

import (
	"log/slog"
)

// TODO: Add tests
type UnsubscribeNamespaceMessage struct {
	TrackNamespacePrefix Tuple
}

func (m *UnsubscribeNamespaceMessage) LogValue() slog.Value {
	return slog.GroupValue(
		slog.String("type", "unsubscribe_namespace"),
		slog.Any("track_namespace_prefix", m.TrackNamespacePrefix),
	)
}

func (m UnsubscribeNamespaceMessage) Type() controlMessageType {
	return messageTypeUnsubscribeNamespace
}

func (m *UnsubscribeNamespaceMessage) Append(buf []byte) []byte {
	return m.TrackNamespacePrefix.append(buf)
}

func (m *UnsubscribeNamespaceMessage) parse(_ Version, data []byte) (err error) {
	m.TrackNamespacePrefix, _, err = parseTuple(data)
	return err
}
