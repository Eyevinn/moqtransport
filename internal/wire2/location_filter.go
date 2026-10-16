package wire2

import (
	"fmt"

	"github.com/Eyevinn/locmaf/vi64"
)

// LocationFilterType selects which Locations a location filter admits, and
// which of its fields are on the wire (draft-ietf-moq-transport-22, Section
// 9.20.9).
//
// The values are draft-22's. Draft-18's SUBSCRIPTION_FILTER uses the same
// parameter Type with a Filter Type of its own (1-4, length-prefixed); the
// codec maps between the two, so the rest of the library speaks only these.
type LocationFilterType uint64

const (
	// LocationFilterNone admits every Location. It is what an absent filter
	// means, and in a REQUEST_UPDATE it is the only way to remove a filter.
	LocationFilterNone LocationFilterType = 0x0
	// LocationFilterRelativeStart starts Start.Group Groups before the Next
	// Group -- {Largest Object.Group + 1 - Start.Group, 0} -- and is open
	// ended. 0 is the Next Group, 1 the current one.
	LocationFilterRelativeStart LocationFilterType = 0x1
	// LocationFilterAbsoluteStart starts at Start and is open ended.
	LocationFilterAbsoluteStart LocationFilterType = 0x2
	// LocationFilterAbsoluteGroupEnd starts at Start and ends with the last
	// Object of Group Start.Group + EndGroupDelta.
	LocationFilterAbsoluteGroupEnd LocationFilterType = 0x3
	// LocationFilterAbsoluteRange starts at Start and ends at {Start.Group +
	// EndGroupDelta, EndObject}, inclusive.
	LocationFilterAbsoluteRange LocationFilterType = 0x4
	// LocationFilterNextObject starts at the Next Object -- {Largest
	// Object.Group, Largest Object.Object + 1} -- and is open ended.
	LocationFilterNextObject LocationFilterType = 0x5
)

func (t LocationFilterType) String() string {
	switch t {
	case LocationFilterNone:
		return "None"
	case LocationFilterRelativeStart:
		return "RelativeStart"
	case LocationFilterAbsoluteStart:
		return "AbsoluteStart"
	case LocationFilterAbsoluteGroupEnd:
		return "AbsoluteGroupEnd"
	case LocationFilterAbsoluteRange:
		return "AbsoluteRange"
	case LocationFilterNextObject:
		return "NextObject"
	}
	return fmt.Sprintf("invalid location filter type: %#x", uint64(t))
}

func (t LocationFilterType) valid() bool {
	return t <= LocationFilterNextObject
}

// hasStart reports whether the type carries a Start Location on the wire.
// RelativeStart carries only Start.Group.
func (t LocationFilterType) hasStart() bool {
	return t == LocationFilterAbsoluteStart || t == LocationFilterAbsoluteGroupEnd ||
		t == LocationFilterAbsoluteRange
}

// hasEndGroup reports whether the type carries an EndGroupDelta.
func (t LocationFilterType) hasEndGroup() bool {
	return t == LocationFilterAbsoluteGroupEnd || t == LocationFilterAbsoluteRange
}

// Draft-18's SUBSCRIPTION_FILTER Filter Types (draft-18 Section 5.1.2). Each
// is one of draft-22's under another number: Next Group Start is a relative
// start of 0 Groups, Largest Object is the Next Object, and draft-18's
// Absolute Range ends with a whole Group, which draft-22 calls Absolute Start,
// Group End.
const (
	filterType18NextGroupStart uint64 = 0x1
	filterType18LargestObject  uint64 = 0x2
	filterType18AbsoluteStart  uint64 = 0x3
	filterType18AbsoluteRange  uint64 = 0x4
)

// LocationFilter is the value of the LOCATION_FILTER parameter (draft-22
// Section 9.20.9), or of draft-18's SUBSCRIPTION_FILTER. The zero value is
// LocationFilterNone.
type LocationFilter struct {
	Type LocationFilterType

	// Start is the first Location for the three absolute types. For
	// LocationFilterRelativeStart only Start.Group is used, and it counts
	// Groups back from the Next Group rather than naming one.
	Start Location

	// EndGroupDelta is the last Group's distance from Start.Group, for
	// AbsoluteGroupEnd and AbsoluteRange.
	EndGroupDelta uint64

	// EndObject is the last Object of the last Group, inclusive, for
	// AbsoluteRange.
	EndObject uint64
}

