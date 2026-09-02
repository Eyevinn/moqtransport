package wire2

import (
	"fmt"
	"math"
	"time"

	"github.com/Eyevinn/locmaf/vi64"
)

// FilterType selects which Objects of a Track a subscription delivers
// (draft-ietf-moq-transport-18, Section 5.1.2). It also decides which of the
// Subscription Filter's optional fields are present, so it is a tag rather
// than a hint.
type FilterType uint64

const (
	// FilterNextGroupStart starts at {Largest Object.Group + 1, 0} and is open
	// ended. It is the filter for joining a live track at the next join point.
	FilterNextGroupStart FilterType = 0x1
	// FilterLargestObject starts at {Largest Object.Group, Largest
	// Object.Object + 1} and is open ended. This is the filter a Joining FETCH
	// requires of the subscription it joins.
	FilterLargestObject FilterType = 0x2
	// FilterAbsoluteStart starts at an explicit Location and is open ended.
	// With Start = {0, 0} it is an unfiltered subscription.
	FilterAbsoluteStart FilterType = 0x3
	// FilterAbsoluteRange starts at an explicit Location and ends at a Group
	// derived from it.
	FilterAbsoluteRange FilterType = 0x4
)

func (t FilterType) String() string {
	switch t {
	case FilterNextGroupStart:
		return "NextGroupStart"
	case FilterLargestObject:
		return "LargestObject"
	case FilterAbsoluteStart:
		return "AbsoluteStart"
	case FilterAbsoluteRange:
		return "AbsoluteRange"
	}
	return fmt.Sprintf("invalid filter type: %#x", uint64(t))
}

// hasStart reports whether this filter type carries an explicit Start
// Location.
func (t FilterType) hasStart() bool {
	return t == FilterAbsoluteStart || t == FilterAbsoluteRange
}

// SubscriptionFilter is the value of the SUBSCRIPTION_FILTER parameter
// (Section 5.1.2). Absent from a SUBSCRIBE the subscription is unfiltered;
// absent from a REQUEST_UPDATE the current filter is unchanged.
type SubscriptionFilter struct {
	Type FilterType

	// StartLocation is present for FilterAbsoluteStart and
	// FilterAbsoluteRange. For the two relative types the publisher computes
	// the start and reports it through LARGEST_OBJECT in the response.
	StartLocation Location

	// EndGroupDelta is present only for FilterAbsoluteRange. Zero means the
	// remainder of the Start Location's Group passes the filter; otherwise the
	// last Group delivered is Start.Group plus this delta.
	EndGroupDelta uint64
}

// EndGroup returns the last Group the filter admits, and whether the filter
// has an end at all.
func (f SubscriptionFilter) EndGroup() (uint64, bool) {
	if f.Type != FilterAbsoluteRange {
		return 0, false
	}
	return f.StartLocation.Group + f.EndGroupDelta, true
}

func (f SubscriptionFilter) String() string {
	switch f.Type {
	case FilterAbsoluteStart:
		return fmt.Sprintf("%v from %v", f.Type, f.StartLocation)
	case FilterAbsoluteRange:
		end, _ := f.EndGroup()
		return fmt.Sprintf("%v from %v through group %d", f.Type, f.StartLocation, end)
	}
	return f.Type.String()
}

// appendValue writes the filter as the SUBSCRIPTION_FILTER parameter's value.
func (f SubscriptionFilter) appendValue(buf []byte) ([]byte, error) {
	switch f.Type {
	case FilterNextGroupStart, FilterLargestObject, FilterAbsoluteStart, FilterAbsoluteRange:
	default:
		return nil, errInvalidFilterType
	}
	buf = vi64.Append(buf, uint64(f.Type))
	if f.Type.hasStart() {
		buf = f.StartLocation.append(buf)
	}
	if f.Type == FilterAbsoluteRange {
		// An End Group past 2^64-1 is a PROTOCOL_VIOLATION, and there is no
		// point writing one we would refuse to read.
		if f.EndGroupDelta > maxUint64-f.StartLocation.Group {
			return nil, errEndGroupOverflow
		}
		buf = vi64.Append(buf, f.EndGroupDelta)
	}
	return buf, nil
}

// parseValue reads a filter from the SUBSCRIPTION_FILTER parameter's value.
// The caller passes exactly the parameter's bytes; anything left over is a
// PROTOCOL_VIOLATION.
func (f *SubscriptionFilter) parseValue(data []byte) error {
	t, n, err := vi64.Parse(data)
	if err != nil {
		return err
	}
	data = data[n:]

	f.Type = FilterType(t)
	switch f.Type {
	case FilterNextGroupStart, FilterLargestObject, FilterAbsoluteStart, FilterAbsoluteRange:
	default:
		return errInvalidFilterType
	}

	if f.Type.hasStart() {
		if n, err = f.StartLocation.parse(data); err != nil {
			return err
		}
		data = data[n:]
	}
	if f.Type == FilterAbsoluteRange {
		if f.EndGroupDelta, n, err = vi64.Parse(data); err != nil {
			return err
		}
		data = data[n:]
		if f.EndGroupDelta > maxUint64-f.StartLocation.Group {
			return errEndGroupOverflow
		}
	}
	if len(data) > 0 {
		return errTrailingBytes
	}
	return nil
}

