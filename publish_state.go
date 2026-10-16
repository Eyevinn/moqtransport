package moqtransport

import (
	"errors"
	"fmt"

	"github.com/Eyevinn/moqtransport/internal/wire2"
)

// ErrNotInThisDraft is returned for something the session's negotiated draft
// does not have: PUBLISH_STATE_NOTIFY on a draft-18 session, a joining FETCH
// on a draft-22 one. Errors wrapping it name what was asked for.
var ErrNotInThisDraft = errors.New("not available in the negotiated MOQT draft")

// PublishState is a change in a subscription's state that the publisher
// reports on its own initiative with PUBLISH_STATE_NOTIFY (draft-22 Section
// 9.10): the subscription paused or resumed, or a different location filter
// in effect, for a reason other than the subscriber's REQUEST_UPDATE. The
// subscriber does not answer it.
//
// Each Has field says whether its value is part of the change; what is not
// mentioned is unchanged.
type PublishState struct {
	// LargestObject is where in the Track the change took effect. The
	// publisher MUST include it when it knows it, and NotifyState does so from
	// what the subscription has published.
	LargestObject    Location
	HasLargestObject bool

	// Paused says whether the publisher has paused the subscription.
	Paused    bool
	HasPaused bool

	// Filter is the location filter now in effect.
	Filter    LocationFilter
	HasFilter bool
}

func (p PublishState) parameters() wire2.Parameters {
	params := wire2.Parameters{}
	if p.HasLargestObject {
		params = append(params, wire2.LocationParameter(wire2.ParamLargestObject, p.LargestObject))
	}
	if p.HasPaused {
		forward := uint8(1)
		if p.Paused {
			forward = 0
		}
		params = append(params, wire2.Uint8Parameter(wire2.ParamForward, forward))
	}
	if p.HasFilter {
		params = append(params, wire2.LocationFilterParameter(p.Filter))
	}
	return params
}

// publishStateFrom reads a PUBLISH_STATE_NOTIFY's parameters.
func publishStateFrom(params wire2.Parameters) (PublishState, error) {
	var state PublishState
	state.LargestObject, state.HasLargestObject = params.LargestObject()
	if _, ok := params.Get(wire2.ParamForward); ok {
		forward, err := params.Forward()
		if err != nil {
			return PublishState{}, err
		}
		state.Paused, state.HasPaused = !forward, true
	}
	if f, ok := params.LocationFilter(); ok {
		state.Filter, state.HasFilter = f, true
	}
	return state, nil
}

// NotifyState tells the subscriber that the subscription's state has changed
// at the publisher, with PUBLISH_STATE_NOTIFY on the subscription's stream.
// It applies the change locally too: a pause reported here is one this
// publisher has made, and Paused reflects it.
//
// Draft-22 forbids using it to change a parameter the subscriber controls
// unless the subscriber asked for the change; the library cannot tell whether
// it did, so that is the caller's to respect. On a draft-18 session it returns
// ErrNotInThisDraft.
func (s *Subscription) NotifyState(state PublishState) error {
	if !s.version.AtLeast(wire2.Version22) {
		return fmt.Errorf("%w: PUBLISH_STATE_NOTIFY", ErrNotInThisDraft)
	}
	s.mu.Lock()
	if state.HasLargestObject {
		s.noteLargestLocked(state.LargestObject)
	}
	state.LargestObject, state.HasLargestObject = s.largest, s.hasLargest
	if state.HasPaused {
		s.forward = !state.Paused
	}
	if state.HasFilter {
		s.filter, s.hasFilter = activeFilter(state.Filter, true)
	}
	s.mu.Unlock()
	return s.write(&wire2.PublishStateNotify{Parameters: state.parameters()})
}
