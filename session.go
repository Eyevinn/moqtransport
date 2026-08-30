package moqtransport

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/Eyevinn/moqtransport/internal/wire2"
)

// Session is one MOQT session over a [Connection]
// (draft-ietf-moq-transport-18, Section 3.3).
//
// Fill in the handlers, then call Run. Run performs the SETUP exchange and
// returns once both control streams are up, so everything after it is on an
// established session; the background loops it starts run until the connection
// or the context ends.
type Session struct {
	// SubscribeHandler answers incoming SUBSCRIBE requests. A nil handler
	// rejects them with NOT_SUPPORTED, which is a legitimate answer and better
	// than leaving the peer waiting.
	SubscribeHandler SubscribeHandler

	// Path is the PATH Setup Option, the path-abempty portion of a moqt:// URI.
	// It is for native QUIC clients only: a server that sends one, or anyone
	// who sends one over WebTransport, has the session closed.
	Path string

	// Authority is the AUTHORITY Setup Option, naming the server the session is
	// for when the transport does not.
	Authority string

	// Implementation is a free-form name and version reported to the peer, for
	// its logs. It has no protocol meaning.
	Implementation string

	// MaxPendingStreams bounds how many streams are buffered while the control
	// streams are still being established. Zero uses a default. Section 3.3
	// says such streams SHOULD be buffered and MAY be reset instead; the bound
	// is what makes the SHOULD safe against a peer that opens streams and never
	// sends SETUP.
	MaxPendingStreams int

	conn    Connection
	version wire2.Version
	control *controlStreamPair

	requestIDs *requestIDGenerator
	remote     *remoteTrackIndex

	ctx    context.Context
	cancel context.CancelCauseFunc

	pendingUni  *pendingStreams[pendingUniStream]
	pendingBidi *pendingStreams[Stream]

	mu             sync.Mutex
	nextTrackAlias uint64
	localAliases   map[string]uint64
}

// trackAliasWait is how long an incoming data stream waits for the control
// message that establishes its Track Alias.
//
// Section 11.4.2 allows buffering "for a brief period to handle reordering
// with the control message that establishes the Track Alias", and blocking the
// stream's reader is also what withholds its flow control meanwhile. The wait
// has to end, though: an alias we never subscribed to would otherwise hold a
// goroutine for the life of the session.
const trackAliasWait = 2 * time.Second

// Run establishes the session on conn and returns once both control streams
// are up.
//
// The accept loops start before SETUP goes out, so a peer that opens data or
// request streams first has them buffered rather than dropped -- with 0-RTT
// either side may speak first, and there is no client/server ordering in the
// handshake to rely on.
//
// ctx bounds the handshake and the session: cancelling it ends the session.
func (s *Session) Run(ctx context.Context, conn Connection) error {
	if s.conn != nil {
		return errSessionAlreadyRunning
	}
	version, err := negotiatedVersion(conn)
	if err != nil {
		return err
	}

	s.conn = conn
	s.version = version
	s.control = newControlStreamPair()
	s.requestIDs = newRequestIDGenerator(conn.Perspective())
	s.remote = newRemoteTrackIndex()
	s.localAliases = map[string]uint64{}
	s.pendingUni = newPendingStreams[pendingUniStream](s.MaxPendingStreams)
	s.pendingBidi = newPendingStreams[Stream](s.MaxPendingStreams)
	s.ctx, s.cancel = context.WithCancelCause(context.WithoutCancel(ctx))

	// Follow the caller's context without tying the session's lifetime to a
	// context that may be a short-lived handshake one.
	go func() {
		select {
		case <-ctx.Done():
			s.closeWithError(SessionErrorNoError, "context cancelled")
		case <-s.ctx.Done():
		}
	}()

	go s.acceptUniStreams()
	go s.acceptBidiStreams()

	setup, err := s.setupMessage()
	if err != nil {
		s.closeWithError(SessionErrorInternal, err.Error())
		return err
	}
	if err := s.control.open(ctx, conn, setup); err != nil {
		s.closeWithError(SessionErrorInternal, err.Error())
		return err
	}
	if _, err := s.control.awaitPeerSetup(ctx); err != nil {
		s.closeWithError(SessionErrorProtocolViolation, err.Error())
		return err
	}

	// The handshake is done, so anything held while it ran can be handled now.
	for _, held := range s.pendingUni.release() {
		go s.dispatchUniStream(held)
	}
	for _, stream := range s.pendingBidi.release() {
		go s.handleBidiStream(stream)
	}
	go s.readControlStream()
	go s.receiveDatagrams()
	return nil
}

// Version returns the draft this session speaks.
func (s *Session) Version() wire2.Version { return s.version }