// SubscriptionFilterParameter builds the SUBSCRIPTION_FILTER parameter.
func SubscriptionFilterParameter(f SubscriptionFilter) (Parameter, error) {
	value, err := f.appendValue(nil)
	if err != nil {
		return Parameter{}, err
	}
	return BytesParameter(ParamSubscriptionFilter, value), nil
}

// Filter returns the SUBSCRIPTION_FILTER parameter's value, and whether it was
// present. An absent filter means unfiltered in a SUBSCRIBE or PUBLISH_OK, and
// unchanged in a REQUEST_UPDATE, which is a distinction only the caller can
// make -- hence the bool rather than a default.
func (pp Parameters) Filter() (SubscriptionFilter, bool, error) {
	p, ok := pp.Get(ParamSubscriptionFilter)
	if !ok {
		return SubscriptionFilter{}, false, nil
	}
	var f SubscriptionFilter
	if err := f.parseValue(p.Bytes); err != nil {
		return SubscriptionFilter{}, true, err
	}
	return f, true, nil
}

// SubscriberPriority returns the SUBSCRIBER_PRIORITY parameter, defaulting to
// 128 when it is absent (Section 10.2.7).
func (pp Parameters) SubscriberPriority() uint8 {
	if p, ok := pp.Get(ParamSubscriberPriority); ok {
		return uint8(p.Number)
	}
	return DefaultSubscriberPriority
}

// GroupOrder returns the GROUP_ORDER parameter and whether it was present.
//
// There is no single default: omitted from a SUBSCRIBE the publisher's own
// preference for the Track applies, while omitted from a FETCH it is
// Ascending. Only the caller knows which message this came from.
func (pp Parameters) GroupOrder() (GroupOrder, bool, error) {
	p, ok := pp.Get(ParamGroupOrder)
	if !ok {
		return 0, false, nil
	}
	order := GroupOrder(p.Number)
	if !order.Valid() {
		return 0, true, errInvalidGroupOrder
	}
	return order, true, nil
}

// Forward returns the FORWARD parameter, defaulting to true when it is absent
// (Section 10.2.12).
func (pp Parameters) Forward() (bool, error) {
	p, ok := pp.Get(ParamForward)
	if !ok {
		return true, nil
	}
	switch p.Number {
	case 0:
		return false, nil
	case 1:
		return true, nil
	}
	return false, errInvalidBoolValue
}

// RendezvousTimeout returns the RENDEZVOUS_TIMEOUT parameter (Section 10.2.6)
// and whether it was present: how long the subscriber is willing to wait for
// a publisher of a Track that has none yet. Absent, the subscriber wants an
// immediate answer, which the section spells as a default of 0. The wire
// carries milliseconds; a value too large for a Duration saturates.
func (pp Parameters) RendezvousTimeout() (time.Duration, bool) {
	p, ok := pp.Get(ParamRendezvousTimeout)
	if !ok {
		return 0, false
	}
	const maxMillis = uint64(math.MaxInt64 / int64(time.Millisecond))
	if p.Number > maxMillis {
		return time.Duration(math.MaxInt64), true
	}
	return time.Duration(p.Number) * time.Millisecond, true
}

// LargestObject returns the LARGEST_OBJECT parameter and whether it was
// present. A publisher that has published anything on the track MUST send it,
// so its absence means the track is empty so far.
func (pp Parameters) LargestObject() (Location, bool) {
	p, ok := pp.Get(ParamLargestObject)
	if !ok {
		return Location{}, false
	}
	return p.Location, true
}

// DefaultPublisherPriority returns the DEFAULT_PUBLISHER_PRIORITY Track
// Property, or 128 if it is absent (Section 12.4).
//
// This is the priority a Subgroup or Datagram inherits when its header sets the
// DEFAULT_PRIORITY bit and leaves the field off the wire.
func (pp KVPList) DefaultPublisherPriority() (uint8, error) {
	p, ok := pp.Get(PropertyDefaultPublisherPriority)
	if !ok {
		return DefaultPublisherPriority, nil
	}
	if p.ValueVarInt > 255 {
		return 0, errPriorityOutOfRange
	}
	return uint8(p.ValueVarInt), nil
}

// DefaultPublisherGroupOrder returns the DEFAULT_PUBLISHER_GROUP_ORDER Track
// Property, or Ascending if it is absent (Section 12.5).
func (pp KVPList) DefaultPublisherGroupOrder() (GroupOrder, error) {
	p, ok := pp.Get(PropertyDefaultPublisherGroupOrder)
	if !ok {
		return GroupOrderAscending, nil
	}
	order := GroupOrder(p.ValueVarInt)
	if !order.Valid() {
		return 0, errInvalidGroupOrder
	}
	return order, nil
}

// DefaultSubscriberPriority is the value a publisher uses when SUBSCRIBER_PRIORITY
// is omitted (Section 10.2.7).
const DefaultSubscriberPriority uint8 = 128

// DefaultPublisherPriority is the priority a Subgroup or Datagram inherits when
// the Track says nothing (Section 12.4).
const DefaultPublisherPriority uint8 = 128
