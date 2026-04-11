package wire

import (
	"log/slog"

	"github.com/quic-go/quic-go/quicvarint"
)

type ServerSetupMessage struct {
	WireVersion     Version // controls wire format: draft-16+ omits selected version
	SelectedVersion Version // only used for draft-14 (pre-ALPN negotiation)
	SetupParameters KVPList
}

func (m *ServerSetupMessage) LogValue() slog.Value {
	attrs := []slog.Attr{
		slog.String("type", "server_setup"),
		slog.Uint64("selected_version", uint64(m.SelectedVersion)),
		slog.Uint64("number_of_parameters", uint64(len(m.SetupParameters))),
	}
	if len(m.SetupParameters) > 0 {
		attrs = append(attrs,
			slog.Any("setup_parameters", m.SetupParameters),
		)
	}
	return slog.GroupValue(attrs...)
}

func (m ServerSetupMessage) Type() controlMessageType {
	return messageTypeServerSetup
}

func (m *ServerSetupMessage) Append(buf []byte) []byte {
	if !m.WireVersion.NegotiatedViaALPN() {
		// Draft-14: include selected version
		buf = quicvarint.Append(buf, uint64(m.SelectedVersion))
	}
	return m.SetupParameters.AppendNumVersioned(m.WireVersion, buf)
}

func (m *ServerSetupMessage) parse(v Version, data []byte) error {
	if !v.NegotiatedViaALPN() {
		// Draft-14: parse selected version from wire
		sv, n, err := quicvarint.Parse(data)
		if err != nil {
			return err
		}
		data = data[n:]
		m.SelectedVersion = Version(sv)
	}
	m.SetupParameters = KVPList{}
	return m.SetupParameters.ParseNumVersioned(v, data)
}
