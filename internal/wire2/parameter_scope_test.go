package wire2

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var allContexts = []ParameterContext{
	ContextSubscribe, ContextSubscribeOk, ContextPublish, ContextPublishOk, ContextPublishStateNotify,
	ContextFetch, ContextFetchOk, ContextTrackStatus, ContextTrackStatusOk,
	ContextPublishNamespace, ContextPublishNamespaceOk, ContextSubscribeNamespace, ContextSubscribeNamespaceOk,
	ContextSubscribeTracks, ContextSubscribeTracksOk,
	ContextUpdateSubscription, ContextUpdateFetch, ContextUpdatePublishNamespace,
	ContextUpdateSubscribeNamespace, ContextUpdateSubscribeTracks, ContextRequestUpdateOk,
}

// The tables have to agree with the registries: a scope naming a type its
// draft does not register is a typo, and a registered type no scope admits
// could never be sent.
func TestParameterScopesMatchTheRegistries(t *testing.T) {
	for _, version := range []Version{Version18, Version22} {
		scopes, err := scopesFor(version)
		require.NoError(t, err)
		registry, err := registryFor(version)
		require.NoError(t, err)

		used := map[uint64]bool{}
		for _, ctx := range allContexts {
			if ctx == ContextPublishStateNotify && version == Version18 {
				continue
			}
			set, ok := scopes[ctx]
			require.True(t, ok, "%v has no scope for %v", version, ctx)
			for typ := range set {
				_, registered := registry[typ]
				assert.True(t, registered, "%v: %v admits unregistered %#x", version, ctx, typ)
				used[typ] = true
			}
		}
		for typ := range registry {
			assert.True(t, used[typ], "%v: %s is allowed nowhere", version, ParameterName(typ))
		}
	}
}

// Spot checks against the drafts' text, both the places the two drafts
// differ and the ones a careless table would get wrong.
func TestParameterScopes(t *testing.T) {
	for _, tc := range []struct {
		version Version
		ctx     ParameterContext
		typ     uint64
		allowed bool
	}{
		// GROUP_ORDER is fixed for a subscription's life: not updatable.
		{Version18, ContextUpdateSubscription, ParamGroupOrder, false},
		{Version22, ContextUpdateSubscription, ParamGroupOrder, false},
		// Draft-18 sets a PUBLISH-initiated subscription's parameters in
		// PUBLISH_OK; draft-19 moved them to REQUEST_UPDATE (WG PR #1790).
		{Version18, ContextPublishOk, ParamSubscriberPriority, true},
		{Version22, ContextPublishOk, ParamSubscriberPriority, false},
		{Version22, ContextUpdateSubscription, ParamSubscriberPriority, true},
		// Draft-22's PUBLISH carries the subscription's initial parameters.
		{Version18, ContextPublish, ParamGroupOrder, false},
		{Version22, ContextPublish, ParamGroupOrder, true},
		// EXPIRES in the namespace OKs is draft-22's.
		{Version18, ContextPublishNamespaceOk, ParamExpires, false},
		{Version22, ContextPublishNamespaceOk, ParamExpires, true},
		{Version22, ContextSubscribeNamespaceOk, ParamExpires, true},
		// Fills: on a subscription, never on a FETCH.
		{Version22, ContextSubscribe, ParamFillParameters, true},
		{Version22, ContextUpdateSubscription, ParamFillParameters, true},
		{Version22, ContextFetch, ParamFillParameters, false},
		{Version22, ContextFetch, ParamFillTimeout, true},
		{Version22, ContextSubscribe, ParamFillTimeout, false},
		// RENDEZVOUS_TIMEOUT is a SUBSCRIBE-only parameter.
		{Version22, ContextSubscribe, ParamRendezvousTimeout, true},
		{Version22, ContextUpdateSubscription, ParamRendezvousTimeout, false},
		{Version22, ContextFetch, ParamRendezvousTimeout, false},
		// TRACK_STATUS is SUBSCRIBE without the delivery parameters.
		{Version18, ContextTrackStatus, ParamSubscriberPriority, false},
		{Version22, ContextTrackStatus, ParamSubscriberPriority, false},
		{Version22, ContextTrackStatus, ParamIncludeProperties, true},
		// PUBLISH_STATE_NOTIFY reports the three things a publisher may
		// change.
		{Version22, ContextPublishStateNotify, ParamLocationFilter, true},
		{Version22, ContextPublishStateNotify, ParamForward, true},
		{Version22, ContextPublishStateNotify, ParamSubscriberPriority, false},
		// FORWARD on a SUBSCRIBE_TRACKS update arrived in draft-19 (#1812).
		{Version18, ContextUpdateSubscribeTracks, ParamForward, false},
		{Version22, ContextUpdateSubscribeTracks, ParamForward, true},
		// Only a namespace request's prefix can be updated.
		{Version22, ContextUpdateSubscribeNamespace, ParamTrackNamespacePrefix, true},
		{Version22, ContextUpdateSubscription, ParamTrackNamespacePrefix, false},
		// The track filter selects tracks, so it belongs to SUBSCRIBE_TRACKS.
		{Version22, ContextSubscribeTracks, ParamTrackPropertyFilter, true},
		{Version22, ContextSubscribe, ParamTrackPropertyFilter, false},
		// FETCH_OK has no parameters at all.
		{Version22, ContextFetchOk, ParamLargestObject, false},
		// Responses carry no AUTHORIZATION_TOKEN.
		{Version22, ContextSubscribeOk, ParamAuthorizationToken, false},
		{Version22, ContextRequestUpdateOk, ParamAuthorizationToken, false},
	} {
		err := CheckParameters(tc.version, tc.ctx, Parameters{{Type: tc.typ}})
		if tc.allowed {
			assert.NoError(t, err, "%v %v %s", tc.version, tc.ctx, ParameterName(tc.typ))
		} else {
			assert.ErrorAs(t, err, &parameterNotAllowedError{}, "%v %v %s", tc.version, tc.ctx, ParameterName(tc.typ))
		}
	}
}

