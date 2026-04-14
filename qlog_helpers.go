package moqtransport

import (
	"github.com/Eyevinn/moqtransport/internal/wire"
	"github.com/mengelbart/qlog"
	"github.com/mengelbart/qlog/moqt"
)

func extensionHeadersToQlog(headers wire.KVPList) []moqt.ExtensionHeader {
	if len(headers) == 0 {
		return nil
	}
	eth := make([]moqt.ExtensionHeader, len(headers))
	for i, e := range headers {
		eth[i] = moqt.ExtensionHeader{
			HeaderType:   e.Type,
			HeaderValue:  e.ValueVarInt,
			HeaderLength: uint64(len(e.ValueBytes)),
			Payload: qlog.RawInfo{
				Length:        uint64(len(e.ValueBytes)),
				PayloadLength: uint64(len(e.ValueBytes)),
				Data:          e.ValueBytes,
			},
		}
	}
	return eth
}
