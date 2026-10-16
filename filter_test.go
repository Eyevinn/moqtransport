package moqtransport

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestFilterConstructors(t *testing.T) {
	assert.Equal(t, LocationFilter{Type: LocationFilterRelativeStart}, NextGroup())
	assert.Equal(t, LocationFilter{Type: LocationFilterRelativeStart, Start: Location{Group: 1}}, RelativeStart(1))
	assert.Equal(t, LocationFilter{Type: LocationFilterNextObject}, NextObject())
	assert.Equal(t, LocationFilter{Type: LocationFilterAbsoluteStart, Start: Location{Group: 2, Object: 3}},
		AbsoluteStart(Location{Group: 2, Object: 3}))

	// An end Object of MaxObjectID is the whole Group.
	assert.Equal(t, LocationFilter{Type: LocationFilterAbsoluteGroupEnd, Start: Location{Group: 2}, EndGroupDelta: 3},
		AbsoluteRange(Location{Group: 2}, Location{Group: 5, Object: MaxObjectID}))
	assert.Equal(t, LocationFilter{Type: LocationFilterAbsoluteRange, Start: Location{Group: 2}, EndGroupDelta: 3, EndObject: 4},
		AbsoluteRange(Location{Group: 2}, Location{Group: 5, Object: 4}))

	end, ok := AbsoluteRange(Location{Group: 2}, Location{Group: 5, Object: 4}).End()
	assert.True(t, ok)
	assert.Equal(t, Location{Group: 5, Object: 4}, end)
}
