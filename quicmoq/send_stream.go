package quicmoq

import (
	"github.com/Eyevinn/moqtransport"
	"github.com/quic-go/quic-go"
)

var (
	_ moqtransport.SendStream        = (*SendStream)(nil)
	_ moqtransport.PrioritizedStream = (*SendStream)(nil)
)

type SendStream struct {
	stream *quic.SendStream
}

// Write implements moqtransport.SendStream.
func (s *SendStream) Write(p []byte) (n int, err error) {
	return s.stream.Write(p)
}

// Reset implements moqtransport.SendStream
func (s *SendStream) Reset(code uint32) {
	s.stream.CancelWrite(quic.StreamErrorCode(code))
}

// Close implements moqtransport.SendStream.
func (s *SendStream) Close() error {
	return s.stream.Close()
}

// StreamID implements moqtransport.SendStream
func (s *SendStream) StreamID() uint64 {
	return uint64(s.stream.StreamID())
}

// SetPriority implements moqtransport.PrioritizedStream.
//
// quic-go schedules streams by RFC 9218's urgency and incremental parameters,
// which is what a moqtransport.PriorityMapper reduces a MOQT priority to.
func (s *SendStream) SetPriority(urgency int8, incremental bool) {
	s.stream.SetPriority(urgency, incremental)
}
