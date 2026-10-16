package wire2

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestLocationFilterWireV22 pins draft-22's encoding of each Location Filter
// Type: no Length, the type alone selecting the fields that follow (Section
// 9.20.9, and the reading WG issue #1991 converged on).
func TestLocationFilterWireV22(t *testing.T) {
	for _, tc := range []struct {
		name   string
		filter LocationFilter
		wire   []byte
	}{
		{"none", LocationFilter{}, []byte{0x00}},
		{"relative start", LocationFilter{Type: LocationFilterRelativeStart, Start: Location{Group: 1}}, []byte{0x01, 0x01}},
		{"absolute start", LocationFilter{Type: LocationFilterAbsoluteStart, Start: Location{Group: 3, Object: 4}}, []byte{0x02, 0x03, 0x04}},
		{"absolute group end", LocationFilter{Type: LocationFilterAbsoluteGroupEnd, Start: Location{Group: 3, Object: 4}, EndGroupDelta: 2}, []byte{0x03, 0x03, 0x04, 0x02}},
		{"absolute range", LocationFilter{Type: LocationFilterAbsoluteRange, Start: Location{Group: 3, Object: 4}, EndGroupDelta: 2, EndObject: 7}, []byte{0x04, 0x03, 0x04, 0x02, 0x07}},
		{"next object", LocationFilter{Type: LocationFilterNextObject}, []byte{0x05}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			buf, err := tc.filter.appendValue(nil, Version22)
			require.NoError(t, err)
			assert.Equal(t, tc.wire, buf)

			var got LocationFilter
			n, err := got.parseValue(append(buf, 0xFF), Version22)
			require.NoError(t, err)
			assert.Equal(t, len(tc.wire), n, "the type alone says where the value ends")
			assert.Equal(t, tc.filter, got)
		})
	}
}

// TestLocationFilterWireV18 pins draft-18's SUBSCRIPTION_FILTER: a Length,
// then Filter Type 1-4. Each draft-22 type with a draft-18 form maps onto
// one, and parsing maps back.
func TestLocationFilterWireV18(t *testing.T) {
	for _, tc := range []struct {
		name   string
		filter LocationFilter
		wire   []byte
		back   LocationFilter // what parsing the wire gives, when not filter
	}{
		{"next group start", LocationFilter{Type: LocationFilterRelativeStart}, []byte{0x01, 0x01}, LocationFilter{}},
		{"largest object", LocationFilter{Type: LocationFilterNextObject}, []byte{0x01, 0x02}, LocationFilter{}},
		{"absolute start", LocationFilter{Type: LocationFilterAbsoluteStart, Start: Location{Group: 3, Object: 4}}, []byte{0x03, 0x03, 0x03, 0x04}, LocationFilter{}},
		{"absolute range", LocationFilter{Type: LocationFilterAbsoluteGroupEnd, Start: Location{Group: 3, Object: 4}, EndGroupDelta: 2}, []byte{0x04, 0x04, 0x03, 0x04, 0x02}, LocationFilter{}},
		{"none becomes absolute start at zero", LocationFilter{}, []byte{0x03, 0x03, 0x00, 0x00},
			LocationFilter{Type: LocationFilterAbsoluteStart}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			buf, err := tc.filter.appendValue(nil, Version18)
			require.NoError(t, err)
			assert.Equal(t, tc.wire, buf)

			var got LocationFilter
			n, err := got.parseValue(append(buf, 0xFF), Version18)
			require.NoError(t, err)
			assert.Equal(t, len(tc.wire), n)
			want := tc.filter
			if tc.back != (LocationFilter{}) {
				want = tc.back
			}
			assert.Equal(t, want, got)
		})
	}
}

// Draft-18 cannot say "start N Groups back" or "end inside a Group". Sending
// either as something else would deliver what was not asked for.
func TestLocationFilterNotInDraft18(t *testing.T) {
	for _, f := range []LocationFilter{
		{Type: LocationFilterRelativeStart, Start: Location{Group: 1}},
		{Type: LocationFilterAbsoluteRange, Start: Location{Group: 1}, EndObject: 3},
	} {
		_, err := f.appendValue(nil, Version18)
		assert.ErrorIs(t, err, errFilterNotInDraft18, "%v", f)
		_, err = f.appendValue(nil, Version22)
		assert.NoError(t, err, "%v", f)
	}
}

