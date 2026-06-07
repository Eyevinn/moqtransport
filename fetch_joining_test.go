package moqtransport

import (
	"testing"

	"github.com/Eyevinn/moqtransport/internal/wire"
	"github.com/stretchr/testify/assert"
)

func TestResolveJoiningFetch(t *testing.T) {
	newSub := func(largest Location) *localTrack {
		return &localTrack{
			isSubscription:  true,
			namespace:       []string{"ns", "a"},
			trackName:       "catalog",
			filterType:      wire.FilterTypeLatestObject,
			largestLocation: &largest,
		}
	}

	t.Run("relative_offset_zero", func(t *testing.T) {
		ns, track, start, end, err := resolveJoiningFetch(newSub(Location{Group: 5, Object: 3}), wire.FetchTypeRelativeJoining, 0)
		assert.NoError(t, err)
		assert.Equal(t, []string{"ns", "a"}, ns)
		assert.Equal(t, "catalog", track)
		assert.Equal(t, Location{Group: 5, Object: 0}, start)
		assert.Equal(t, Location{Group: 5, Object: 4}, end) // largest.Object + 1
	})

	t.Run("relative_offset_n", func(t *testing.T) {
		_, _, start, end, err := resolveJoiningFetch(newSub(Location{Group: 5, Object: 3}), wire.FetchTypeRelativeJoining, 2)
		assert.NoError(t, err)
		assert.Equal(t, Location{Group: 3, Object: 0}, start)
		assert.Equal(t, Location{Group: 5, Object: 4}, end)
	})

	t.Run("relative_underflow_clamps_to_group_zero", func(t *testing.T) {
		_, _, start, end, err := resolveJoiningFetch(newSub(Location{Group: 2, Object: 3}), wire.FetchTypeRelativeJoining, 5)
		assert.NoError(t, err)
		assert.Equal(t, Location{Group: 0, Object: 0}, start)
		assert.Equal(t, Location{Group: 2, Object: 4}, end)
	})

	t.Run("absolute", func(t *testing.T) {
		_, _, start, end, err := resolveJoiningFetch(newSub(Location{Group: 5, Object: 3}), wire.FetchTypeAbsoluteJoining, 3)
		assert.NoError(t, err)
		assert.Equal(t, Location{Group: 3, Object: 0}, start)
		assert.Equal(t, Location{Group: 5, Object: 4}, end)
	})

	t.Run("absolute_start_after_largest_is_invalid", func(t *testing.T) {
		_, _, _, _, err := resolveJoiningFetch(newSub(Location{Group: 5, Object: 3}), wire.FetchTypeAbsoluteJoining, 7)
		assert.ErrorIs(t, err, errInvalidJoiningFetchRange)
	})

	t.Run("static_catalog_offset_zero", func(t *testing.T) {
		// A common MSF case: a single full (static) catalog at {0,0}.
		_, _, start, end, err := resolveJoiningFetch(newSub(Location{Group: 0, Object: 0}), wire.FetchTypeRelativeJoining, 0)
		assert.NoError(t, err)
		assert.Equal(t, Location{Group: 0, Object: 0}, start)
		assert.Equal(t, Location{Group: 0, Object: 1}, end)
	})
}
