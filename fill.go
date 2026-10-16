package moqtransport

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/Eyevinn/moqtransport/internal/wire2"
	"github.com/mengelbart/qlog/moqt"
)

// Fill is draft-22's replacement for the Joining FETCH (draft-22 Section 3.4).
// A SUBSCRIBE or REQUEST_UPDATE that carries FILL_PARAMETERS asks the
// publisher to deliver the Objects of a range that already exist -- the fill
// range -- on a unidirectional stream of their own, alongside the
// subscription's own delivery. The stream starts with a FETCH_HEADER naming
// the Request ID of the message that asked, and carries Objects exactly as a
// FETCH response does. There is no answer message: the publisher FINs the
// stream once the range is delivered, resets it if it cannot be, and opens
// none at all when the range is empty or the subscription is paused.

// FillOption customises the FILL_PARAMETERS of a fill request. A parameter
// not given is inherited from the subscription.
type FillOption func(*wire2.Parameters)

// FillRange sets the fill range. Without it the fill range is the
// subscription's own location filter; the zero filter asks for the whole
// Track. Either way it is evaluated like a FETCH, so it ends at the Largest
// Object.
//
// To join a track at the start of its current Group, subscribe with
// WithFilter(NextObject()) and WithFill(FillRange(RelativeStart(1))): the
// subscription delivers what is published from now on, the fill what came
// before it in the Group, and nothing arrives twice.
func FillRange(filter LocationFilter) FillOption {
	return func(pp *wire2.Parameters) {
		*pp = append(*pp, wire2.LocationFilterParameter(filter))
	}
}

// FillTimeout bounds how long a relay should wait upstream for Objects it
// does not have before reporting them as timed out. Zero asks for only what
// is immediately available. The wire carries whole milliseconds.
func FillTimeout(d time.Duration) FillOption {
	return func(pp *wire2.Parameters) {
		*pp = append(*pp, wire2.VarintParameter(wire2.ParamFillTimeout, uint64(max(d, 0)/time.Millisecond)))
	}
}

// FillPriority sets the subscriber priority of the fill stream.
func FillPriority(priority uint8) FillOption {
	return func(pp *wire2.Parameters) {
		*pp = append(*pp, wire2.Uint8Parameter(wire2.ParamSubscriberPriority, priority))
	}
}

// FillGroupOrder sets the order the fill delivers Groups in.
func FillGroupOrder(order GroupOrder) FillOption {
	return func(pp *wire2.Parameters) {
		*pp = append(*pp, wire2.Uint8Parameter(wire2.ParamGroupOrder, uint8(order)))
	}
}

func fillParameters(opts []FillOption) wire2.Parameters {
	params := wire2.Parameters{}
	for _, opt := range opts {
		opt(&params)
	}
	return params
}

// WithFill asks the publisher for a fill alongside the subscription. Read it
// from RemoteTrack.Fill once Subscribe returns. Draft-22 only: on a draft-18
// session Subscribe fails, and a Joining FETCH is the way to do the same.
func WithFill(opts ...FillOption) SubscribeOption {
	return func(s *wire2.Subscribe) error {
		s.Parameters = append(s.Parameters, wire2.FillParametersParameter(fillParameters(opts)))
		return nil
	}
}

// FillStream is a fill this endpoint asked for: the Objects of the fill range,
// delivered on a stream of their own.
//
// A fill can also end without a stream. When the subscription's answer shows
// the range is empty, or the subscription is paused, the publisher opens
// none, and ReadObject reports the fill complete with nothing in it.
type FillStream struct {
	track     *RemoteTrack
	requestID uint64
	params    wire2.Parameters

	objects chan *FetchObject

	// done is closed when the fill is over: delivered, failed, or abandoned.
	// objects is never closed; done is what says no more will come.
	done     chan struct{}
	doneOnce sync.Once

	mu       sync.Mutex
	stream   ReceiveStream
	complete bool
	err      error
}

