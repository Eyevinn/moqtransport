package wire2

import "fmt"

// ParameterContext is where in a request's exchange a parameter block sits,
// which is what decides the parameters it may carry. Both drafts make a
// parameter outside the messages its definition lists a PROTOCOL_VIOLATION
// (draft-18 Section 10.2.1, draft-22 Section 9.20.1).
//
// Most contexts are a message type. REQUEST_OK and REQUEST_UPDATE are not
// enough on their own: they serve several requests, and what they may carry
// depends on which -- a SUBSCRIBE_NAMESPACE's REQUEST_UPDATE can carry
// TRACK_NAMESPACE_PREFIX and a subscription's cannot.
type ParameterContext uint8

const (
	ContextSubscribe ParameterContext = iota + 1
	ContextSubscribeOk
	ContextPublish
	// ContextPublishOk is the REQUEST_OK answering a PUBLISH.
	ContextPublishOk
	ContextPublishStateNotify
	ContextFetch
	ContextFetchOk
	ContextTrackStatus
	// ContextTrackStatusOk is the REQUEST_OK answering a TRACK_STATUS.
	ContextTrackStatusOk
	ContextPublishNamespace
	ContextPublishNamespaceOk
	ContextSubscribeNamespace
	ContextSubscribeNamespaceOk
	ContextSubscribeTracks
	ContextSubscribeTracksOk
	// ContextUpdateSubscription is a REQUEST_UPDATE for a subscription,
	// whether it began with SUBSCRIBE or PUBLISH.
	ContextUpdateSubscription
	ContextUpdateFetch
	ContextUpdatePublishNamespace
	ContextUpdateSubscribeNamespace
	ContextUpdateSubscribeTracks
	// ContextRequestUpdateOk is the REQUEST_OK answering a REQUEST_UPDATE.
	ContextRequestUpdateOk
)

var contextNames = map[ParameterContext]string{
	ContextSubscribe:                "SUBSCRIBE",
	ContextSubscribeOk:              "SUBSCRIBE_OK",
	ContextPublish:                  "PUBLISH",
	ContextPublishOk:                "PUBLISH_OK",
	ContextPublishStateNotify:       "PUBLISH_STATE_NOTIFY",
	ContextFetch:                    "FETCH",
	ContextFetchOk:                  "FETCH_OK",
	ContextTrackStatus:              "TRACK_STATUS",
	ContextTrackStatusOk:            "TRACK_STATUS_OK",
	ContextPublishNamespace:         "PUBLISH_NAMESPACE",
	ContextPublishNamespaceOk:       "PUBLISH_NAMESPACE_OK",
	ContextSubscribeNamespace:       "SUBSCRIBE_NAMESPACE",
	ContextSubscribeNamespaceOk:     "SUBSCRIBE_NAMESPACE_OK",
	ContextSubscribeTracks:          "SUBSCRIBE_TRACKS",
	ContextSubscribeTracksOk:        "SUBSCRIBE_TRACKS_OK",
	ContextUpdateSubscription:       "REQUEST_UPDATE for a subscription",
	ContextUpdateFetch:              "REQUEST_UPDATE for a FETCH",
	ContextUpdatePublishNamespace:   "REQUEST_UPDATE for a PUBLISH_NAMESPACE",
	ContextUpdateSubscribeNamespace: "REQUEST_UPDATE for a SUBSCRIBE_NAMESPACE",
	ContextUpdateSubscribeTracks:    "REQUEST_UPDATE for a SUBSCRIBE_TRACKS",
	ContextRequestUpdateOk:          "REQUEST_UPDATE_OK",
}

func (c ParameterContext) String() string {
	if name, ok := contextNames[c]; ok {
		return name
	}
	return fmt.Sprintf("unknown parameter context %d", uint8(c))
}

// parameterSet is the parameter types a context admits.
type parameterSet map[uint64]struct{}

func allow(types ...uint64) parameterSet {
	set := make(parameterSet, len(types))
	for _, t := range types {
		set[t] = struct{}{}
	}
	return set
}

func union(sets ...parameterSet) parameterSet {
	out := parameterSet{}
	for _, s := range sets {
		for t := range s {
			out[t] = struct{}{}
		}
	}
	return out
}

