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