func newFillStream(track *RemoteTrack, requestID uint64, params wire2.Parameters) *FillStream {
	return &FillStream{
		track:     track,
		requestID: requestID,
		params:    params,
		objects:   make(chan *FetchObject, fetchObjectBuffer),
		done:      make(chan struct{}),
	}
}

// RequestID is the Request ID the fill stream's FETCH_HEADER carries: the
// SUBSCRIBE's for the fill asked for with it, the REQUEST_UPDATE's for a
// Refill.
func (f *FillStream) RequestID() uint64 { return f.requestID }

// ReadObject returns the next record of the fill, blocking until one arrives.
//
// It returns ErrFetchComplete once the fill range has been delivered in full
// and drained -- immediately, for a fill the publisher had nothing to send
// for -- and any other error if the fill failed: the publisher reset the
// stream, the request was refused, or the subscription ended first. A failed
// fill does not affect the subscription.
func (f *FillStream) ReadObject(ctx context.Context) (*FetchObject, error) {
	select {
	case o := <-f.objects:
		return o, nil
	default:
	}
	select {
	case o := <-f.objects:
		return o, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-f.done:
		select {
		case o := <-f.objects:
			return o, nil
		default:
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		if f.complete {
			return nil, ErrFetchComplete
		}
		return nil, f.err
	}
}

// Close abandons the fill with STOP_SENDING on its stream. It leaves the
// subscription alone (draft-22 Section 3.4.1).
func (f *FillStream) Close() error {
	f.mu.Lock()
	stream := f.stream
	f.mu.Unlock()
	if stream != nil {
		stream.Stop(uint32(StreamErrorCancelled))
	}
	f.finish(false, errFillClosedLocally)
	return nil
}

// finish ends the fill. The first outcome wins.
func (f *FillStream) finish(complete bool, err error) {
	f.doneOnce.Do(func() {
		f.mu.Lock()
		f.complete, f.err = complete, err
		f.mu.Unlock()
		close(f.done)
	})
}

func (f *FillStream) isDone() bool {
	select {
	case <-f.done:
		return true
	default:
		return false
	}
}

// expect decides, from the publisher's answer, whether a fill stream is
// coming. Draft-22 has the publisher evaluate the fill range like a FETCH
// against the Largest Object it reports in the answer, and open no stream for
// an empty range or a paused subscription; the subscriber can work out the
// same, and not wait for a stream that will never be opened.
func (f *FillStream) expect(filter LocationFilter, largest Location, hasLargest, paused bool) {
	f.mu.Lock()
	attached := f.stream != nil
	f.mu.Unlock()
	if attached {
		return
	}
	if inner, ok := f.params.LocationFilter(); ok {
		filter = inner
	}
	if _, ok := filter.FetchRange(largest, hasLargest); paused || !ok {
		f.finish(true, nil)
	}
}

// awaitOrder implements fetchReceiver. The fill's Group Order can depend on
// the publisher's preference for the Track, which arrives in SUBSCRIBE_OK, and
// a fill stream can overtake it.
func (f *FillStream) awaitOrder(ctx context.Context) (GroupOrder, error) {
	if order, ok, err := f.params.GroupOrder(); err != nil || ok {
		return order, err
	}
	return f.track.awaitGroupOrder(ctx)
}

// attach implements fetchReceiver.
func (f *FillStream) attach(stream ReceiveStream) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.isDone() || f.stream != nil {
		return false
	}
	f.stream = stream
	return true
}

// deliver implements fetchReceiver.
func (f *FillStream) deliver(ctx context.Context, o *FetchObject) error {
	select {
	case f.objects <- o:
		return nil
	case <-ctx.Done():
		return context.Cause(ctx)
	case <-f.done:
		return errFillClosedLocally
	}
}

// responseComplete implements fetchReceiver. The stream counts toward the
// subscription's PUBLISH_DONE Stream Count whichever way it ended.
func (f *FillStream) responseComplete(complete bool) {
	if complete {
		f.finish(true, nil)
	} else {
		f.finish(false, errFillReset)
	}
	f.track.dataStreamEnded()
}

