package wire

import (
	"fmt"

	"github.com/quic-go/quic-go/quicvarint"
)

type Version uint64

const (
	VersionDraft14 Version = 0xff00000e // draft-ietf-moq-transport-14
	VersionDraft16 Version = 0xff000010 // draft-ietf-moq-transport-16

	CurrentVersion = VersionDraft14 // default for backward compat
)

// ALPN protocol identifiers
const (
	ALPNDraft14 = "moq-00"  // all drafts prior to draft-15
	ALPNDraft16 = "moqt-16" // draft-16 and its pattern for future drafts
)

// SupportedVersions lists versions that use in-band SETUP negotiation
// (draft-14 and earlier). Draft-16+ negotiate via ALPN and don't appear here.
var SupportedVersions = []Version{VersionDraft14}

// VersionFromALPN returns the MoQ version implied by the given ALPN string.
// For "moq-00" (pre-draft-15), the version must still be negotiated in SETUP
// messages, so version 0 is returned. For "moqt-NN" ALPNs, the specific
// version is returned. The bool indicates whether the ALPN is recognized.
func VersionFromALPN(alpn string) (Version, bool) {
	switch alpn {
	case ALPNDraft14:
		return 0, true // version negotiated via SETUP, not ALPN
	case ALPNDraft16:
		return VersionDraft16, true
	default:
		return 0, false
	}
}

// ALPNForVersion returns the ALPN protocol string for a given MoQ version.
func ALPNForVersion(v Version) string {
	switch v {
	case VersionDraft16:
		return ALPNDraft16
	default:
		return ALPNDraft14
	}
}

// NegotiatedViaALPN reports whether this version uses ALPN-only negotiation
// (no version fields in SETUP messages). This is true for draft-16 and later.
func (v Version) NegotiatedViaALPN() bool {
	return v >= VersionDraft16
}

func (v Version) String() string {
	return fmt.Sprintf("0x%x", uint64(v))
}

func (v Version) Len() uint64 {
	return uint64(quicvarint.Len(uint64(v)))
}

type versions []Version

func (v versions) String() string {
	res := "["
	for i, e := range v {
		if i < len(v)-1 {
			res += fmt.Sprintf("%v, ", e)
		} else {
			res += fmt.Sprintf("%v", e)
		}
	}
	res += "]"
	return res
}

func (v versions) Len() uint64 {
	l := uint64(0)
	for _, x := range v {
		l = l + x.Len()
	}
	return l
}

func (v versions) append(buf []byte) []byte {
	buf = quicvarint.Append(buf, uint64(len(v)))
	for _, vv := range v {
		buf = quicvarint.Append(buf, uint64(vv))
	}
	return buf
}

func (vs *versions) parse(data []byte) (int, error) {
	numVersions, parsed, err := quicvarint.Parse(data)
	if err != nil {
		return parsed, err
	}
	data = data[parsed:]

	for i := 0; i < int(numVersions); i++ {
		v, n, err := quicvarint.Parse(data)
		parsed += n
		if err != nil {
			return parsed, err
		}
		data = data[n:]
		*vs = append(*vs, Version(v))
	}
	return parsed, nil
}
