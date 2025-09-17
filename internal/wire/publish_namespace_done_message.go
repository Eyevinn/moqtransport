package wire

import (
	"log/slog"
)

type PublishNamespaceDoneMessage struct {
	TrackNamespace Tuple
}

func (m *PublishNamespaceDoneMessage) LogValue() slog.Value {
	return slog.GroupValue(
		slog.String("type", "publish_namespace_done"),
		slog.Any("track_namespace", m.TrackNamespace),
	)
}

func (m PublishNamespaceDoneMessage) Type() controlMessageType {
	return messageTypePublishNamespaceDone
}

func (m *PublishNamespaceDoneMessage) Append(buf []byte) []byte {
	buf = m.TrackNamespace.append(buf)
	return buf
}

func (p *PublishNamespaceDoneMessage) parse(_ Version, data []byte) (err error) {
	p.TrackNamespace, _, err = parseTuple(data)
	return err
}
