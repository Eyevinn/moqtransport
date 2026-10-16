package wire2

import (
	"math"
	"time"
)

// LocationFilter returns the LOCATION_FILTER parameter (SUBSCRIPTION_FILTER in
// draft-18) and whether it was present. An absent filter means unfiltered in a
// SUBSCRIBE or FETCH and unchanged in a REQUEST_UPDATE, which is a distinction
// only the caller can make -- hence the bool rather than a default.
func (pp Parameters) LocationFilter() (LocationFilter, bool) {
	p, ok := pp.Get(ParamLocationFilter)
	if !ok {
		return LocationFilter{}, false
	}
	return p.Filter, true
}

// FillParameters returns the FILL_PARAMETERS block and whether it was present.
// Its presence, even empty, is what asks for a fill fetch stream (draft-22
// Section 9.20.15).
func (pp Parameters) FillParameters() (Parameters, bool) {
	p, ok := pp.Get(ParamFillParameters)
	if !ok {
		return nil, false
	}
	return p.Params, true
}

// FillTimeout returns the FILL_TIMEOUT parameter (draft-22 Section 9.20.5)
// and whether it was present: the total time a relay should spend waiting
// upstream for Objects it does not have before reporting them as timed out.
func (pp Parameters) FillTimeout() (time.Duration, bool) {
	p, ok := pp.Get(ParamFillTimeout)
	if !ok {
		return 0, false
	}
	return millis(p.Number), true
}

// IncludeProperties returns the INCLUDE_PROPERTIES parameter, defaulting to
// true when it is absent (draft-22 Section 9.20.21).
func (pp Parameters) IncludeProperties() (bool, error) {
	return pp.flag(ParamIncludeProperties)
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
// (Section 10.2.12). False means the subscription is paused.
func (pp Parameters) Forward() (bool, error) {
	return pp.flag(ParamForward)
}

// flag reads a uint8 parameter that may only be 0 or 1, and defaults to 1.
// Any other value is a PROTOCOL_VIOLATION.
func (pp Parameters) flag(typ uint64) (bool, error) {
	p, ok := pp.Get(typ)
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
	return millis(p.Number), true
}

// millis converts a wire duration in milliseconds, saturating at the largest
// Duration rather than wrapping.
func millis(ms uint64) time.Duration {
	const maxMillis = uint64(math.MaxInt64 / int64(time.Millisecond))
	if ms > maxMillis {
		return time.Duration(math.MaxInt64)
	}
	return time.Duration(ms) * time.Millisecond
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