// EndGroup returns the last Group the filter admits, and whether it has one.
func (f LocationFilter) EndGroup() (uint64, bool) {
	if !f.Type.hasEndGroup() {
		return 0, false
	}
	return f.Start.Group + f.EndGroupDelta, true
}

// End returns the last Location the filter admits, inclusive, and whether it
// has one. For AbsoluteGroupEnd it is {EndGroup, MaxObjectID}: every Object of
// the last Group.
func (f LocationFilter) End() (Location, bool) {
	group, ok := f.EndGroup()
	if !ok {
		return Location{}, false
	}
	if f.Type == LocationFilterAbsoluteRange {
		return Location{Group: group, Object: f.EndObject}, true
	}
	return Location{Group: group, Object: MaxObjectID}, true
}

func (f LocationFilter) String() string {
	switch f.Type {
	case LocationFilterRelativeStart:
		return fmt.Sprintf("%v %d groups back", f.Type, f.Start.Group)
	case LocationFilterAbsoluteStart:
		return fmt.Sprintf("%v from %v", f.Type, f.Start)
	case LocationFilterAbsoluteGroupEnd:
		end, _ := f.EndGroup()
		return fmt.Sprintf("%v from %v through group %d", f.Type, f.Start, end)
	case LocationFilterAbsoluteRange:
		end, _ := f.End()
		return fmt.Sprintf("%v from %v through %v", f.Type, f.Start, end)
	}
	return f.Type.String()
}

// validate checks what both drafts make a PROTOCOL_VIOLATION: an unknown type,
// and an end Group past 2^64-1.
func (f LocationFilter) validate() error {
	if !f.Type.valid() {
		return errInvalidFilterType
	}
	if f.Type.hasEndGroup() && f.EndGroupDelta > maxUint64-f.Start.Group {
		return errEndGroupOverflow
	}
	return nil
}

// appendValue writes the filter as the parameter's value in version's
// encoding.
func (f LocationFilter) appendValue(buf []byte, version Version) ([]byte, error) {
	if err := f.validate(); err != nil {
		return nil, err
	}
	if version == Version18 {
		inner, err := f.append18(nil)
		if err != nil {
			return nil, err
		}
		buf = vi64.Append(buf, uint64(len(inner)))
		return append(buf, inner...), nil
	}

	buf = vi64.Append(buf, uint64(f.Type))
	switch f.Type {
	case LocationFilterRelativeStart:
		buf = vi64.Append(buf, f.Start.Group)
	case LocationFilterAbsoluteStart:
		buf = f.Start.append(buf)
	case LocationFilterAbsoluteGroupEnd:
		buf = f.Start.append(buf)
		buf = vi64.Append(buf, f.EndGroupDelta)
	case LocationFilterAbsoluteRange:
		buf = f.Start.append(buf)
		buf = vi64.Append(buf, f.EndGroupDelta)
		buf = vi64.Append(buf, f.EndObject)
	}
	return buf, nil
}

// append18 writes the body of draft-18's SUBSCRIPTION_FILTER: Filter Type,
// then Start Location for the absolute types, then End Group Delta for
// Absolute Range.
//
// Draft-18 has no way to say "no filter", but an Absolute Start at {0, 0}
// admits exactly what no filter does. It cannot express a relative start of
// more than zero Groups, nor an end inside a Group, so those fail here rather
// than being sent as something the subscriber did not ask for.
func (f LocationFilter) append18(buf []byte) ([]byte, error) {
	switch f.Type {
	case LocationFilterNone:
		buf = vi64.Append(buf, filterType18AbsoluteStart)
		return Location{}.append(buf), nil
	case LocationFilterRelativeStart:
		if f.Start.Group != 0 {
			return nil, errFilterNotInDraft18
		}
		return vi64.Append(buf, filterType18NextGroupStart), nil
	case LocationFilterNextObject:
		return vi64.Append(buf, filterType18LargestObject), nil
	case LocationFilterAbsoluteStart:
		buf = vi64.Append(buf, filterType18AbsoluteStart)
		return f.Start.append(buf), nil
	case LocationFilterAbsoluteGroupEnd:
		buf = vi64.Append(buf, filterType18AbsoluteRange)
		buf = f.Start.append(buf)
		return vi64.Append(buf, f.EndGroupDelta), nil
	}
	return nil, errFilterNotInDraft18
}

