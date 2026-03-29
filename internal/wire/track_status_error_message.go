package wire

import (
	"log/slog"

	"github.com/quic-go/quic-go/quicvarint"
)

type TrackStatusErrorMessage struct {
	RequestID    uint64
	ErrorCode    uint64
	ReasonPhrase string
}

func (m *TrackStatusErrorMessage) LogValue() slog.Value {
	return slog.GroupValue(
		slog.String("type", "track_status_error"),
		slog.Uint64("request_id", m.RequestID),
		slog.Uint64("error_code", m.ErrorCode),
		slog.String("reason", m.ReasonPhrase),
	)
}

func (m TrackStatusErrorMessage) Type() controlMessageType {
	return messageTypeTrackStatusError
}

func (m *TrackStatusErrorMessage) Append(buf []byte) []byte {
	buf = quicvarint.Append(buf, m.RequestID)
	buf = quicvarint.Append(buf, uint64(m.ErrorCode))
	buf = appendVarIntBytes(buf, []byte(m.ReasonPhrase))
	return buf
}

func (m *TrackStatusErrorMessage) parse(_ Version, data []byte) (err error) {
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