// Fill returns the fill asked for with WithFill, or nil if Subscribe was not
// asked for one.
func (t *RemoteTrack) Fill() *FillStream {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.fill
}

// Refill asks for another fill on an established subscription, with a
// REQUEST_UPDATE carrying FILL_PARAMETERS and nothing else. A fill already
// under way is not cancelled by it. Draft-22 only.
func (t *RemoteTrack) Refill(opts ...FillOption) (*FillStream, error) {
	if !t.version.AtLeast(wire2.Version22) {
		return nil, fmt.Errorf("%w: fill", ErrNotInThisDraft)
	}
	params := fillParameters(opts)
	var fill *FillStream
	err := t.update(wire2.Parameters{wire2.FillParametersParameter(params)}, func(requestID uint64) *FillStream {
		fill = newFillStream(t, requestID, params)
		return fill
	})
	if err != nil {
		return nil, err
	}
	return fill, nil
}

// FillHandler serves the fills subscribers ask for (draft-22 only).
//
// HandleFill runs on a goroutine of its own, once per fill the library has
// decided to open a stream for: the range is not empty and the subscription
// is not paused. A nil handler on the session refuses every fill by resetting
// its stream, which is how draft-22 says a fill failed; the subscription is
// not affected.
type FillHandler interface {
	HandleFill(*FillRequest)
}

// FillHandlerFunc adapts a function to [FillHandler].
type FillHandlerFunc func(*FillRequest)

func (f FillHandlerFunc) HandleFill(r *FillRequest) { f(r) }

// FillRequest is a fill a subscriber asked for on one of this endpoint's
// subscriptions. Its range has already been resolved against the
// subscription's Largest Object.
type FillRequest struct {
	sub       *Subscription
	requestID uint64
	rng       LocationRange
	order     GroupOrder
	priority  uint8
	params    wire2.Parameters

	mu       sync.Mutex
	answered bool
}

// RequestID is the Request ID of the SUBSCRIBE or REQUEST_UPDATE that asked
// for the fill, which its stream's FETCH_HEADER carries.
func (r *FillRequest) RequestID() uint64 { return r.requestID }

// Subscription is the subscription the fill belongs to.
func (r *FillRequest) Subscription() *Subscription { return r.sub }

// Namespace is the Track Namespace being filled.
func (r *FillRequest) Namespace() []string { return r.sub.Namespace() }

// Track is the Track Name being filled.
func (r *FillRequest) Track() string { return r.sub.Track() }

// Range is the fill range, both ends inclusive. It never extends past the
// Largest Object the subscription had when the fill was asked for.
func (r *FillRequest) Range() LocationRange { return r.rng }

// GroupOrder is the order the fill must deliver Groups in.
func (r *FillRequest) GroupOrder() GroupOrder { return r.order }

// Timeout is the subscriber's FILL_TIMEOUT, and whether it gave one: how long
// a relay should spend waiting upstream before reporting Objects as timed out
// with EndOfRangeTimedOut.
func (r *FillRequest) Timeout() (time.Duration, bool) { return r.params.FillTimeout() }

// Parameters is the FILL_PARAMETERS block as the subscriber sent it.
func (r *FillRequest) Parameters() Parameters { return r.params }

// Context is the subscription's: it ends when the subscription does, and with
// it any fill still being written.
func (r *FillRequest) Context() context.Context { return r.sub.Context() }

// Accept opens the fill stream. Write the range's Objects to it in the Group
// Order and Close it, which tells the subscriber the fill is complete.
func (r *FillRequest) Accept() (*FetchResponse, error) {
	if err := r.claim(); err != nil {
		return nil, err
	}
	return r.sub.openFill(r)
}