// The three places the drafts contradict themselves are read the way that
// does not end a session over a peer following one half of the text.
func TestParameterScopesWhereTheTextConflicts(t *testing.T) {
	// "The same parameters ... it would have set in a SUBSCRIBE_OK".
	for _, v := range []Version{Version18, Version22} {
		assert.NoError(t, CheckParameters(v, ContextTrackStatusOk, Parameters{{Type: ParamExpires}}))
	}
	// Draft-22 Section 3.6.2: anything valid in SUBSCRIBE is valid in
	// SUBSCRIBE_TRACKS.
	assert.NoError(t, CheckParameters(Version22, ContextSubscribeTracks, Parameters{{Type: ParamFillParameters}}))
	assert.NoError(t, CheckParameters(Version22, ContextSubscribeTracks, Parameters{{Type: ParamLocationFilter}}))
	// Draft-18's delivery timeouts are listed for "REQUEST_UPDATE", any of
	// them.
	assert.NoError(t, CheckParameters(Version18, ContextUpdateFetch, Parameters{{Type: ParamObjectDeliveryTimeout}}))
	assert.Error(t, CheckParameters(Version22, ContextUpdateFetch, Parameters{{Type: ParamObjectDeliveryTimeout}}))
}

func TestCheckParametersReportsTheFirstOffender(t *testing.T) {
	err := CheckParameters(Version22, ContextSubscribeOk, Parameters{
		{Type: ParamExpires},
		{Type: ParamForward},
	})
	require.Error(t, err)
	assert.Equal(t, "FORWARD is not allowed in SUBSCRIBE_OK", err.Error())

	assert.NoError(t, CheckParameters(Version22, ContextFetchOk, nil))
	assert.Error(t, CheckParameters(Version(20), ContextFetchOk, nil))
	assert.Error(t, CheckParameters(Version18, ContextPublishStateNotify, nil), "draft-18 has no PUBLISH_STATE_NOTIFY")
	assert.Equal(t, "REQUEST_UPDATE for a FETCH", ContextUpdateFetch.String())
}
