package wire

import (
	"log/slog"

	"github.com/quic-go/quic-go/quicvarint"
)

type PublishNamespaceCancelMessage struct {
	TrackNamespace Tuple
	ErrorCode      uint64
	ReasonPhrase   string
}

func (m *PublishNamespaceCancelMessage) LogValue() slog.Value {
	return slog.GroupValue(
		slog.String("type", "publish_namespace_cancel"),
		slog.Any("track_namespace", m.TrackNamespace),
		slog.Uint64("error_code", m.ErrorCode),
		slog.String("reason", m.ReasonPhrase),
	)
}

func (m PublishNamespaceCancelMessage) GetTrackNamespace() string {
	return m.TrackNamespace.String()
}

func (m PublishNamespaceCancelMessage) Type() controlMessageType {
	return messageTypePublishNamespaceCancel
}

func (m *PublishNamespaceCancelMessage) Append(buf []byte) []byte {
	buf = m.TrackNamespace.append(buf)
	buf = quicvarint.Append(buf, m.ErrorCode)
	buf = appendVarIntBytes(buf, []byte(m.ReasonPhrase))
	return buf
}

func (m *PublishNamespaceCancelMessage) parse(_ Version, data []byte) (err error) {
	var n int
	m.TrackNamespace, n, err = parseTuple(data)
	if err != nil {
		return err
	}
	data = data[n:]

	m.ErrorCode, n, err = quicvarint.Parse(data)
	if err != nil {
		return err
	}
	data = data[n:]

	reasonPhrase, _, err := parseVarIntBytes(data)
	m.ReasonPhrase = string(reasonPhrase)
	return err
}
