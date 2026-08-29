package moqtransport

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// The two registries share names but not values, which is the whole reason
// they are separate types. If they ever converge, the split can go; until then
// a single set of constants would silently send GOING_AWAY as the wrong code.
func TestErrorRegistriesDoNotShareValues(t *testing.T) {
	// The same name, different values.
	assert.NotEqual(t, uint64(RequestErrorGoingAway), uint64(PublishDoneGoingAway))
	// And the same value, different names: 0x2 is TIMEOUT in one registry and
	// TRACK_ENDED in the other, so a mixed-up constant would be sent happily
	// and read as something else entirely.
	assert.Equal(t, uint64(RequestErrorTimeout), uint64(PublishDoneTrackEnded))

	// The names that do line up are worth pinning too, so a future edit that
	// "tidies" one registry to match the other fails here rather than on the
	// wire.
	assert.Equal(t, uint64(0x6), uint64(RequestErrorGoingAway))
	assert.Equal(t, uint64(0x4), uint64(PublishDoneGoingAway))
}

// An unrecognized code has to be reportable. A grease value is called out
// separately because it means the peer is exercising our unknown-value path on
// purpose, which is not the same signal as a code we failed to implement.
func TestErrorCodeStrings(t *testing.T) {
	assert.Equal(t, "DOES_NOT_EXIST", RequestErrorDoesNotExist.String())
	assert.Equal(t, "SUBSCRIPTION_ENDED", PublishDoneSubscriptionEnded.String())

	assert.Contains(t, RequestErrorCode(0x99).String(), "unknown request error code")
	assert.Contains(t, PublishDoneCode(0x99).String(), "unknown publish done code")

	// 0x7f*N + 0x9D for N = 0, 1, 2.
	for _, grease := range []uint64{0x9D, 0x11C, 0x19B} {
		assert.Contains(t, RequestErrorCode(grease).String(), "grease", "%#x", grease)
		assert.Contains(t, PublishDoneCode(grease).String(), "grease", "%#x", grease)
	}
}
