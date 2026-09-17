package quicmoq

import (
	"github.com/Eyevinn/moqtransport"
	"github.com/quic-go/quic-go"
)

var (
	_ moqtransport.Stream            = (*Stream)(nil)
	_ moqtransport.PrioritizedStream = (*Stream)(nil)
)

type Stream struct {
	stream *quic.Stream
}

// Read implements moqtransport.Stream.
func (s *Stream) Read(p []byte) (n int, err error) {
	return s.stream.Read(p)
}

// Write implements moqtransport.Stream.
func (s *Stream) Write(p []byte) (n int, err error) {
	return s.stream.Write(p)
}

// Close implements moqtransport.Stream.
func (s *Stream) Close() error {
	return s.stream.Close()
}

// Reset implements moqtransport.Stream.
func (s *Stream) Reset(code uint32) {
	s.stream.CancelWrite(quic.StreamErrorCode(code))
}

// Stop implements moqtransport.Stream.
func (s *Stream) Stop(code uint32) {
	s.stream.CancelRead(quic.StreamErrorCode(code))
}

// StreamID implements moqtransport.Stream.
func (s *Stream) StreamID() uint64 {
	return uint64(s.stream.StreamID())
}

// SetPriority implements moqtransport.PrioritizedStream.
//
// Bidirectional streams carry requests. draft-ietf-moq-transport-18 Section
// 7.2 places them below the control streams and above Object data; the
// control streams themselves are unidirectional since draft-17, so they are
// SendStreams, not these. Nothing applies a priority to a request stream
// today: it keeps quic-go's default urgency.
func (s *Stream) SetPriority(urgency int8, incremental bool) {
	s.stream.SetPriority(urgency, incremental)
}