// Context is cancelled when the session ends. [context.Cause] says why.
func (s *Session) Context() context.Context { return s.ctx }

// Close ends the session, closing the underlying connection.
func (s *Session) Close(code SessionErrorCode, reason string) error {
	return s.closeWithError(code, reason)
}

func (s *Session) closeWithError(code SessionErrorCode, reason string) error {
	s.cancel(fmt.Errorf("session closed: %v: %v", code, reason))
	return s.conn.CloseWithError(uint64(code), reason)
}

// fail ends the session over a protocol error, using the error's own code
// where it carries one.
func (s *Session) fail(err error) {
	var protocolErr ProtocolError
	if errors.As(err, &protocolErr) {
		s.closeWithError(protocolErr.Code(), protocolErr.message)
		return
	}
	s.closeWithError(SessionErrorProtocolViolation, err.Error())
}

// setupMessage builds our SETUP. There is no version field: draft-17 removed
// it, and the version came from the ALPN before a byte of MOQT was written.
func (s *Session) setupMessage() (*wire2.Setup, error) {
	setup := &wire2.Setup{Options: wire2.KVPList{}}
	if s.Path != "" {
		if s.conn.Protocol() != ProtocolQUIC {
			return nil, errPathOverWebTransport
		}
		if s.conn.Perspective() != PerspectiveClient {
			return nil, errPathFromServer
		}
		setup.Options = append(setup.Options, wire2.PathOption(s.Path))
	}
	if s.Authority != "" {
		setup.Options = append(setup.Options, wire2.AuthorityOption(s.Authority))
	}
	if s.Implementation != "" {
		setup.Options = append(setup.Options, wire2.ImplementationOption(s.Implementation))
	}
	return setup, nil
}

// negotiatedVersion maps the negotiated protocol identifier to a version. For
// native QUIC that is the TLS ALPN; for WebTransport it is the subprotocol,
// which the connection reports through the same method.
func negotiatedVersion(conn Connection) (wire2.Version, error) {
	alpn := conn.NegotiatedALPN()
	version, ok := wire2.VersionFromALPN(alpn)
	if !ok {
		return 0, fmt.Errorf("%w: peer negotiated %q, this build speaks %v",
			errUnsupportedVersion, alpn, strings.Join(wire2.SupportedALPNs(), ", "))
	}
	return version, nil
}

// Subscribe opens a subscription to a track and waits for the publisher to
// answer it.
func (s *Session) Subscribe(ctx context.Context, namespace []string, track string, opts ...SubscribeOption) (*RemoteTrack, error) {
	msg := &wire2.Subscribe{
		RequestID:      s.requestIDs.nextID(),
		TrackNamespace: namespaceFields(namespace),
		TrackName:      []byte(track),
		Parameters:     wire2.Parameters{},
	}
	for _, opt := range opts {
		if err := opt(msg); err != nil {
			return nil, err
		}
	}

	stream, err := s.conn.OpenStreamSync(ctx)
	if err != nil {
		return nil, err
	}
	rs := newRequestStream(s.ctx, stream)
	rt := newRemoteTrack(rs, s, msg.RequestID, namespace, track)

	// The reader starts before the request goes out: the publisher may answer
	// the moment it has the bytes, and a response that arrived while we were
	// still setting up would have nobody to read it.
	go func() {
		if err := rt.run(); err != nil {
			s.failIfProtocolError(err)
		}
	}()

	if err := rs.write(msg); err != nil {
		rs.cancel(StreamErrorInternal, err)
		return nil, err
	}
	if err := rt.awaitEstablished(ctx); err != nil {
		rs.cancel(StreamErrorCancelled, err)
		return nil, err
	}
	return rt, nil
}

// registerTrackAlias implements subscriberSession.
func (s *Session) registerTrackAlias(alias uint64, t *RemoteTrack) error {
	return s.remote.add(alias, t)
}

// unregisterTrackAlias implements subscriberSession.
func (s *Session) unregisterTrackAlias(alias uint64, t *RemoteTrack) {
	s.remote.remove(alias, t)
}

// trackAlias implements publisherSession.
//
// One alias per track, shared by every subscription to it. Section 11.1
// forbids one alias naming two tracks; it says nothing against two
// subscriptions to one track sharing an alias, and a subscriber that has
// subscribed twice would otherwise be sent every Object twice.
func (s *Session) trackAlias(namespace []string, track string) uint64 {
	key := strings.Join(namespace, "/") + "\x00" + track
	s.mu.Lock()
	defer s.mu.Unlock()
	if alias, ok := s.localAliases[key]; ok {
		return alias
	}
	alias := s.nextTrackAlias
	s.nextTrackAlias++
	s.localAliases[key] = alias
	return alias
}

