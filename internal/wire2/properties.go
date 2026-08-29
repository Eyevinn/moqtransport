package wire2

import "fmt"

// Property types (draft-ietf-moq-transport-18, Section 15.8).
//
// Properties are Key-Value-Pairs describing a Track or an Object, and unlike
// Message Parameters they are relay-visible and forwarded. A relay that does
// not understand a Property MUST forward it unchanged and MUST cache it with
// the Track or Object, so an unknown type is neither an error nor something to
// drop -- with the one exception of Mandatory Track Properties.
const (
	PropertyObjectDeliveryTimeout      uint64 = 0x02
	PropertyMaxCacheDuration           uint64 = 0x04
	PropertySubgroupDeliveryTimeout    uint64 = 0x06
	PropertyImmutableProperties        uint64 = 0x0B
	PropertyDefaultPublisherPriority   uint64 = 0x0E
	PropertyDefaultPublisherGroupOrder uint64 = 0x22
	PropertyDynamicGroups              uint64 = 0x30
	PropertyPriorGroupIDGap            uint64 = 0x3C
	PropertyPriorObjectIDGap           uint64 = 0x3E
)

// PropertyScope is where a Property may appear.
type PropertyScope uint8

const (
	PropertyScopeTrack PropertyScope = 1 << iota
	PropertyScopeObject
)

var propertyRegistry = map[uint64]struct {
	name  string
	scope PropertyScope
}{
	PropertyObjectDeliveryTimeout:      {"OBJECT_DELIVERY_TIMEOUT", PropertyScopeTrack},
	PropertyMaxCacheDuration:           {"MAX_CACHE_DURATION", PropertyScopeTrack},
	PropertySubgroupDeliveryTimeout:    {"SUBGROUP_DELIVERY_TIMEOUT", PropertyScopeTrack},
	PropertyImmutableProperties:        {"IMMUTABLE_PROPERTIES", PropertyScopeTrack | PropertyScopeObject},
	PropertyDefaultPublisherPriority:   {"DEFAULT_PUBLISHER_PRIORITY", PropertyScopeTrack},
	PropertyDefaultPublisherGroupOrder: {"DEFAULT_PUBLISHER_GROUP_ORDER", PropertyScopeTrack},
	PropertyDynamicGroups:              {"DYNAMIC_GROUPS", PropertyScopeTrack},
	PropertyPriorGroupIDGap:            {"PRIOR_GROUP_ID_GAP", PropertyScopeObject},
	PropertyPriorObjectIDGap:           {"PRIOR_OBJECT_ID_GAP", PropertyScopeObject},
}

// PropertyName returns the registered name of a Property type.
func PropertyName(typ uint64) string {
	if def, ok := propertyRegistry[typ]; ok {
		return def.name
	}
	if IsMandatoryTrackProperty(typ) {
		return fmt.Sprintf("MANDATORY_TRACK_PROPERTY(%#x)", typ)
	}
	if IsApplicationProperty(typ) {
		return fmt.Sprintf("APPLICATION_PROPERTY(%#x)", typ)
	}
	return fmt.Sprintf("UNKNOWN_PROPERTY(%#x)", typ)
}

// Mandatory Track Property range (Section 2.5.1). A track carrying one of
// these that the endpoint does not understand MUST NOT be processed or
// forwarded; the endpoint answers UNSUPPORTED_EXTENSION instead.
const (
	mandatoryTrackPropertyMin = 0x4000
	mandatoryTrackPropertyMax = 0x7FFF
)

// IsMandatoryTrackProperty reports whether typ is in the range reserved for
// properties an endpoint must understand to serve the track at all.
func IsMandatoryTrackProperty(typ uint64) bool {
	return typ >= mandatoryTrackPropertyMin && typ <= mandatoryTrackPropertyMax
}

// IsApplicationProperty reports whether typ is in a range IANA will never
// allocate, reserved for application-defined metadata (Section 2.5). A relay
// forwards these unchanged and MUST NOT interpret them: the same codepoint may
// mean different things to different applications.
func IsApplicationProperty(typ uint64) bool {
	return (typ >= 0x38 && typ <= 0x3F) || (typ >= 0x3800 && typ <= 0x3FFF)
}

// PropertyScopeOf returns the scope a registered Property may appear in.
func PropertyScopeOf(typ uint64) (PropertyScope, bool) {
	def, ok := propertyRegistry[typ]
	if !ok {
		return 0, false
	}
	return def.scope, true
}

// ValidateObjectProperties checks a block of Object Properties. A Mandatory
// Track Property received as an Object Property makes the track malformed
// (Section 2.5.1); a registered Track-only property in Object scope is out of
// the scope its definition allows. Unknown types pass: they are forwarded.
func ValidateObjectProperties(pp KVPList) error {
	for _, p := range pp {
		if IsMandatoryTrackProperty(p.Type) {
			return propertyScopeError{propertyType: p.Type, scope: PropertyScopeObject}
		}
		if scope, ok := PropertyScopeOf(p.Type); ok && scope&PropertyScopeObject == 0 {
			return propertyScopeError{propertyType: p.Type, scope: PropertyScopeObject}
		}
	}
	return nil
}

// ValidateTrackProperties checks a block of Track Properties. Unknown types
// pass, including unknown Mandatory Track Properties: those are not a parse
// error but a signal to reject the track with UNSUPPORTED_EXTENSION, which is
// a decision for the handler that knows the request. Use
// UnsupportedMandatoryProperties to find them.
func ValidateTrackProperties(pp KVPList) error {
	for _, p := range pp {
		if scope, ok := PropertyScopeOf(p.Type); ok && scope&PropertyScopeTrack == 0 {
			return propertyScopeError{propertyType: p.Type, scope: PropertyScopeTrack}
		}
	}
	return nil
}

// UnsupportedMandatoryProperties returns the Mandatory Track Property types in
// pp that this implementation does not understand. A non-empty result means
// the track cannot be processed or forwarded.
func UnsupportedMandatoryProperties(pp KVPList) []uint64 {
	var unsupported []uint64
	for _, p := range pp {
		if IsMandatoryTrackProperty(p.Type) {
			if _, known := propertyRegistry[p.Type]; !known {
				unsupported = append(unsupported, p.Type)
			}
		}
	}
	return unsupported
}
