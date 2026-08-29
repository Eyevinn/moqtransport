package moqtransport

import (
	"bytes"
	"context"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/Eyevinn/moqtransport/internal/wire2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

// setupBytes returns a framed SETUP as it appears on the wire, which is also
// exactly what opening a control stream looks like: the leading 0x2F00 is both
// the stream type and the message type.
func setupBytes(t *testing.T, options wire2.KVPList) []byte {
	t.Helper()
	buf, err := wire2.AppendControlMessage(nil, &wire2.Setup{Options: options})
	require.NoError(t, err)
	return buf
}

// adoptFrom wires a reader up the way the accept loop will: sniff the stream
// type, then hand it back to the parser.
func adoptFrom(t *testing.T, p *controlStreamPair, r io.Reader) error {
	t.Helper()
	parser := wire2.NewControlMessageParser(r, wire2.ScopeControl)
	streamType, kind, err := wire2.ReadStreamType(parser.Reader())
	require.NoError(t, err)
	require.Equal(t, wire2.StreamTypeSetup, streamType)
	require.Equal(t, wire2.UniStreamControl, kind)
	return p.adoptRemote(nil, parser)
}

func TestControlStreamPairOpenSendsSetup(t *testing.T) {
	ctrl := gomock.NewController(t)
	conn := NewMockConnection(ctrl)
	stream := NewMockSendStream(ctrl)

	var written bytes.Buffer
	conn.EXPECT().OpenUniStreamSync(gomock.Any()).Return(stream, nil)
	stream.EXPECT().Write(gomock.Any()).DoAndReturn(written.Write)

	p := newControlStreamPair()
	setup := &wire2.Setup{Options: wire2.KVPList{wire2.ImplementationOption("test/1.0")}}
	require.NoError(t, p.open(context.Background(), conn, setup))

	// What went out must parse back as a control stream opening with SETUP.
	parser := wire2.NewControlMessageParser(bytes.NewReader(written.Bytes()), wire2.ScopeControl)
	streamType, kind, err := wire2.ReadStreamType(parser.Reader())
	require.NoError(t, err)
	assert.Equal(t, wire2.StreamTypeSetup, streamType)
	assert.Equal(t, wire2.UniStreamControl, kind)

	msg, err := parser.ParseBody(wire2.ControlMessageType(streamType))
	require.NoError(t, err)
	assert.Equal(t, setup, msg)
}

// TestControlStreamPairDoesNotOpenUntilAsked pins that nothing reaches the wire
// from construction alone, so a session can finish assembling first.
func TestControlStreamPairDoesNotOpenUntilAsked(t *testing.T) {
	ctrl := gomock.NewController(t)
	conn := NewMockConnection(ctrl)
	// No EXPECT calls: touching the connection here fails the test.
	_ = conn

	p := newControlStreamPair()
	assert.Nil(t, p.local)

	err := p.write(&wire2.GoAwayCtrl{})
	assert.ErrorIs(t, err, errControlStreamNotOpen)
}

func TestControlStreamPairAdoptRemote(t *testing.T) {
	p := newControlStreamPair()
	options := wire2.KVPList{
		wire2.MaxAuthTokenCacheSizeOption(2048),
		wire2.ImplementationOption("peer/2.0"),
	}
	require.NoError(t, adoptFrom(t, p, bytes.NewReader(setupBytes(t, options))))

	setup, err := p.awaitPeerSetup(context.Background())
	require.NoError(t, err)
	assert.Equal(t, uint64(2048), setup.MaxAuthTokenCacheSize())

	impl, ok := setup.Implementation()
	require.True(t, ok)
	assert.Equal(t, "peer/2.0", impl)
}

func TestControlStreamPairAwaitBlocksUntilSetupArrives(t *testing.T) {
	p := newControlStreamPair()

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_, err := p.awaitPeerSetup(ctx)
	assert.ErrorIs(t, err, context.DeadlineExceeded)

	// And once it arrives, the wait returns immediately.
	require.NoError(t, adoptFrom(t, p, bytes.NewReader(setupBytes(t, wire2.KVPList{}))))
	setup, err := p.awaitPeerSetup(context.Background())
	require.NoError(t, err)
	assert.NotNil(t, setup)
}

// TestControlStreamPairMalformedSetupReleasesWaiters is the reason readiness is
// a channel and not a flag: a peer that opens a control stream and then sends
// nonsense must fail the wait, not hang it until the setup deadline.
func TestControlStreamPairMalformedSetupReleasesWaiters(t *testing.T) {
	p := newControlStreamPair()

	// The control stream type followed by a truncated frame.
	truncated := []byte{0xaf, 0x00, 0x00}
	err := adoptFrom(t, p, bytes.NewReader(truncated))
	require.Error(t, err)

	_, err = p.awaitPeerSetup(context.Background())
	assert.ErrorIs(t, err, io.ErrUnexpectedEOF)
}

func TestControlStreamPairRejectsSecondControlStream(t *testing.T) {
	p := newControlStreamPair()
	require.NoError(t, adoptFrom(t, p, bytes.NewReader(setupBytes(t, wire2.KVPList{}))))

	parser := wire2.NewControlMessageParser(bytes.NewReader(setupBytes(t, wire2.KVPList{})), wire2.ScopeControl)
	_, _, err := wire2.ReadStreamType(parser.Reader())
	require.NoError(t, err)
	assert.ErrorIs(t, p.adoptRemote(nil, parser), errDuplicateControlStream)
}

// TestControlStreamPairReadsGoAwayAfterSetup covers the only other message the
// control stream carries.
func TestControlStreamPairReadsGoAwayAfterSetup(t *testing.T) {
	stream := setupBytes(t, wire2.KVPList{})
	goaway := &wire2.GoAwayCtrl{NewSessionURI: "moqt://other.example/", Timeout: 5000, RequestID: 12}
	buf, err := wire2.AppendControlMessage(stream, goaway)
	require.NoError(t, err)

	p := newControlStreamPair()
	require.NoError(t, adoptFrom(t, p, bytes.NewReader(buf)))

	msg, err := p.read()
	require.NoError(t, err)
	assert.Equal(t, goaway, msg)
}

func TestControlStreamPairWrite(t *testing.T) {
	ctrl := gomock.NewController(t)
	conn := NewMockConnection(ctrl)
	stream := NewMockSendStream(ctrl)

	var written bytes.Buffer
	conn.EXPECT().OpenUniStreamSync(gomock.Any()).Return(stream, nil)
	stream.EXPECT().Write(gomock.Any()).DoAndReturn(written.Write).Times(2)

	p := newControlStreamPair()
	require.NoError(t, p.open(context.Background(), conn, &wire2.Setup{Options: wire2.KVPList{}}))

	goaway := &wire2.GoAwayCtrl{Timeout: 1}
	require.NoError(t, p.write(goaway))

	parser := wire2.NewControlMessageParser(bytes.NewReader(written.Bytes()), wire2.ScopeControl)
	streamType, _, err := wire2.ReadStreamType(parser.Reader())
	require.NoError(t, err)
	_, err = parser.ParseBody(wire2.ControlMessageType(streamType))
	require.NoError(t, err)

	msg, err := parser.Parse()
	require.NoError(t, err)
	assert.Equal(t, goaway, msg)
}

func TestControlStreamPairOpenPropagatesError(t *testing.T) {
	ctrl := gomock.NewController(t)
	conn := NewMockConnection(ctrl)
	wantErr := errors.New("no streams available")
	conn.EXPECT().OpenUniStreamSync(gomock.Any()).Return(nil, wantErr)

	p := newControlStreamPair()
	err := p.open(context.Background(), conn, &wire2.Setup{Options: wire2.KVPList{}})
	assert.ErrorIs(t, err, wantErr)
}