// Draft-18 lists, for each parameter, the messages it may appear in (Section
// 10.2.2 onwards); its message definitions say only "defined in Section
// 10.2". These sets are those lists turned around.
//
// Two of draft-18's lists say "REQUEST_UPDATE" without saying for what:
// SUBGROUP_DELIVERY_TIMEOUT and OBJECT_DELIVERY_TIMEOUT. Read as written they
// may appear in any REQUEST_UPDATE, and so they do here; closing a session
// over the narrower reading would be enforcing something the text does not
// say.
var (
	subscribe18 = allow(ParamAuthorizationToken, ParamSubgroupDeliveryTimeout, ParamObjectDeliveryTimeout,
		ParamRendezvousTimeout, ParamSubscriberPriority, ParamGroupOrder, ParamLocationFilter,
		ParamForward, ParamNewGroupRequest)
	anyUpdate18 = allow(ParamAuthorizationToken, ParamSubgroupDeliveryTimeout, ParamObjectDeliveryTimeout)
)

var parameterScopes18 = map[ParameterContext]parameterSet{
	ContextSubscribe:   subscribe18,
	ContextSubscribeOk: allow(ParamExpires, ParamLargestObject),
	ContextPublish:     allow(ParamAuthorizationToken, ParamExpires, ParamLargestObject, ParamForward),
	ContextPublishOk: allow(ParamSubgroupDeliveryTimeout, ParamObjectDeliveryTimeout, ParamSubscriberPriority,
		ParamGroupOrder, ParamLocationFilter, ParamExpires, ParamForward, ParamNewGroupRequest),
	ContextFetch:   allow(ParamAuthorizationToken, ParamFillTimeout, ParamSubscriberPriority, ParamGroupOrder),
	ContextFetchOk: allow(),
	// Section 10.14: TRACK_STATUS is SUBSCRIBE without the delivery
	// parameters, and the only one left with TRACK_STATUS in its list is
	// AUTHORIZATION_TOKEN.
	ContextTrackStatus: allow(ParamAuthorizationToken),
	// LARGEST_OBJECT is the one parameter listing TRACK_STATUS_OK, but
	// Section 10.14 also has the publisher answer "with the same parameters
	// ... it would have set in a SUBSCRIBE_OK", which is EXPIRES as well. A
	// peer doing what that sentence says is not violating the protocol.
	ContextTrackStatusOk:        allow(ParamLargestObject, ParamExpires),
	ContextPublishNamespace:     allow(ParamAuthorizationToken),
	ContextPublishNamespaceOk:   allow(),
	ContextSubscribeNamespace:   allow(ParamAuthorizationToken),
	ContextSubscribeNamespaceOk: allow(),
	ContextSubscribeTracks:      allow(ParamAuthorizationToken, ParamForward),
	ContextSubscribeTracksOk:    allow(),
	ContextUpdateSubscription: union(anyUpdate18, allow(ParamSubscriberPriority, ParamLocationFilter,
		ParamForward, ParamNewGroupRequest)),
	ContextUpdateFetch:              union(anyUpdate18, allow(ParamSubscriberPriority)),
	ContextUpdatePublishNamespace:   anyUpdate18,
	ContextUpdateSubscribeNamespace: union(anyUpdate18, allow(ParamTrackNamespacePrefix)),
	ContextUpdateSubscribeTracks:    union(anyUpdate18, allow(ParamTrackNamespacePrefix)),
	ContextRequestUpdateOk:          allow(ParamExpires, ParamLargestObject),
}

// Draft-22 lists the allowed parameters twice, per parameter (Section 9.20)
// and per message (Section 9, from WG PR #1916). The two agree everywhere
// except SUBSCRIBE_TRACKS: Section 9.18 lists nine parameters, but Section
// 3.6.2 makes "any Parameter that can be specified on a Subscription" valid
// there too, and goes on to describe a SUBSCRIBE_TRACKS carrying
// LOCATION_FILTER and FILL_PARAMETERS, which Section 9.18 leaves out. The set
// here is the union, since this implementation refuses SUBSCRIBE_TRACKS with
// NOT_SUPPORTED anyway and has no business ending the session over it.
var (
	rangeFilters22 = allow(ParamSubgroupFilter, ParamObjectIDFilter, ParamPriorityFilter, ParamObjectPropertyFilter)
	subscribe22    = union(rangeFilters22, allow(ParamObjectDeliveryTimeout, ParamAuthorizationToken,
		ParamRendezvousTimeout, ParamSubgroupDeliveryTimeout, ParamForward, ParamSubscriberPriority,
		ParamLocationFilter, ParamGroupOrder, ParamFillParameters, ParamNewGroupRequest, ParamIncludeProperties))
)