func TestLocationFilterInvalid(t *testing.T) {
	var f LocationFilter

	_, err := f.parseValue([]byte{0x06}, Version22)
	assert.ErrorIs(t, err, errInvalidFilterType, "draft-22 types end at 0x05")
	_, err = f.parseValue([]byte{0x01, 0x05}, Version18)
	assert.ErrorIs(t, err, errInvalidFilterType, "draft-18 types end at 0x4")
	_, err = f.parseValue([]byte{0x01, 0x00}, Version18)
	assert.ErrorIs(t, err, errInvalidFilterType, "draft-18 has no type 0")

	// Draft-18's Length bounds the value exactly.
	_, err = f.parseValue([]byte{0x02, 0x01, 0x00}, Version18)
	assert.ErrorIs(t, err, errTrailingBytes)

	// A Start Group plus End Group Delta past 2^64-1, in either draft.
	overflow := []byte{0x03}
	overflow = append(overflow, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF) // StartGroup 2^64-1
	overflow = append(overflow, 0x00, 0x01)                                          // StartObject, EndGroupDelta
	_, err = f.parseValue(overflow, Version22)
	assert.ErrorIs(t, err, errEndGroupOverflow)

	_, err = LocationFilter{Type: LocationFilterAbsoluteGroupEnd, Start: Location{Group: maxUint64}, EndGroupDelta: 1}.appendValue(nil, Version22)
	assert.ErrorIs(t, err, errEndGroupOverflow)
	_, err = LocationFilter{Type: 9}.appendValue(nil, Version22)
	assert.ErrorIs(t, err, errInvalidFilterType)

	for _, truncated := range [][]byte{{}, {0x01}, {0x02, 0x03}, {0x03, 0x03, 0x04}, {0x04, 0x03, 0x04, 0x02}} {
		_, err := f.parseValue(truncated, Version22)
		assert.Error(t, err, "%x", truncated)
	}
}

func TestLocationFilterEnd(t *testing.T) {
	_, ok := LocationFilter{Type: LocationFilterAbsoluteStart}.End()
	assert.False(t, ok)

	end, ok := LocationFilter{Type: LocationFilterAbsoluteGroupEnd, Start: Location{Group: 3}, EndGroupDelta: 2}.End()
	require.True(t, ok)
	assert.Equal(t, Location{Group: 5, Object: MaxObjectID}, end, "a Group end covers the whole Group")

	end, ok = LocationFilter{Type: LocationFilterAbsoluteRange, Start: Location{Group: 3}, EndGroupDelta: 2, EndObject: 9}.End()
	require.True(t, ok)
	assert.Equal(t, Location{Group: 5, Object: 9}, end)
}

func TestLocationCompare(t *testing.T) {
	assert.Equal(t, -1, Location{Group: 1, Object: 9}.Compare(Location{Group: 2}))
	assert.Equal(t, -1, Location{Group: 2, Object: 1}.Compare(Location{Group: 2, Object: 2}))
	assert.Equal(t, 0, Location{Group: 2, Object: 2}.Compare(Location{Group: 2, Object: 2}))
	assert.Equal(t, 1, Location{Group: 3}.Compare(Location{Group: 2, Object: 9}))
}

