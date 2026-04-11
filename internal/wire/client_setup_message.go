package wire

import (
	"log/slog"

	"github.com/quic-go/quic-go/quicvarint"
)

type ClientSetupMessage struct {
	WireVersion       Version  // controls wire format: draft-16+ omits version list
	SupportedVersions versions // only used for draft-14 (pre-ALPN negotiation)
	SetupParameters   KVPList
}

func (m *ClientSetupMessage) LogValue() slog.Value {
	attrs := []slog.Attr{
		slog.String("type", "client_setup"),
		slog.Uint64("number_of_supported_versions", uint64(len(m.SupportedVersions))),
		slog.Any("supported_versions", m.SupportedVersions),
		slog.Uint64("number_of_parameters", uint64(len(m.SetupParameters))),
	}
	if len(m.SetupParameters) > 0 {
		attrs = append(attrs,
			slog.Any("setup_parameters", m.SetupParameters),
		)
	}
	return slog.GroupValue(attrs...)
}

func (m ClientSetupMessage) Type() controlMessageType {
	return messageTypeClientSetup
}

func (m *ClientSetupMessage) Append(buf []byte) []byte {
	if !m.WireVersion.NegotiatedViaALPN() {
		// Draft-14: include version list for in-band negotiation
		buf = quicvarint.Append(buf, uint64(len(m.SupportedVersions)))
		for _, v := range m.SupportedVersions {
			buf = quicvarint.Append(buf, uint64(v))
		}
	}
	return m.SetupParameters.AppendNumVersioned(m.WireVersion, buf)
}

func (m *ClientSetupMessage) parse(v Version, data []byte) error {
	if !v.NegotiatedViaALPN() {
		// Draft-14: parse version list from wire
		n, err := m.SupportedVersions.parse(data)
		if err != nil {
			return err
		}
		data = data[n:]
	}
	m.SetupParameters = KVPList{}
	return m.SetupParameters.ParseNumVersioned(v, data)
}