// parseValue reads the parameter's value in version's encoding and returns
// the bytes consumed.
func (f *LocationFilter) parseValue(data []byte, version Version) (int, error) {
	if version == Version18 {
		value, n, err := parseByteString(data, maxValueLength)
		if err != nil {
			return n, err
		}
		consumed, err := f.parse18(value)
		if err != nil {
			return n, err
		}
		if consumed != len(value) {
			return n, errTrailingBytes
		}
		return n, nil
	}

	t, parsed, err := vi64.Parse(data)
	if err != nil {
		return parsed, err
	}
	*f = LocationFilter{Type: LocationFilterType(t)}
	if !f.Type.valid() {
		return parsed, errInvalidFilterType
	}

	var n int
	switch {
	case f.Type == LocationFilterRelativeStart:
		f.Start.Group, n, err = vi64.Parse(data[parsed:])
		parsed += n
	case f.Type.hasStart():
		n, err = f.Start.parse(data[parsed:])
		parsed += n
	}
	if err != nil {
		return parsed, err
	}
	if f.Type.hasEndGroup() {
		f.EndGroupDelta, n, err = vi64.Parse(data[parsed:])
		parsed += n
		if err != nil {
			return parsed, err
		}
	}
	if f.Type == LocationFilterAbsoluteRange {
		f.EndObject, n, err = vi64.Parse(data[parsed:])
		parsed += n
		if err != nil {
			return parsed, err
		}
	}
	return parsed, f.validate()
}

// parse18 reads the body of draft-18's SUBSCRIPTION_FILTER into its draft-22
// equivalent.
func (f *LocationFilter) parse18(data []byte) (int, error) {
	t, parsed, err := vi64.Parse(data)
	if err != nil {
		return parsed, err
	}
	*f = LocationFilter{}
	switch t {
	case filterType18NextGroupStart:
		f.Type = LocationFilterRelativeStart
		return parsed, nil
	case filterType18LargestObject:
		f.Type = LocationFilterNextObject
		return parsed, nil
	case filterType18AbsoluteStart:
		f.Type = LocationFilterAbsoluteStart
	case filterType18AbsoluteRange:
		f.Type = LocationFilterAbsoluteGroupEnd
	default:
		return parsed, errInvalidFilterType
	}

	n, err := f.Start.parse(data[parsed:])
	parsed += n
	if err != nil {
		return parsed, err
	}
	if f.Type == LocationFilterAbsoluteGroupEnd {
		f.EndGroupDelta, n, err = vi64.Parse(data[parsed:])
		parsed += n
		if err != nil {
			return parsed, err
		}
	}
	return parsed, f.validate()
}

// MaxObjectID is the largest Object ID. An inclusive end Location whose Object
// is MaxObjectID covers the rest of its Group, whatever that turns out to be.
const MaxObjectID = maxUint64

// Compare orders two Locations: Group first, then Object (draft-22 Section
// 8.2). It returns -1, 0 or +1.
func (l Location) Compare(o Location) int {
	switch {
	case l.Group < o.Group:
		return -1
	case l.Group > o.Group:
		return 1
	case l.Object < o.Object:
		return -1
	case l.Object > o.Object:
		return 1
	}
	return 0
}

// LocationRange is a resolved filter: the Locations from Start to End, both
// inclusive, or from Start on when Open.
type LocationRange struct {
	Start Location
	End   Location
	Open  bool
}

// Contains reports whether l is in the range.
func (r LocationRange) Contains(l Location) bool {
	if l.Compare(r.Start) < 0 {
		return false
	}
	return r.Open || l.Compare(r.End) <= 0
}

func (r LocationRange) String() string {
	if r.Open {
		return fmt.Sprintf("[%v, ...)", r.Start)
	}
	return fmt.Sprintf("[%v, %v]", r.Start, r.End)
}

// nextObject is the Location after Largest Object, or {0, 0} when nothing has
// been published (draft-22 Section 3.1.4).
func nextObject(largest Location, hasLargest bool) Location {
	if !hasLargest {
		return Location{}
	}
	if largest.Object == MaxObjectID {
		if largest.Group == maxUint64 {
			return largest
		}
		return Location{Group: largest.Group + 1}
	}
	return Location{Group: largest.Group, Object: largest.Object + 1}
}

// relativeStartGroup is Largest Object.Group + 1 - groupsBack, clamped to
// [0, 2^64-1] as draft-22 requires, and 0 when nothing has been published.
func relativeStartGroup(largest Location, hasLargest bool, groupsBack uint64) uint64 {
	if !hasLargest {
		return 0
	}
	if largest.Group == maxUint64 {
		// The Next Group is 2^64, one past what a Group ID can hold.
		if groupsBack == 0 {
			return maxUint64
		}
		return maxUint64 - (groupsBack - 1)
	}
	next := largest.Group + 1
	if groupsBack > next {
		return 0
	}
	return next - groupsBack
}