// TestSubscriptionRange resolves each type against a Largest Object of
// {10, 4}: the Next Object is {10, 5} and the Next Group {11, 0}.
func TestSubscriptionRange(t *testing.T) {
	largest := Location{Group: 10, Object: 4}
	for _, tc := range []struct {
		name   string
		filter LocationFilter
		want   LocationRange
	}{
		{"none", LocationFilter{}, LocationRange{Open: true}},
		{"next group", LocationFilter{Type: LocationFilterRelativeStart}, LocationRange{Start: Location{Group: 11}, Open: true}},
		{"current group", LocationFilter{Type: LocationFilterRelativeStart, Start: Location{Group: 1}}, LocationRange{Start: Location{Group: 10}, Open: true}},
		{"three groups back", LocationFilter{Type: LocationFilterRelativeStart, Start: Location{Group: 3}}, LocationRange{Start: Location{Group: 8}, Open: true}},
		{"clamped at zero", LocationFilter{Type: LocationFilterRelativeStart, Start: Location{Group: 99}}, LocationRange{Open: true}},
		{"next object", LocationFilter{Type: LocationFilterNextObject}, LocationRange{Start: Location{Group: 10, Object: 5}, Open: true}},
		{"absolute start", LocationFilter{Type: LocationFilterAbsoluteStart, Start: Location{Group: 2, Object: 1}}, LocationRange{Start: Location{Group: 2, Object: 1}, Open: true}},
		{"ends before largest", LocationFilter{Type: LocationFilterAbsoluteGroupEnd, Start: Location{Group: 2}, EndGroupDelta: 1},
			LocationRange{Start: Location{Group: 2}, End: Location{Group: 3, Object: MaxObjectID}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, tc.filter.SubscriptionRange(largest, true))
		})
	}

	// Nothing published yet: the Next Object is {0, 0}, and so is anywhere
	// relative to the Next Group.
	assert.Equal(t, LocationRange{Open: true}, LocationFilter{Type: LocationFilterNextObject}.SubscriptionRange(Location{}, false))
	assert.Equal(t, LocationRange{Open: true}, LocationFilter{Type: LocationFilterRelativeStart, Start: Location{Group: 1}}.SubscriptionRange(Location{}, false))
}

// The relative start is clamped to [0, 2^64-1] rather than wrapping
// (draft-22 Section 9.20.9).
func TestRelativeStartClampsAtTheTop(t *testing.T) {
	largest := Location{Group: maxUint64}
	assert.Equal(t, uint64(maxUint64), relativeStartGroup(largest, true, 0))
	assert.Equal(t, uint64(maxUint64), relativeStartGroup(largest, true, 1))
	assert.Equal(t, uint64(maxUint64-1), relativeStartGroup(largest, true, 2))
	assert.Equal(t, Location{Group: maxUint64, Object: MaxObjectID}, nextObject(Location{Group: maxUint64, Object: MaxObjectID}, true))
	assert.Equal(t, Location{Group: 4}, nextObject(Location{Group: 3, Object: MaxObjectID}, true))
}

// TestFetchRange resolves the filter the way a FETCH or a fill does: an open
// or too-distant end stops at Largest Object, and an empty range is reported
// rather than resolved.
func TestFetchRange(t *testing.T) {
	largest := Location{Group: 10, Object: 4}
	for _, tc := range []struct {
		name   string
		filter LocationFilter
		want   LocationRange
		ok     bool
	}{
		{"whole track", LocationFilter{}, LocationRange{End: largest}, true},
		{"current group", LocationFilter{Type: LocationFilterRelativeStart, Start: Location{Group: 1}}, LocationRange{Start: Location{Group: 10}, End: largest}, true},
		{"next group is empty", LocationFilter{Type: LocationFilterRelativeStart}, LocationRange{}, false},
		{"next object is empty", LocationFilter{Type: LocationFilterNextObject}, LocationRange{}, false},
		{"absolute start", LocationFilter{Type: LocationFilterAbsoluteStart, Start: Location{Group: 4}}, LocationRange{Start: Location{Group: 4}, End: largest}, true},
		{"end beyond largest", LocationFilter{Type: LocationFilterAbsoluteGroupEnd, Start: Location{Group: 9}, EndGroupDelta: 5},
			LocationRange{Start: Location{Group: 9}, End: largest}, true},
		{"end before largest", LocationFilter{Type: LocationFilterAbsoluteRange, Start: Location{Group: 2}, EndGroupDelta: 1, EndObject: 7},
			LocationRange{Start: Location{Group: 2}, End: Location{Group: 3, Object: 7}}, true},
		{"start after largest", LocationFilter{Type: LocationFilterAbsoluteStart, Start: Location{Group: 10, Object: 5}}, LocationRange{}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := tc.filter.FetchRange(largest, true)
			assert.Equal(t, tc.ok, ok)
			assert.Equal(t, tc.want, got)
		})
	}

	_, ok := LocationFilter{}.FetchRange(Location{}, false)
	assert.False(t, ok, "nothing published means nothing to fetch")
}

