package wire

import (
	"log/slog"

	"github.com/quic-go/quic-go/quicvarint"
)

type SubscribeNamespaceErrorMessage struct {
	RequestID    uint64
	ErrorCode    uint64
	ReasonPhrase string
}

func (m *SubscribeNamespaceErrorMessage) LogValue() slog.Value {
	return slog.GroupValue(
		slog.String("type", "subscribe_namespace_error"),
		slog.Uint64("error_code", m.ErrorCode),
		slog.String("reason", m.ReasonPhrase),
	)
}

func (m SubscribeNamespaceErrorMessage) Type() controlMessageType {
	return messageTypeSubscribeNamespaceError
}

func (m *SubscribeNamespaceErrorMessage) Append(buf []byte) []byte {
	buf = quicvarint.Append(buf, m.RequestID)
	buf = quicvarint.Append(buf, m.ErrorCode)
	return appendVarIntBytes(buf, []byte(m.ReasonPhrase))
}

func (m *SubscribeNamespaceErrorMessage) parse(_ Version, data []byte) (err error) {
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

	reasonPhrase, _, err := parseVarIntBytes(data)
	if err != nil {
		return err
	}
	m.ReasonPhrase = string(reasonPhrase)
	return nil
}
