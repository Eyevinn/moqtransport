package moqtransport

// FetchResponseWriter implements ResponseWriter and FetchPublisher for FETCH messages.
type FetchResponseWriter struct {
	id         uint64
	session    *Session
	localTrack *localTrack
	handled    bool
}

// Accept implements ResponseWriter.
func (f *FetchResponseWriter) Accept() error {
	f.handled = true
	return f.session.acceptFetch(f.id)
}

// Reject implements ResponseWriter.
func (f *FetchResponseWriter) Reject(code uint64, reason string) error {
	f.handled = true
	return f.session.rejectFetch(f.id, code, reason)
}

// FetchStream returns a FetchStream for writing objects.
func (f *FetchResponseWriter) FetchStream() (*FetchStream, error) {
	return f.localTrack.getFetchStream()
}
