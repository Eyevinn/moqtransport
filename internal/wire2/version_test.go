package wire2

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestVersionALPN(t *testing.T) {
	assert.Equal(t, "moqt-18", Version18.ALPN())
	assert.Equal(t, "moqt-18", Version18.String())
}

func TestSupportedALPNs(t *testing.T) {
	// Order is the offer order, most preferred first.
	assert.Equal(t, []string{"moqt-18"}, SupportedALPNs())
}

func TestVersionFromALPN(t *testing.T) {
	v, ok := VersionFromALPN("moqt-18")
	require.True(t, ok)
	assert.Equal(t, Version18, v)

	// Drafts we deliberately do not speak, and nonsense, are both rejected:
	// with no version field in SETUP there is no in-band way to recover.
	for _, alpn := range []string{"moqt-16", "moqt-20", "moq-00", "h3", ""} {
		_, ok := VersionFromALPN(alpn)
		assert.False(t, ok, "%q should not resolve", alpn)
	}
}

func TestSetupOptionAccessors(t *testing.T) {
	setup := &Setup{Options: KVPList{
		PathOption("/live?x=1"),
		AuthorityOption("relay.example"),
		MaxAuthTokenCacheSizeOption(4096),
		ImplementationOption("Eyevinn/moqtransport"),
	}}

	path, ok := setup.Path()
	require.True(t, ok)
	assert.Equal(t, "/live?x=1", path)

	authority, ok := setup.Authority()
	require.True(t, ok)
	assert.Equal(t, "relay.example", authority)

	impl, ok := setup.Implementation()
	require.True(t, ok)
	assert.Equal(t, "Eyevinn/moqtransport", impl)

	assert.Equal(t, uint64(4096), setup.MaxAuthTokenCacheSize())
}

func TestSetupOptionsRoundTrip(t *testing.T) {
	setup := &Setup{Options: KVPList{
		PathOption("/live"),
		MaxAuthTokenCacheSizeOption(1024),
		ImplementationOption("impl/1.0"),
	}}
	buf, err := AppendControlMessage(nil, setup)
	require.NoError(t, err)

	got, err := NewControlMessageParser(bytes.NewReader(buf), ScopeControl).Parse()
	require.NoError(t, err)
	assert.Equal(t, setup, got)
}

// TestMaxAuthTokenCacheSizeDefault pins the draft's default: an absent option
// means 0, which prohibits token aliases rather than allowing unlimited ones.
func TestMaxAuthTokenCacheSizeDefault(t *testing.T) {
	assert.Equal(t, uint64(0), (&Setup{Options: KVPList{}}).MaxAuthTokenCacheSize())
}

func TestValidateSetup(t *testing.T) {
	withPath := &Setup{Options: KVPList{PathOption("/live")}}
	withAuthority := &Setup{Options: KVPList{AuthorityOption("relay.example")}}
	plain := &Setup{Options: KVPList{ImplementationOption("impl/1.0")}}

	t.Run("a client on native QUIC may send PATH and AUTHORITY", func(t *testing.T) {
		assert.NoError(t, ValidateSetup(withPath, true, false))
		assert.NoError(t, ValidateSetup(withAuthority, true, false))
	})

	t.Run("a server may not", func(t *testing.T) {
		assert.ErrorContains(t, ValidateSetup(withPath, false, false), "sent by a server")
		assert.ErrorContains(t, ValidateSetup(withAuthority, false, false), "sent by a server")
	})

	t.Run("nobody may over WebTransport", func(t *testing.T) {
		assert.ErrorContains(t, ValidateSetup(withPath, true, true), "sent over WebTransport")
		assert.ErrorContains(t, ValidateSetup(withAuthority, true, true), "sent over WebTransport")
	})

	t.Run("other options are unrestricted", func(t *testing.T) {
		assert.NoError(t, ValidateSetup(plain, false, true))
	})

	t.Run("unknown options are never rejected", func(t *testing.T) {
		unknown := &Setup{Options: KVPList{{Type: 0x99, ValueBytes: []byte("x")}}}
		assert.NoError(t, ValidateSetup(unknown, false, true))
	})
}