// start is the first Location the filter admits, given the publisher's
// Largest Object.
func (f LocationFilter) start(largest Location, hasLargest bool) Location {
	switch f.Type {
	case LocationFilterRelativeStart:
		return Location{Group: relativeStartGroup(largest, hasLargest, f.Start.Group)}
	case LocationFilterNextObject:
		return nextObject(largest, hasLargest)
	case LocationFilterNone:
		return Location{}
	}
	return f.Start
}

// SubscriptionRange resolves the filter for a subscription, against the
// publisher's Largest Object (hasLargest false when nothing has been
// published). A subscription range is always valid, even one that ends
// before Largest Object (draft-22 Section 3.3.1).
func (f LocationFilter) SubscriptionRange(largest Location, hasLargest bool) LocationRange {
	end, bounded := f.End()
	return LocationRange{Start: f.start(largest, hasLargest), End: end, Open: !bounded}
}

// FetchRange resolves the filter the way a FETCH, or a fill, evaluates it: an
// open end stops at Largest Object, and so does an end beyond it (draft-22
// Section 9.20.9). It returns false for an empty range -- nothing published,
// or a start after Largest Object -- which a FETCH answers with INVALID_RANGE
// and a fill answers by opening no stream.
func (f LocationFilter) FetchRange(largest Location, hasLargest bool) (LocationRange, bool) {
	if !hasLargest {
		return LocationRange{}, false
	}
	r := LocationRange{Start: f.start(largest, hasLargest), End: largest}
	if end, ok := f.End(); ok && end.Compare(largest) < 0 {
		r.End = end
	}
	if r.Start.Compare(r.End) > 0 {
		return LocationRange{}, false
	}
	return r, true
}

// FetchFilterFromV18 turns a draft-18 FETCH range into the filter that admits
// the same Objects. Draft-18's End Location is the last Object plus one,
// except that an Object of 0 means the whole Group (draft-18 Section
// 10.12.1), so {5, 0} is all of Group 5 and {5, 3} ends at Object 2.
func FetchFilterFromV18(start, end Location) (LocationFilter, error) {
	if end.Group < start.Group {
		return LocationFilter{}, errFetchEndBeforeStart
	}
	f := LocationFilter{Start: start, EndGroupDelta: end.Group - start.Group}
	if end.Object == 0 {
		f.Type = LocationFilterAbsoluteGroupEnd
		return f, nil
	}
	f.Type = LocationFilterAbsoluteRange
	f.EndObject = end.Object - 1
	if end.Group == start.Group && f.EndObject < start.Object {
		return LocationFilter{}, errFetchEndBeforeStart
	}
	return f, nil
}

// V18FetchRange turns a filter into draft-18's explicit FETCH Start and End
// Locations. Draft-18 FETCH has no relative form and no way to say "up to
// whatever is largest", so an open end becomes the whole of the last possible
// Group, which the publisher clamps to Largest Object; a relative start
// cannot be expressed at all.
func V18FetchRange(f LocationFilter) (start, end Location, err error) {
	switch f.Type {
	case LocationFilterNone:
		return Location{}, Location{Group: maxUint64}, nil
	case LocationFilterAbsoluteStart:
		return f.Start, Location{Group: maxUint64}, nil
	case LocationFilterAbsoluteGroupEnd, LocationFilterAbsoluteRange:
		if err := f.validate(); err != nil {
			return Location{}, Location{}, err
		}
		last, _ := f.End()
		return f.Start, V18EndLocation(last), nil
	}
	return Location{}, Location{}, errFilterNotInDraft18
}

// V18EndLocation encodes an inclusive end Location the way draft-18's FETCH
// and FETCH_OK do: the last Object plus one, or Object 0 for the whole Group.
func V18EndLocation(last Location) Location {
	if last.Object == MaxObjectID {
		return Location{Group: last.Group}
	}
	return Location{Group: last.Group, Object: last.Object + 1}
}

// InclusiveFromV18End decodes draft-18's FETCH_OK End Location to the last
// Location covered, inclusive.
func InclusiveFromV18End(end Location) Location {
	if end.Object == 0 {
		return Location{Group: end.Group, Object: MaxObjectID}
	}
	return Location{Group: end.Group, Object: end.Object - 1}
}
