package wire2

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestSetupOptionsAreASeparateNamespace pins the collision that makes the
// three registries genuinely distinct rather than one table.
func TestSetupOptionsAreASeparateNamespace(t *testing.T) {
	assert.Equal(t, "MAX_AUTH_TOKEN_CACHE_SIZE", SetupOptionName(0x04))
	assert.Equal(t, "RENDEZVOUS_TIMEOUT", ParameterName(0x04))
	assert.Equal(t, "MAX_CACHE_DURATION", PropertyName(0x04))
}

func TestGreaseSetupOptions(t *testing.T) {
	// 0x7f * N + 0x9D for N = 0, 1, 2.
	for _, typ := range []uint64{0x9D, 0x9D + 0x7F, 0x9D + 2*0x7F} {
		assert.True(t, IsGreaseSetupOption(typ), "%#x should be grease", typ)
		assert.Contains(t, SetupOptionName(typ), "GREASE")
	}
	for _, typ := range []uint64{0x01, 0x07, 0x9C, 0x9E} {
		assert.False(t, IsGreaseSetupOption(typ), "%#x should not be grease", typ)
	}
}

func TestKnownSetupOption(t *testing.T) {
	assert.True(t, KnownSetupOption(SetupOptionPath))
	assert.True(t, KnownSetupOption(SetupOptionMOQTImplementation))
	// Unknown is not an error for Setup Options; the receiver ignores it.
	assert.False(t, KnownSetupOption(0x99))
	assert.Contains(t, SetupOptionName(0x99), "UNKNOWN")
}

func TestSetupOptionParityMatchesTheirValues(t *testing.T) {
	// Setup Options really are Key-Value-Pairs, so the registry must not
	// contradict the parity rule: PATH, AUTHORIZATION_TOKEN, AUTHORITY and
	// MOQT_IMPLEMENTATION are byte strings at odd types, and
	// MAX_AUTH_TOKEN_CACHE_SIZE is a number at an even one.
	for _, typ := range []uint64{SetupOptionPath, SetupOptionAuthorizationToken, SetupOptionAuthority, SetupOptionMOQTImplementation} {
		assert.Equal(t, uint64(1), typ%2, "%s should be odd", SetupOptionName(typ))
	}
	assert.Equal(t, uint64(0), SetupOptionMaxAuthTokenCacheSize%2)
}

func TestMandatoryTrackPropertyRange(t *testing.T) {
	for _, typ := range []uint64{0x4000, 0x5555, 0x7FFF} {
		assert.True(t, IsMandatoryTrackProperty(typ), "%#x", typ)
	}
	for _, typ := range []uint64{0x3FFF, 0x8000, 0x02} {
		assert.False(t, IsMandatoryTrackProperty(typ), "%#x", typ)
	}
}

func TestApplicationPropertyRanges(t *testing.T) {
	for _, typ := range []uint64{0x38, 0x3F, 0x3800, 0x3FFF} {
		assert.True(t, IsApplicationProperty(typ), "%#x", typ)
	}
	for _, typ := range []uint64{0x37, 0x40, 0x37FF, 0x4000} {
		assert.False(t, IsApplicationProperty(typ), "%#x", typ)
	}
}

func TestPropertyScopes(t *testing.T) {
	scope, ok := PropertyScopeOf(PropertyMaxCacheDuration)
	require.True(t, ok)
	assert.Equal(t, PropertyScopeTrack, scope)

	scope, ok = PropertyScopeOf(PropertyPriorGroupIDGap)
	require.True(t, ok)
	assert.Equal(t, PropertyScopeObject, scope)

	scope, ok = PropertyScopeOf(PropertyImmutableProperties)
	require.True(t, ok)
	assert.Equal(t, PropertyScopeTrack|PropertyScopeObject, scope)

	_, ok = PropertyScopeOf(0x99)
	assert.False(t, ok)
}

func TestValidateObjectProperties(t *testing.T) {
	t.Run("a mandatory track property makes the track malformed", func(t *testing.T) {
		err := ValidateObjectProperties(KVPList{{Type: 0x4002, ValueVarInt: 1}})
		assert.ErrorContains(t, err, "not allowed in object scope")
	})

	t.Run("a track-scoped property is rejected", func(t *testing.T) {
		err := ValidateObjectProperties(KVPList{{Type: PropertyMaxCacheDuration, ValueVarInt: 1}})
		assert.ErrorContains(t, err, "not allowed in object scope")
	})

	t.Run("object-scoped and dual-scoped properties pass", func(t *testing.T) {
		assert.NoError(t, ValidateObjectProperties(KVPList{
			{Type: PropertyPriorGroupIDGap, ValueVarInt: 1},
			{Type: PropertyImmutableProperties, ValueVarInt: 1},
		}))
	})

	t.Run("an unknown property is forwarded, not rejected", func(t *testing.T) {
		assert.NoError(t, ValidateObjectProperties(KVPList{{Type: 0x3A, ValueVarInt: 1}}))
	})
}

func TestValidateTrackProperties(t *testing.T) {
	assert.NoError(t, ValidateTrackProperties(KVPList{
		{Type: PropertyMaxCacheDuration, ValueVarInt: 1},
		{Type: 0x4002, ValueVarInt: 1},
	}))
	err := ValidateTrackProperties(KVPList{{Type: PropertyPriorObjectIDGap, ValueVarInt: 1}})
	assert.ErrorContains(t, err, "not allowed in track scope")
}

func TestUnsupportedMandatoryProperties(t *testing.T) {
	unsupported := UnsupportedMandatoryProperties(KVPList{
		{Type: PropertyMaxCacheDuration, ValueVarInt: 1},
		{Type: 0x4002, ValueVarInt: 1},
		{Type: 0x7FFF, ValueVarInt: 1},
	})
	assert.Equal(t, []uint64{0x4002, 0x7FFF}, unsupported)

	assert.Empty(t, UnsupportedMandatoryProperties(KVPList{{Type: PropertyDynamicGroups, ValueVarInt: 1}}))
}