// Reject reports that the fill cannot be delivered. There is no error message
// for a fill: the draft has the publisher open the stream and reset it right
// after the FETCH_HEADER, which is what this does.
func (r *FillRequest) Reject(code StreamErrorCode) error {
	if err := r.claim(); err != nil {
		return err
	}
	response, err := r.sub.openFill(r)
	if err != nil {
		return err
	}
	response.Reset(code)
	return nil
}

func (r *FillRequest) claim() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.answered {
		return errRequestAlreadyAnswered
	}
	r.answered = true
	return nil
}

// startFill decides whether a fill asked for with requestID gets a stream,
// and if so hands it to the session's FillHandler (draft-22 Section 3.4.1).
// It runs after the SUBSCRIBE_OK or REQUEST_UPDATE_OK, so the Largest Object
// the range is resolved against is the one the subscriber was told.
func (s *Subscription) startFill(requestID uint64, inner wire2.Parameters) {
	s.mu.Lock()
	paused := !s.forward
	filter, hasFilter := s.filter, s.hasFilter
	largest, hasLargest := s.largest, s.hasLargest
	order, priority := s.groupOrder, s.priority
	s.mu.Unlock()

	if paused {
		// No stream, and resuming does not open one later either.
		return
	}
	if f, ok := inner.LocationFilter(); ok {
		filter, hasFilter = f, true
	}
	if !hasFilter {
		filter = LocationFilter{}
	}
	rng, ok := filter.FetchRange(largest, hasLargest)
	if !ok {
		return
	}
	if o, ok, err := inner.GroupOrder(); err == nil && ok {
		order = o
	}
	if p, ok := inner.Get(wire2.ParamSubscriberPriority); ok {
		priority = uint8(p.Number)
	}

	req := &FillRequest{
		sub:       s,
		requestID: requestID,
		rng:       rng,
		order:     order,
		priority:  priority,
		params:    inner,
	}
	handler := s.session.fillHandler()
	if handler == nil {
		_ = req.Reject(StreamErrorInternal)
		return
	}
	go handler.HandleFill(req)
}

// openFill opens a fill stream and writes its FETCH_HEADER. The stream counts
// toward PUBLISH_DONE's Stream Count, and is reset if the subscription ends
// while it is still open.
func (s *Subscription) openFill(r *FillRequest) (*FetchResponse, error) {
	stream, err := s.session.openUniStream(s.ctx)
	if err != nil {
		return nil, err
	}
	applyPriority(stream, s.session.priorityMapper(), ObjectPriority{
		SubscriberPriority: r.priority,
		PublisherPriority:  wire2.DefaultPublisherPriority,
		GroupOrder:         r.order,
	})
	if _, err := stream.Write(wire2.AppendFetchHeader(nil, r.requestID)); err != nil {
		stream.Reset(uint32(StreamErrorInternal))
		return nil, err
	}
	s.qlog.logStreamType(moqt.OwnerLocal, stream, moqt.StreamTypeFetchHeader)

	response := &FetchResponse{
		stream: stream,
		writer: wire2.NewFetchWriter(r.order, s.version),
		qlog:   s.qlog,
	}
	response.onDone = func() {
		s.mu.Lock()
		delete(s.fills, response)
		s.mu.Unlock()
	}
	s.mu.Lock()
	s.streamCount++
	if s.fills == nil {
		s.fills = map[*FetchResponse]struct{}{}
	}
	s.fills[response] = struct{}{}
	s.mu.Unlock()
	return response, nil
}

// resetFills resets every fill stream still open, which draft-22 requires
// when the subscription is cancelled and PUBLISH_DONE requires before it is
// sent.
func (s *Subscription) resetFills(code StreamErrorCode) {
	s.mu.Lock()
	fills := make([]*FetchResponse, 0, len(s.fills))
	for f := range s.fills {
		fills = append(fills, f)
	}
	s.mu.Unlock()
	for _, f := range fills {
		f.Reset(code)
	}
}

var (
	errFillClosedLocally = errors.New("fill closed by the application")
	errFillReset         = errors.New("the publisher reset the fill stream")
)
