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
	// Version18 is draft-ietf-moq-transport-18, the current interop target.
	Version18 Version = 18
)

// ALPN returns the protocol identifier this version negotiates under, for both
// TLS ALPN and the WebTransport subprotocol.
func (v Version) ALPN() string {
	return fmt.Sprintf("moqt-%d", uint8(v))
}

func (v Version) String() string {
	return v.ALPN()
}

// supportedVersions lists what this build speaks, most preferred first. Order
// matters: it is the offer order in NextProtos, and a peer picking any entry
// gets a version we implement. Adding a draft means adding a declaration set
// and one entry here.
var supportedVersions = []Version{Version18}

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
	for _, v := range supportedVersions {
		if v.ALPN() == alpn {
			return v, true
		}
	}
	return 0, false
}
