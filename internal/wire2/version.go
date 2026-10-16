package wire2

import "fmt"

// Version identifies a MOQT draft this package can speak.
//
// From draft-17 onwards a version number never appears on the wire: SETUP has
// no version field and negotiation is entirely out of band, through the TLS
// ALPN for native QUIC and the WebTransport subprotocol otherwise. So a
// Version is only an internal label that selects a declaration set, and the
// ALPN string is the whole interop contract.
type Version uint8

const (
	// Version18 is draft-ietf-moq-transport-18.
	Version18 Version = 18

	// Version22 is draft-ietf-moq-transport-22. It and draft-18 are the two
	// interop targets the working group named at its October 2026 interim.
	Version22 Version = 22
)

// ALPN returns the protocol identifier this version negotiates under, for both
// TLS ALPN and the WebTransport subprotocol.
func (v Version) ALPN() string {
	return fmt.Sprintf("moqt-%d", uint8(v))
}

func (v Version) String() string {
	return v.ALPN()
}

// knownVersions are the drafts this build has codecs for. A peer negotiating
// any of them gets a session, whether or not it is offered.
var knownVersions = []Version{Version22, Version18}

// supportedVersions lists what this build offers, most preferred first. Order
// matters: it is the offer order in NextProtos, and Go's TLS server picks the
// first of its own protocols the client also offers, so a server offering
// this list speaks the newest draft with any client that can. Adding a draft
// means adding a declaration set, an entry in knownVersions, and one here.
var supportedVersions = []Version{Version22, Version18}

// AtLeast reports whether v is draft n or later. Behavior that changed between
// drafts is gated on it rather than on equality, so a third declaration set
// inherits the newer behavior by default.
func (v Version) AtLeast(n Version) bool {
	return v >= n
}

// SupportedALPNs returns the protocol identifiers to offer, most preferred
// first.
func SupportedALPNs() []string {
	alpns := make([]string, 0, len(supportedVersions))
	for _, v := range supportedVersions {
		alpns = append(alpns, v.ALPN())
	}
	return alpns
}

// VersionFromALPN maps a negotiated protocol identifier to its version.
//
// An unrecognized identifier is not something to recover from: with no version
// field in SETUP there is no in-band way to discover what the peer meant, so
// the session cannot continue.
func VersionFromALPN(alpn string) (Version, bool) {
	for _, v := range knownVersions {
		if v.ALPN() == alpn {
			return v, true
		}
	}
	return 0, false
}
