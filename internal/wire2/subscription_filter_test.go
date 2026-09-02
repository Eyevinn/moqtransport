package wire2

import (
	"math"
	"testing"
	"time"

	"github.com/Eyevinn/locmaf/vi64"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The Filter Type is a tag, not a hint: it decides which of the optional
// fields follow, so the round trip is the test that they agree.
func TestSubscriptionFilterRoundTrip(t *testing.T) {
	for _, f := range []SubscriptionFilter{
		{Type: FilterNextGroupStart},
		{Type: FilterLargestObject},
		{Type: FilterAbsoluteStart, StartLocation: Location{Group: 4, Object: 9}},
		{Type: FilterAbsoluteRange, StartLocation: Location{Group: 4, Object: 0}, EndGroupDelta: 0},
		{Type: FilterAbsoluteRange, StartLocation: Location{Group: 4, Object: 0}, EndGroupDelta: 10},
	} {
		t.Run(f.String(), func(t *testing.T) {
			param, err := SubscriptionFilterParameter(f)
			require.NoError(t, err)

			got, present, err := Parameters{param}.Filter()
			require.NoError(t, err)
			assert.True(t, present)
			assert.Equal(t, f, got)
		})
	}
}

func TestSubscriptionFilterEndGroup(t *testing.T) {
	// Zero delta means the remainder of the start group.
	end, ok := SubscriptionFilter{
		Type: FilterAbsoluteRange, StartLocation: Location{Group: 4, Object: 3},
	}.EndGroup()
	assert.True(t, ok)
	assert.Equal(t, uint64(4), end)

	end, ok = SubscriptionFilter{
		Type: FilterAbsoluteRange, StartLocation: Location{Group: 4}, EndGroupDelta: 6,
	}.EndGroup()
	assert.True(t, ok)
	assert.Equal(t, uint64(10), end)

	// The open-ended filters have no end at all.
	_, ok = SubscriptionFilter{Type: FilterLargestObject}.EndGroup()
	assert.False(t, ok)
}

func TestSubscriptionFilterInvalid(t *testing.T) {
	_, err := SubscriptionFilterParameter(SubscriptionFilter{Type: 0x5})
	assert.ErrorIs(t, err, errInvalidFilterType)

	_, _, err = Parameters{BytesParameter(ParamSubscriptionFilter, vi64.Append(nil, 0x5))}.Filter()
	assert.ErrorIs(t, err, errInvalidFilterType)

	// An End Group past 2^64-1 is a PROTOCOL_VIOLATION, refused on both sides.
	overflow := SubscriptionFilter{
		Type:          FilterAbsoluteRange,
		StartLocation: Location{Group: maxUint64 - 1},
		EndGroupDelta: 5,
	}
	_, err = SubscriptionFilterParameter(overflow)
	assert.ErrorIs(t, err, errEndGroupOverflow)

	raw := vi64.Append(nil, uint64(FilterAbsoluteRange))
	raw = vi64.Append(raw, maxUint64-1)
	raw = vi64.Append(raw, 0)
	raw = vi64.Append(raw, 5)
	_, _, err = Parameters{BytesParameter(ParamSubscriptionFilter, raw)}.Filter()
	assert.ErrorIs(t, err, errEndGroupOverflow)

	// Bytes after the filter mean the two ends disagree about its shape.
	trailing := vi64.Append(nil, uint64(FilterLargestObject))
	_, _, err = Parameters{BytesParameter(ParamSubscriptionFilter, append(trailing, 0x00))}.Filter()
	assert.ErrorIs(t, err, errTrailingBytes)
}

// The defaults are what a parameter block means when it says nothing, and
// getting one wrong is silent: the subscription just behaves differently from
// what the peer asked for.
func TestParameterDefaults(t *testing.T) {
	empty := Parameters{}

	assert.Equal(t, uint8(128), empty.SubscriberPriority())

	forward, err := empty.Forward()
	require.NoError(t, err)
	assert.True(t, forward)

	_, present, err := empty.Filter()
	require.NoError(t, err)
	assert.False(t, present, "absent means unfiltered on a SUBSCRIBE and unchanged on an update")

	_, present, err = empty.GroupOrder()
	require.NoError(t, err)
	assert.False(t, present, "the default differs between SUBSCRIBE and FETCH, so it is the caller's")

	_, present = empty.LargestObject()
	assert.False(t, present)

	wait, present := empty.RendezvousTimeout()
	assert.False(t, present, "absent means the subscriber wants an immediate answer")
	assert.Zero(t, wait)
}

func TestRendezvousTimeoutParameter(t *testing.T) {
	wait, present := Parameters{VarintParameter(ParamRendezvousTimeout, 5000)}.RendezvousTimeout()
	assert.True(t, present)
	assert.Equal(t, 5*time.Second, wait)

	// A value past what a Duration can hold saturates rather than wrapping.
	wait, present = Parameters{VarintParameter(ParamRendezvousTimeout, 1<<62-1)}.RendezvousTimeout()
	assert.True(t, present)
	assert.Equal(t, time.Duration(math.MaxInt64), wait)
}

func TestParameterAccessors(t *testing.T) {
	pp := Parameters{
		Uint8Parameter(ParamSubscriberPriority, 7),
		Uint8Parameter(ParamForward, 0),
		Uint8Parameter(ParamGroupOrder, uint8(GroupOrderDescending)),
		LocationParameter(ParamLargestObject, Location{Group: 3, Object: 4}),
	}

	assert.Equal(t, uint8(7), pp.SubscriberPriority())

	forward, err := pp.Forward()
	require.NoError(t, err)
	assert.False(t, forward)

	order, present, err := pp.GroupOrder()
	require.NoError(t, err)
	assert.True(t, present)
	assert.Equal(t, GroupOrderDescending, order)

	largest, present := pp.LargestObject()
	assert.True(t, present)
	assert.Equal(t, Location{Group: 3, Object: 4}, largest)

	_, _, err = Parameters{Uint8Parameter(ParamGroupOrder, 0)}.GroupOrder()
	assert.ErrorIs(t, err, errInvalidGroupOrder)

	_, err = Parameters{Uint8Parameter(ParamForward, 2)}.Forward()
	assert.ErrorIs(t, err, errInvalidBoolValue)
}
