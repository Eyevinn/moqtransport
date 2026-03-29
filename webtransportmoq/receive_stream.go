package webtransportmoq

import (
	"github.com/Eyevinn/moqtransport"
	"github.com/quic-go/webtransport-go"
)

var _ moqtransport.ReceiveStream = (*ReceiveStream)(nil)

type ReceiveStream struct {
	stream *webtransport.ReceiveStream
}

// Read implements moqtransport.ReceiveStream.
func (r *ReceiveStream) Read(p []byte) (n int, err error) {
	return r.stream.Read(p)
}

// Stop implements moqtransport.ReceiveStream.
func (r *ReceiveStream) Stop(code uint32) {
	r.stream.CancelRead(webtransport.StreamErrorCode(code))
}

// StreamID implements moqtransport.ReceiveStream.
// webtransport-go v0.10+ no longer exposes the stream ID.
func (r *ReceiveStream) StreamID() uint64 {
	return 0
}