var parameterScopes22 = map[ParameterContext]parameterSet{
	ContextSubscribe:   subscribe22,
	ContextSubscribeOk: allow(ParamExpires, ParamLargestObject),
	ContextPublish: allow(ParamObjectDeliveryTimeout, ParamAuthorizationToken, ParamSubgroupDeliveryTimeout,
		ParamExpires, ParamLargestObject, ParamForward, ParamSubscriberPriority, ParamLocationFilter,
		ParamGroupOrder),
	ContextPublishOk:          allow(ParamExpires),
	ContextPublishStateNotify: allow(ParamLargestObject, ParamForward, ParamLocationFilter),
	ContextFetch: union(rangeFilters22, allow(ParamAuthorizationToken, ParamFillTimeout,
		ParamSubscriberPriority, ParamLocationFilter, ParamGroupOrder, ParamIncludeProperties)),
	ContextFetchOk:     allow(),
	ContextTrackStatus: allow(ParamAuthorizationToken, ParamIncludeProperties),
	// As in draft-18: Section 9.3 lists LARGEST_OBJECT alone, while Section
	// 9.13 answers "with the same parameters ... it would have set in a
	// SUBSCRIBE_OK".
	ContextTrackStatusOk:        allow(ParamLargestObject, ParamExpires),
	ContextPublishNamespace:     allow(ParamAuthorizationToken),
	ContextPublishNamespaceOk:   allow(ParamExpires),
	ContextSubscribeNamespace:   allow(ParamAuthorizationToken),
	ContextSubscribeNamespaceOk: allow(ParamExpires),
	ContextSubscribeTracks: union(subscribe22, allow(ParamAuthorizationToken, ParamForward, ParamGroupOrder,
		ParamTrackPropertyFilter, ParamIncludeProperties)),
	ContextSubscribeTracksOk: allow(ParamExpires),
	ContextUpdateSubscription: union(rangeFilters22, allow(ParamObjectDeliveryTimeout, ParamAuthorizationToken,
		ParamSubgroupDeliveryTimeout, ParamForward, ParamSubscriberPriority, ParamLocationFilter,
		ParamFillParameters, ParamNewGroupRequest)),
	ContextUpdateFetch:              allow(ParamAuthorizationToken, ParamSubscriberPriority),
	ContextUpdatePublishNamespace:   allow(ParamAuthorizationToken),
	ContextUpdateSubscribeNamespace: allow(ParamAuthorizationToken, ParamTrackNamespacePrefix),
	ContextUpdateSubscribeTracks: allow(ParamAuthorizationToken, ParamForward, ParamTrackPropertyFilter,
		ParamTrackNamespacePrefix),
	ContextRequestUpdateOk: allow(ParamExpires, ParamLargestObject),
}

func scopesFor(version Version) (map[ParameterContext]parameterSet, error) {
	switch version {
	case Version18:
		return parameterScopes18, nil
	case Version22:
		return parameterScopes22, nil
	}
	return nil, unsupportedVersionError{version: version}
}

// CheckParameters reports a parameter in pp that version does not allow in
// ctx. The parameters inside FILL_PARAMETERS are a scope of their own, which
// the codec already checked against Table 7 while parsing.
func CheckParameters(version Version, ctx ParameterContext, pp Parameters) error {
	scopes, err := scopesFor(version)
	if err != nil {
		return err
	}
	allowed, ok := scopes[ctx]
	if !ok {
		return fmt.Errorf("no parameter scope for %v", ctx)
	}
	for _, p := range pp {
		if _, ok := allowed[p.Type]; !ok {
			return parameterNotAllowedError{parameterType: p.Type, context: ctx}
		}
	}
	return nil
}

// parameterNotAllowedError is a parameter outside the messages its definition
// lists.
type parameterNotAllowedError struct {
	parameterType uint64
	context       ParameterContext
}

func (e parameterNotAllowedError) Error() string {
	return fmt.Sprintf("%s is not allowed in %v", ParameterName(e.parameterType), e.context)
}
