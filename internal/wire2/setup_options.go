package wire2

import "fmt"

// Setup Option types (draft-ietf-moq-transport-18, Section 15.4).
//
// Setup Options are Key-Value-Pairs carried in the SETUP message, spanning the
// whole message body. They use a namespace separate from Message Parameters
// and constant across MOQT versions, so the same codepoint means different
// things in the two registries: 0x04 is MAX_AUTH_TOKEN_CACHE_SIZE here and
// RENDEZVOUS_TIMEOUT as a Message Parameter.
//
// Unlike a Message Parameter, an unrecognized Setup Option MUST be ignored
// rather than closing the session, and duplicates of unknown options MUST be
// tolerated. That is possible precisely because a Key-Value-Pair is
// self-describing: the parity of the Type says how long the value is.
const (
	SetupOptionPath                  uint64 = 0x01
	SetupOptionAuthorizationToken    uint64 = 0x03
	SetupOptionMaxAuthTokenCacheSize uint64 = 0x04
	SetupOptionAuthority             uint64 = 0x05
	SetupOptionMOQTImplementation    uint64 = 0x07
)

var setupOptionNames = map[uint64]string{
	SetupOptionPath:                  "PATH",
	SetupOptionAuthorizationToken:    "AUTHORIZATION_TOKEN",
	SetupOptionMaxAuthTokenCacheSize: "MAX_AUTH_TOKEN_CACHE_SIZE",
	SetupOptionAuthority:             "AUTHORITY",
	SetupOptionMOQTImplementation:    "MOQT_IMPLEMENTATION",
}

// SetupOptionName returns the registered name of a Setup Option type.
func SetupOptionName(typ uint64) string {
	if name, ok := setupOptionNames[typ]; ok {
		return name
	}
	if IsGreaseSetupOption(typ) {
		return fmt.Sprintf("GREASE(%#x)", typ)
	}
	return fmt.Sprintf("UNKNOWN(%#x)", typ)
}

// greaseBase and greaseStep define the reserved greasing codepoints,
// 0x7f * N + 0x9D (Section 15.4). A peer sends them to keep implementations
// honest about ignoring what they do not recognize, so they are never an error.
const (
	greaseBase = 0x9D
	greaseStep = 0x7F
)

// IsGreaseSetupOption reports whether typ is one of the reserved greasing
// codepoints. They carry no meaning and MUST be ignored, like any other
// unrecognized option; naming them only makes logs easier to read.
func IsGreaseSetupOption(typ uint64) bool {
	return typ >= greaseBase && (typ-greaseBase)%greaseStep == 0
}

// KnownSetupOption reports whether typ is registered in this version of MOQT.
// A false result is not an error: the receiver ignores the option.
func KnownSetupOption(typ uint64) bool {
	_, ok := setupOptionNames[typ]
	return ok
}

// PathOption returns the PATH Setup Option, the path-abempty portion of a
// moqt:// URI with the query appended after a '?' if present (Section
// 10.3.1.2). It is for native QUIC only: a server that sends one, or anyone
// who sends one over WebTransport, MUST have the session closed.
func PathOption(path string) KeyValuePair {
	return KeyValuePair{Type: SetupOptionPath, ValueBytes: []byte(path)}
}

// AuthorityOption returns the AUTHORITY Setup Option, the authority portion of
// a moqt:// URI (Section 10.3.1.1). Native QUIC and clients only, like PATH.
func AuthorityOption(authority string) KeyValuePair {
	return KeyValuePair{Type: SetupOptionAuthority, ValueBytes: []byte(authority)}
}

// MaxAuthTokenCacheSizeOption returns the MAX_AUTH_TOKEN_CACHE_SIZE option, the
// bytes of registered authorization tokens the sender will hold for this
// session. Omitting it means 0, which forbids token aliases entirely.
func MaxAuthTokenCacheSizeOption(size uint64) KeyValuePair {
	return KeyValuePair{Type: SetupOptionMaxAuthTokenCacheSize, ValueVarInt: size}
}

// ImplementationOption returns the MOQT_IMPLEMENTATION option naming the
// sender's implementation and version. The draft says endpoints SHOULD send
// one: it is what makes an interop failure attributable.
func ImplementationOption(name string) KeyValuePair {
	return KeyValuePair{Type: SetupOptionMOQTImplementation, ValueBytes: []byte(name)}
}

// Path returns the PATH option's value.
func (m *Setup) Path() (string, bool) { return m.stringOption(SetupOptionPath) }

// Authority returns the AUTHORITY option's value.
func (m *Setup) Authority() (string, bool) { return m.stringOption(SetupOptionAuthority) }

// Implementation returns the MOQT_IMPLEMENTATION option's value.
func (m *Setup) Implementation() (string, bool) {
	return m.stringOption(SetupOptionMOQTImplementation)
}

// MaxAuthTokenCacheSize returns the peer's token cache budget. The default when
// the option is absent is 0, which prohibits token aliases.
func (m *Setup) MaxAuthTokenCacheSize() uint64 {
	if p, ok := m.Options.Get(SetupOptionMaxAuthTokenCacheSize); ok {
		return p.ValueVarInt
	}
	return 0
}

func (m *Setup) stringOption(typ uint64) (string, bool) {
	p, ok := m.Options.Get(typ)
	if !ok {
		return "", false
	}
	return string(p.ValueBytes), true
}

// ValidateSetup checks the options a peer sent against the rules that depend
// on who sent them and over what transport.
//
// PATH and AUTHORITY are for a client on native QUIC only. Receiving either
// from a server, or over WebTransport, is a session error: the URI is carried
// by the transport there, so the option is meaningless and possibly an attempt
// to reach a different origin. Unknown options are not checked at all -- they
// MUST be ignored.
func ValidateSetup(m *Setup, fromClient bool, webTransport bool) error {
	for _, typ := range []uint64{SetupOptionPath, SetupOptionAuthority} {
		if _, ok := m.Options.Get(typ); !ok {
			continue
		}
		if !fromClient {
			return setupOptionError{option: typ, reason: "sent by a server"}
		}
		if webTransport {
			return setupOptionError{option: typ, reason: "sent over WebTransport"}
		}
	}
	return nil
}