// openUniStream implements publisherSession.
func (s *Session) openUniStream(ctx context.Context) (SendStream, error) {
	return s.conn.OpenUniStreamSync(ctx)
}

// sendDatagram implements publisherSession.
func (s *Session) sendDatagram(b []byte) error {
	return s.conn.SendDatagram(b)
}

// pendingUniStream is a classified unidirectional stream waiting for the
// handshake to finish. The leading stream type has already been read, which is
// what makes the buffering possible at all: the control stream cannot be held
// back, since it is the very thing setup is waiting for.
type pendingUniStream struct {
	stream     ReceiveStream
	reader     *bufio.Reader
	streamType wire2.StreamType
	kind       wire2.UniStreamKind
}

// acceptUniStreams takes the peer's unidirectional streams: its control
// stream, subgroup and fetch data streams, and padding.
func (s *Session) acceptUniStreams() {
	for {
		stream, err := s.conn.AcceptUniStream(s.ctx)
		if err != nil {
			return
		}
		// Each stream is classified on its own goroutine, because reading its
		// type varint blocks and the accept loop must stay free for the
		// control stream behind it.
		go s.handleUniStream(stream)
	}
}

func (s *Session) handleUniStream(stream ReceiveStream) {
	reader := bufio.NewReader(stream)
	streamType, kind, err := wire2.ReadStreamType(reader)
	if err != nil {
		if errors.Is(err, io.EOF) {
			// A stream that carried nothing at all. Nothing to do and nothing
			// to complain about.
			return
		}
		s.fail(err)
		return
	}

	// The control stream is never buffered: setup completes only when it
	// arrives, so holding it would deadlock the handshake against itself.
	if kind == wire2.UniStreamControl {
		s.adoptControlStream(stream, reader)
		return
	}

	held, err := s.pendingUni.hold(pendingUniStream{
		stream:     stream,
		reader:     reader,
		streamType: streamType,
		kind:       kind,
	})
	if err != nil {
		// The buffer is full, which Section 3.3 lets us answer by resetting
		// the stream rather than buffering it.
		stream.Stop(uint32(StreamErrorExcessiveLoad))
		return
	}
	if held {
		return
	}
	s.dispatchUniStream(pendingUniStream{
		stream:     stream,
		reader:     reader,
		streamType: streamType,
		kind:       kind,
	})
}

func (s *Session) dispatchUniStream(u pendingUniStream) {
	switch u.kind {
	case wire2.UniStreamSubgroup:
		s.handleSubgroupStream(u.stream, u.reader, u.streamType)
	case wire2.UniStreamPadding:
		// Padding carries nothing. Read it away so the sender's flow control
		// is released, which is the only thing it wants.
		io.Copy(io.Discard, u.reader)
	case wire2.UniStreamFetch:
		// FETCH is not implemented on this branch yet. Telling the publisher
		// to stop is better than reading a response nothing will consume.
		u.stream.Stop(uint32(StreamErrorInternal))
	}
}

func (s *Session) adoptControlStream(stream ReceiveStream, reader *bufio.Reader) {
	parser := wire2.NewControlMessageParserFromReader(reader, wire2.ScopeControl)
	if err := s.control.adoptRemote(stream, parser); err != nil {
		s.fail(err)
	}
}

