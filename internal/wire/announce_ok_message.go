package wire

import (
	"log/slog"

	"github.com/quic-go/quic-go/quicvarint"
)

type AnnounceOkMessage struct {
	RequestID  uint64
	Parameters KVPList // draft-16+: REQUEST_OK includes parameters
}

func (m *AnnounceOkMessage) LogValue() slog.Value {
	return slog.GroupValue(
		slog.String("type", "announce_ok"),
	)
}

func (m AnnounceOkMessage) Type() controlMessageType {
	return messageTypeAnnounceOk
}

func (m *AnnounceOkMessage) Append(buf []byte) []byte {
	buf = quicvarint.Append(buf, m.RequestID)
	if len(m.Parameters) > 0 {
		// Draft-16 REQUEST_OK format includes parameters
		buf = quicvarint.Append(buf, uint64(len(m.Parameters)))
		for _, p := range m.Parameters {
			buf = p.append(buf)
		}
	}
	return buf
}

func (m *AnnounceOkMessage) parse(v Version, data []byte) (err error) {
	var n int
	m.RequestID, n, err = quicvarint.Parse(data)
	if err != nil {
		return err
	}
	data = data[n:]

	if v.NegotiatedViaALPN() && len(data) > 0 {
		// Draft-16 REQUEST_OK: includes parameters
		m.Parameters = KVPList{}
		return m.Parameters.ParseNumVersioned(v, data)
	}
	return nil
}
