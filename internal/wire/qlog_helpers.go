package wire

import "github.com/mengelbart/qlog"

const maxQlogPayloadBytes = 20

// TruncatedRawInfo creates a qlog.RawInfo with the Data field truncated to
// maxQlogPayloadBytes. The Length and PayloadLength fields reflect the original
// full length, while Data contains only the first 20 bytes (or less if shorter).
func TruncatedRawInfo(data []byte) qlog.RawInfo {
	truncated := data
	if len(data) > maxQlogPayloadBytes {
		truncated = data[:maxQlogPayloadBytes]
	}
	return qlog.RawInfo{
		Length:        uint64(len(data)),
		PayloadLength: uint64(len(data)),
		Data:          truncated,
	}
}