// handleSubgroupStream routes one subgroup stream to the subscriptions it
// belongs to and delivers its Objects.
func (s *Session) handleSubgroupStream(stream ReceiveStream, reader *bufio.Reader, streamType wire2.StreamType) {
	header, err := wire2.ParseSubgroupHeader(streamType, reader)
	if err != nil {
		s.fail(err)
		return
	}

	ctx, cancel := context.WithTimeout(s.ctx, trackAliasWait)
	tracks, ok := s.remote.await(ctx, header.TrackAlias)
	cancel()
	if !ok {
		// An alias we have no subscription for. Section 11.4.2 lets us abandon
		// the stream rather than close the session: the publisher may simply
		// be slower to notice an unsubscribe than we were to send it.
		stream.Stop(uint32(StreamErrorCancelled))
		return
	}

	receiver := newSubgroupReceiver(stream, header, reader, tracks[0].defaultPriority())
	err = receiver.receive(func(o *Object) error {
		for _, t := range tracks {
			if err := t.deliver(s.ctx, o); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		s.failIfProtocolError(err)
	}
}

// acceptBidiStreams takes the peer's request streams. Section 3.3 allows seven
// message types to open one, and a stream beginning with anything else is a
// PROTOCOL_VIOLATION.
func (s *Session) acceptBidiStreams() {
	for {
		stream, err := s.conn.AcceptStream(s.ctx)
		if err != nil {
			return
		}
		held, err := s.pendingBidi.hold(stream)
		if err != nil {
			stream.Reset(uint32(StreamErrorExcessiveLoad))
			stream.Stop(uint32(StreamErrorExcessiveLoad))
			continue
		}
		if held {
			continue
		}
		go s.handleBidiStream(stream)
	}
}

func (s *Session) handleBidiStream(stream Stream) {
	rs := newRequestStream(s.ctx, stream)
	msg, err := rs.readRequest()
	if err != nil {
		if errors.Is(err, io.EOF) {
			rs.cancel(StreamErrorCancelled, err)
			return
		}
		rs.cancel(StreamErrorInternal, err)
		s.fail(err)
		return
	}

	switch m := msg.(type) {
	case *wire2.Subscribe:
		req := newSubscribeRequest(rs, s, m)
		if err := req.serve(s.SubscribeHandler); err != nil {
			s.failIfProtocolError(err)
		}

	default:
		// A request type this build does not implement. The codepoint is known
		// -- an unknown one would have failed in readRequest -- so the answer
		// is NOT_SUPPORTED rather than closing the session.
		err := rs.finish(&wire2.RequestError{
			ErrorCode:   uint64(RequestErrorNotSupported),
			ErrorReason: fmt.Sprintf("%v is not supported", msg.Type()),
		})
		rs.cancel(StreamErrorCancelled, errRequestTypeNotSupported)
		if err != nil {
			return
		}
	}
}

// readControlStream reads what follows SETUP on the peer's control stream.
// Only GOAWAY belongs there.
func (s *Session) readControlStream() {
	for {
		msg, err := s.control.read()
		if err != nil {
			if errors.Is(err, io.EOF) {
				// Section 3.3: a control stream MUST NOT be closed for the
				// life of the session.
				s.fail(errControlStreamClosed)
			}
			return
		}
		if _, ok := msg.(*wire2.GoAwayCtrl); !ok {
			s.fail(errUnexpectedMessageOnControlStream)
			return
		}
		// GOAWAY handling belongs with session migration, which is not
		// implemented yet. Reading it keeps the stream moving.
	}
}

// receiveDatagrams delivers Objects sent with Object Forwarding Preference =
// Datagram.
func (s *Session) receiveDatagrams() {
	for {
		data, err := s.conn.ReceiveDatagram(s.ctx)
		if err != nil {
			return
		}
		datagram, err := wire2.ParseObjectDatagram(data)
		if err != nil {
			s.fail(err)
			return
		}

		// Unlike a subgroup stream there is nothing to hold open here, so a
		// datagram for an alias we do not know is dropped rather than waited
		// on. Section 11.3 allows exactly that.
		tracks, ok := s.remote.await(s.ctx, datagram.TrackAlias)
		if !ok {
			continue
		}
		priority := datagram.Priority
		if datagram.DefaultPriority {
			priority = tracks[0].defaultPriority()
		}
		object := &Object{
			GroupID:              datagram.GroupID,
			ObjectID:             datagram.ObjectID,
			ForwardingPreference: ObjectForwardingPreferenceDatagram,
			Priority:             priority,
			Properties:           datagram.Properties,
			Status:               datagram.Status,
			Payload:              datagram.Payload,
		}
		for _, t := range tracks {
			if err := t.deliver(s.ctx, object); err != nil {
				break
			}
		}
	}
}

// failIfProtocolError closes the session for errors that require it and lets
// everything else stay local to the request it happened on.
func (s *Session) failIfProtocolError(err error) {
	var protocolErr ProtocolError
	if errors.As(err, &protocolErr) {
		s.fail(err)
	}
}

func namespaceFields(namespace []string) [][]byte {
	fields := make([][]byte, len(namespace))
	for i, f := range namespace {
		fields[i] = []byte(f)
	}
	return fields
}

var (
	errSessionAlreadyRunning = errors.New("session is already running")
	errUnsupportedVersion    = errors.New("unsupported MOQT version")
	errPathOverWebTransport  = errors.New("the PATH setup option is for native QUIC only")
	errPathFromServer        = errors.New("only a client may send the PATH setup option")

	errRequestTypeNotSupported = errors.New("request type is not supported")

	errControlStreamClosed = ProtocolError{
		code:    SessionErrorProtocolViolation,
		message: "peer closed its control stream",
	}

	errUnexpectedMessageOnControlStream = ProtocolError{
		code:    SessionErrorProtocolViolation,
		message: "unexpected message type on the control stream",
	}
)
