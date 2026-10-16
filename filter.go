package moqtransport

import "github.com/Eyevinn/moqtransport/internal/wire2"

// NextGroup is the location filter that starts at the beginning of the Group
// after the publisher's Largest Object, and is open ended: the filter for
// joining a live track at its next join point. It is draft-18's Next Group
// Start, and a relative start of zero Groups in draft-22.
func NextGroup() LocationFilter {
	return LocationFilter{Type: wire2.LocationFilterRelativeStart}
}

// RelativeStart starts groupsBack Groups before the Next Group and is open
// ended: 1 is the current Group, 2 the one before it. The start is clamped to
// Group 0. Draft-18 can express only groupsBack 0, so anything else fails to
// send on a draft-18 session.
func RelativeStart(groupsBack uint64) LocationFilter {
	return LocationFilter{Type: wire2.LocationFilterRelativeStart, Start: Location{Group: groupsBack}}
}

// NextObject starts just past the publisher's Largest Object and is open
// ended: the subscription delivers only what is published from now on.
// Draft-18 calls it the Largest Object filter.
func NextObject() LocationFilter {
	return LocationFilter{Type: wire2.LocationFilterNextObject}
}

// AbsoluteStart starts at start and is open ended.
func AbsoluteStart(start Location) LocationFilter {
	return LocationFilter{Type: wire2.LocationFilterAbsoluteStart, Start: start}
}

// AbsoluteRange admits start through end, both inclusive. An end whose Object
// is [MaxObjectID] covers the whole of its Group, which is the only kind of
// end draft-18 subscriptions can express. An end before start fails to send.
func AbsoluteRange(start, end Location) LocationFilter {
	f := LocationFilter{Start: start, EndGroupDelta: end.Group - start.Group}
	if end.Object == MaxObjectID {
		f.Type = wire2.LocationFilterAbsoluteGroupEnd
		return f
	}
	f.Type = wire2.LocationFilterAbsoluteRange
	f.EndObject = end.Object
	return f
}