func TestLocationRangeContains(t *testing.T) {
	r := LocationRange{Start: Location{Group: 2, Object: 3}, End: Location{Group: 4, Object: 1}}
	assert.False(t, r.Contains(Location{Group: 2, Object: 2}))
	assert.True(t, r.Contains(Location{Group: 2, Object: 3}))
	assert.True(t, r.Contains(Location{Group: 3, Object: 99}))
	assert.True(t, r.Contains(Location{Group: 4, Object: 1}))
	assert.False(t, r.Contains(Location{Group: 4, Object: 2}))

	r.Open = true
	assert.True(t, r.Contains(Location{Group: 400}))
}

// Draft-18 FETCH carries an explicit range whose End is the last Object plus
// one, with Object 0 meaning the whole Group. The conversions in both
// directions have to agree with that.
func TestDraft18FetchRanges(t *testing.T) {
	f, err := FetchFilterFromV18(Location{Group: 1, Object: 2}, Location{Group: 5})
	require.NoError(t, err)
	assert.Equal(t, LocationFilter{Type: LocationFilterAbsoluteGroupEnd, Start: Location{Group: 1, Object: 2}, EndGroupDelta: 4}, f)

	f, err = FetchFilterFromV18(Location{Group: 1, Object: 2}, Location{Group: 5, Object: 3})
	require.NoError(t, err)
	assert.Equal(t, LocationFilter{Type: LocationFilterAbsoluteRange, Start: Location{Group: 1, Object: 2}, EndGroupDelta: 4, EndObject: 2}, f)

	_, err = FetchFilterFromV18(Location{Group: 5}, Location{Group: 4})
	assert.ErrorIs(t, err, errFetchEndBeforeStart)
	_, err = FetchFilterFromV18(Location{Group: 5, Object: 4}, Location{Group: 5, Object: 3})
	assert.ErrorIs(t, err, errFetchEndBeforeStart)

	start, end, err := V18FetchRange(LocationFilter{Type: LocationFilterAbsoluteRange, Start: Location{Group: 1}, EndGroupDelta: 2, EndObject: 6})
	require.NoError(t, err)
	assert.Equal(t, Location{Group: 1}, start)
	assert.Equal(t, Location{Group: 3, Object: 7}, end)

	start, end, err = V18FetchRange(LocationFilter{Type: LocationFilterAbsoluteGroupEnd, Start: Location{Group: 1}, EndGroupDelta: 2})
	require.NoError(t, err)
	assert.Equal(t, Location{Group: 1}, start)
	assert.Equal(t, Location{Group: 3}, end, "a whole Group is Object 0")

	start, end, err = V18FetchRange(LocationFilter{})
	require.NoError(t, err)
	assert.Equal(t, Location{}, start)
	assert.Equal(t, Location{Group: maxUint64}, end, "open ended reaches the last possible Group")

	_, _, err = V18FetchRange(LocationFilter{Type: LocationFilterRelativeStart, Start: Location{Group: 1}})
	assert.ErrorIs(t, err, errFilterNotInDraft18)
	_, _, err = V18FetchRange(LocationFilter{Type: LocationFilterNextObject})
	assert.ErrorIs(t, err, errFilterNotInDraft18)

	assert.Equal(t, Location{Group: 4, Object: 8}, V18EndLocation(Location{Group: 4, Object: 7}))
	assert.Equal(t, Location{Group: 4}, V18EndLocation(Location{Group: 4, Object: MaxObjectID}))
	assert.Equal(t, Location{Group: 4, Object: 7}, InclusiveFromV18End(Location{Group: 4, Object: 8}))
	assert.Equal(t, Location{Group: 4, Object: MaxObjectID}, InclusiveFromV18End(Location{Group: 4}))
}
