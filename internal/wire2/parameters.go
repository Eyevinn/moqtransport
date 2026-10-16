package wire2

import (
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/Eyevinn/locmaf/vi64"
)

// Message Parameter types (draft-ietf-moq-transport-18 Section 15.7,
// draft-22 Section 16.7). The two registries share every codepoint they have
// in common; draft-22 adds the fill and range filter parameters and
// INCLUDE_PROPERTIES.
const (
	ParamObjectDeliveryTimeout   uint64 = 0x02
	ParamAuthorizationToken      uint64 = 0x03
	ParamRendezvousTimeout       uint64 = 0x04
	ParamSubgroupDeliveryTimeout uint64 = 0x06
	ParamExpires                 uint64 = 0x08
	ParamLargestObject           uint64 = 0x09
	ParamFillTimeout             uint64 = 0x0A
	ParamForward                 uint64 = 0x10
	ParamSubscriberPriority      uint64 = 0x20
	// ParamLocationFilter is LOCATION_FILTER in draft-22 and
	// SUBSCRIPTION_FILTER in draft-18: same codepoint, same purpose, different
	// encoding. See LocationFilter.
	ParamLocationFilter       uint64 = 0x21
	ParamGroupOrder           uint64 = 0x22
	ParamFillParameters       uint64 = 0x23
	ParamSubgroupFilter       uint64 = 0x25
	ParamObjectIDFilter       uint64 = 0x26
	ParamPriorityFilter       uint64 = 0x27
	ParamObjectPropertyFilter uint64 = 0x28
	ParamTrackPropertyFilter  uint64 = 0x29
	ParamNewGroupRequest      uint64 = 0x32
	ParamTrackNamespacePrefix uint64 = 0x34
	ParamIncludeProperties    uint64 = 0x35
)

// paramEncoding is how a Message Parameter's value is serialized. Unlike a
// Key-Value-Pair, whose value encoding follows from the parity of its Type,
// a Message Parameter's encoding comes from its definition in the registry
// (draft-18 Section 10.2, draft-22 Section 9.20). This is why an unknown
// parameter cannot be skipped, and so why receiving one is fatal and why the
// block is bounded by a count rather than a length.
type paramEncoding uint8

const (
	// A single byte, 0-255.
	encUint8 paramEncoding = iota
	// A vi64.
	encVarint
	// Two consecutive vi64s: Group then Object.
	encLocation
	// A vi64 length followed by that many bytes.
	encBytes
	// A Track Namespace: a count of fields, each length-prefixed
	// (Section 2.4.1). Section 10.2 does not list this among its four value
	// encodings, but TRACK_NAMESPACE_PREFIX uses it.
	encNamespace
	// A location filter, whose wire form is per draft: length-prefixed with a
	// Filter Type of 1-4 in draft-18, unprefixed with a Location Filter Type
	// of 0-5 selecting the fields that follow in draft-22.
	encLocationFilter
	// A vi64 length, then a nested count-prefixed parameter block with a
	// registry of its own. FILL_PARAMETERS is the one parameter that uses it.
	encParameters
)

type parameterDef struct {
	name       string
	encoding   paramEncoding
	repeatable bool
}

// parameterRegistry is one draft's Message Parameter table. A Type absent
// from it is a PROTOCOL_VIOLATION on receipt and cannot be serialized on send.
type parameterRegistry map[uint64]parameterDef

// parameters18 is draft-18's table.
//
// FILL_TIMEOUT and RENDEZVOUS_TIMEOUT are recorded as varints: Sections 10.2.5
// and 10.2.6 never name their encoding, unlike every other parameter, but both
// are durations in milliseconds and every other timeout parameter is a varint.
// Draft-22 confirms neither and keeps both.
var parameters18 = parameterRegistry{
	ParamObjectDeliveryTimeout:   {"OBJECT_DELIVERY_TIMEOUT", encVarint, false},
	ParamAuthorizationToken:      {"AUTHORIZATION_TOKEN", encBytes, true},
	ParamRendezvousTimeout:       {"RENDEZVOUS_TIMEOUT", encVarint, false},
	ParamSubgroupDeliveryTimeout: {"SUBGROUP_DELIVERY_TIMEOUT", encVarint, false},
	ParamExpires:                 {"EXPIRES", encVarint, false},
	ParamLargestObject:           {"LARGEST_OBJECT", encLocation, false},
	ParamFillTimeout:             {"FILL_TIMEOUT", encVarint, false},
	ParamForward:                 {"FORWARD", encUint8, false},
	ParamSubscriberPriority:      {"SUBSCRIBER_PRIORITY", encUint8, false},
	ParamLocationFilter:          {"SUBSCRIPTION_FILTER", encLocationFilter, false},
	ParamGroupOrder:              {"GROUP_ORDER", encUint8, false},
	ParamNewGroupRequest:         {"NEW_GROUP_REQUEST", encVarint, false},
	ParamTrackNamespacePrefix:    {"TRACK_NAMESPACE_PREFIX", encNamespace, false},
}

// parameters22 is draft-22's table.
//
// LOCATION_FILTER carries no Length even though the type is odd: Section 9.20
// gives each parameter its own encoding rather than the Key-Value-Pair parity
// rule, and its definition has the Location Filter Type select the fields.
// That is also how moq-dev, Meta's relay and moqtail frame it (WG issue
// #1991). FILL_PARAMETERS, by the same issue, is length-prefixed and its value
// starts with the Number of Parameters.
//
// The five range filters are stored as their raw value bytes: a malformed one
// is an INVALID_FILTER request error rather than a session error, so its
// structure is checked by the request handler, not here. They may repeat.
var parameters22 = parameterRegistry{
	ParamObjectDeliveryTimeout:   {"OBJECT_DELIVERY_TIMEOUT", encVarint, false},
	ParamAuthorizationToken:      {"AUTHORIZATION_TOKEN", encBytes, true},
	ParamRendezvousTimeout:       {"RENDEZVOUS_TIMEOUT", encVarint, false},
	ParamSubgroupDeliveryTimeout: {"SUBGROUP_DELIVERY_TIMEOUT", encVarint, false},
	ParamExpires:                 {"EXPIRES", encVarint, false},
	ParamLargestObject:           {"LARGEST_OBJECT", encLocation, false},
	ParamFillTimeout:             {"FILL_TIMEOUT", encVarint, false},
	ParamForward:                 {"FORWARD", encUint8, false},
	ParamSubscriberPriority:      {"SUBSCRIBER_PRIORITY", encUint8, false},
	ParamLocationFilter:          {"LOCATION_FILTER", encLocationFilter, false},
	ParamGroupOrder:              {"GROUP_ORDER", encUint8, false},
	ParamFillParameters:          {"FILL_PARAMETERS", encParameters, false},
	ParamSubgroupFilter:          {"SUBGROUP_FILTER", encBytes, true},
	ParamObjectIDFilter:          {"OBJECTID_FILTER", encBytes, true},
	ParamPriorityFilter:          {"PRIORITY_FILTER", encBytes, true},
	ParamObjectPropertyFilter:    {"OBJECT_PROPERTY_FILTER", encBytes, true},
	ParamTrackPropertyFilter:     {"TRACK_PROPERTY_FILTER", encBytes, true},
	ParamNewGroupRequest:         {"NEW_GROUP_REQUEST", encVarint, false},
	ParamTrackNamespacePrefix:    {"TRACK_NAMESPACE_PREFIX", encNamespace, false},
	ParamIncludeProperties:       {"INCLUDE_PROPERTIES", encUint8, false},
}

// fillParameters22 is the registry inside FILL_PARAMETERS (draft-22 Table 7).
// It is a separate scope: a type may appear both there and in the enclosing
// message, and anything not listed is a PROTOCOL_VIOLATION even if the
// enclosing message could carry it.
var fillParameters22 = parameterRegistry{
	ParamFillTimeout:          parameters22[ParamFillTimeout],
	ParamSubscriberPriority:   parameters22[ParamSubscriberPriority],
	ParamLocationFilter:       parameters22[ParamLocationFilter],
	ParamGroupOrder:           parameters22[ParamGroupOrder],
	ParamSubgroupFilter:       parameters22[ParamSubgroupFilter],
	ParamObjectIDFilter:       parameters22[ParamObjectIDFilter],
	ParamPriorityFilter:       parameters22[ParamPriorityFilter],
	ParamObjectPropertyFilter: parameters22[ParamObjectPropertyFilter],
}

// registryFor returns version's top-level Message Parameter table.
func registryFor(version Version) (parameterRegistry, error) {
	switch version {
	case Version18:
		return parameters18, nil
	case Version22:
		return parameters22, nil
	}
	return nil, unsupportedVersionError{version: version}
}

// ParameterName returns the registered name of a Message Parameter type,
// using draft-22's names: 0x21 is LOCATION_FILTER, which draft-18 called
// SUBSCRIPTION_FILTER.
func ParameterName(typ uint64) string {
	if def, ok := parameters22[typ]; ok {
		return def.name
	}
	return fmt.Sprintf("UNKNOWN(%#x)", typ)
}

// Parameter is one Message Parameter (draft-18 Section 10.2, draft-22 Section
// 9.20). Which of the value fields carries the value follows from the Type's
// registered encoding: Number for encUint8 and encVarint, Location, Bytes,
// Namespace, Filter for LOCATION_FILTER, or Params for FILL_PARAMETERS.
type Parameter struct {
	Type      uint64
	Number    uint64
	Location  Location
	Bytes     []byte
	Namespace [][]byte
	Filter    LocationFilter
	Params    Parameters
}

func (p Parameter) String() string {
	def := parameters22[p.Type]
	switch def.encoding {
	case encLocation:
		return fmt.Sprintf("{%s: %v}", ParameterName(p.Type), p.Location)
	case encBytes:
		return fmt.Sprintf("{%s: %v}", ParameterName(p.Type), p.Bytes)
	case encNamespace:
		return fmt.Sprintf("{%s: %v}", ParameterName(p.Type), p.Namespace)
	case encLocationFilter:
		return fmt.Sprintf("{%s: %v}", ParameterName(p.Type), p.Filter)
	case encParameters:
		return fmt.Sprintf("{%s: %v}", ParameterName(p.Type), p.Params)
	}
	return fmt.Sprintf("{%s: %v}", ParameterName(p.Type), p.Number)
}

// Uint8Parameter and the constructors below build a Parameter of the shape the
// registry expects. They do not check the Type against the registry; the codec
// does that, so a mismatch surfaces on the first attempt to serialize.
func Uint8Parameter(typ uint64, v uint8) Parameter {
	return Parameter{Type: typ, Number: uint64(v)}
}

func VarintParameter(typ uint64, v uint64) Parameter {
	return Parameter{Type: typ, Number: v}
}

func LocationParameter(typ uint64, v Location) Parameter {
	return Parameter{Type: typ, Location: v}
}

func BytesParameter(typ uint64, v []byte) Parameter {
	return Parameter{Type: typ, Bytes: v}
}

func NamespaceParameter(typ uint64, v [][]byte) Parameter {
	return Parameter{Type: typ, Namespace: v}
}

// LocationFilterParameter builds the LOCATION_FILTER parameter, which draft-18
// calls SUBSCRIPTION_FILTER. The filter is encoded for whichever draft the
// message is sent in, and a filter that draft cannot express fails then.
func LocationFilterParameter(f LocationFilter) Parameter {
	return Parameter{Type: ParamLocationFilter, Filter: f}
}

// FillParametersParameter builds FILL_PARAMETERS, whose presence on a
// subscription asks for a fill fetch stream (draft-22 Section 3.4). inner may
// be empty: the fill then inherits everything from the subscription.
func FillParametersParameter(inner Parameters) Parameter {
	if inner == nil {
		inner = Parameters{}
	}
	return Parameter{Type: ParamFillParameters, Params: inner}
}

// Parameters is a Message Parameter block. Types are delta-encoded on the wire
// and MUST be serialized in ascending order, so the append side sorts a copy.
type Parameters []Parameter

// appendNum writes the block, prefixed by its parameter count, in version's
// encoding.
func (pp Parameters) appendNum(buf []byte, version Version) ([]byte, error) {
	registry, err := registryFor(version)
	if err != nil {
		return nil, err
	}
	return pp.appendWith(buf, registry, version)
}

func (pp Parameters) appendWith(buf []byte, registry parameterRegistry, version Version) ([]byte, error) {
	sorted := make(Parameters, len(pp))
	copy(sorted, pp)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].Type < sorted[j].Type })

	buf = vi64.Append(buf, uint64(len(sorted)))
	var prevType uint64
	for _, p := range sorted {
		def, ok := registry[p.Type]
		if !ok {
			return nil, unknownParameterError{parameterType: p.Type}
		}
		buf = vi64.Append(buf, p.Type-prevType)
		prevType = p.Type

		var err error
		if buf, err = p.appendValue(buf, def, version); err != nil {
			return nil, err
		}
	}
	return buf, nil
}

func (p Parameter) appendValue(buf []byte, def parameterDef, version Version) ([]byte, error) {
	switch def.encoding {
	case encUint8:
		if p.Number > 255 {
			return nil, fmt.Errorf("%s value %d does not fit a uint8", def.name, p.Number)
		}
		return append(buf, byte(p.Number)), nil
	case encVarint:
		return vi64.Append(buf, p.Number), nil
	case encLocation:
		return p.Location.append(buf), nil
	case encBytes:
		buf = vi64.Append(buf, uint64(len(p.Bytes)))
		return append(buf, p.Bytes...), nil
	case encNamespace:
		buf = vi64.Append(buf, uint64(len(p.Namespace)))
		for _, f := range p.Namespace {
			buf = vi64.Append(buf, uint64(len(f)))
			buf = append(buf, f...)
		}
		return buf, nil
	case encLocationFilter:
		return p.Filter.appendValue(buf, version)
	case encParameters:
		// The nested block is written separately so that its length, which
		// precedes it, is known.
		inner, err := p.Params.appendWith(nil, fillParameters22, version)
		if err != nil {
			return nil, err
		}
		buf = vi64.Append(buf, uint64(len(inner)))
		return append(buf, inner...), nil
	}
	return nil, fmt.Errorf("unhandled parameter encoding %v", def.encoding)
}

// parseNum reads a count-prefixed block in version's encoding and returns the
// bytes consumed.
//
// An unrecognized Type is an error rather than something to skip: without the
// registry the parser cannot know how long the value is, so it cannot find the
// next parameter. A repeated Type is an error unless the registry marks the
// parameter repeatable.
func (pp *Parameters) parseNum(data []byte, version Version) (int, error) {
	registry, err := registryFor(version)
	if err != nil {
		return 0, err
	}
	return pp.parseWith(data, registry, version)
}

func (pp *Parameters) parseWith(data []byte, registry parameterRegistry, version Version) (int, error) {
	count, parsed, err := vi64.Parse(data)
	if err != nil {
		return parsed, err
	}
	// Every parameter costs at least one byte, so the remaining length bounds
	// the count and with it the allocation.
	if count > uint64(len(data)-parsed) {
		return parsed, io.ErrUnexpectedEOF
	}

	list := make(Parameters, 0, count)
	seen := make(map[uint64]struct{}, count)
	var prevType uint64
	for range count {
		delta, n, err := vi64.Parse(data[parsed:])
		parsed += n
		if err != nil {
			return parsed, err
		}
		if delta > maxUint64-prevType {
			return parsed, errDeltaTypeOverflow
		}
		p := Parameter{Type: prevType + delta}
		prevType = p.Type

		def, ok := registry[p.Type]
		if !ok {
			return parsed, unknownParameterError{parameterType: p.Type}
		}
		if _, dup := seen[p.Type]; dup && !def.repeatable {
			return parsed, duplicateParameterError{parameterType: p.Type}
		}
		seen[p.Type] = struct{}{}

		n, err = p.parseValue(data[parsed:], def.encoding, version)
		parsed += n
		if err != nil {
			return parsed, err
		}
		list = append(list, p)
	}
	*pp = list
	return parsed, nil
}

func (p *Parameter) parseValue(data []byte, encoding paramEncoding, version Version) (int, error) {
	switch encoding {
	case encUint8:
		if len(data) < 1 {
			return 0, io.ErrUnexpectedEOF
		}
		p.Number = uint64(data[0])
		return 1, nil

	case encVarint:
		v, n, err := vi64.Parse(data)
		p.Number = v
		return n, err

	case encLocation:
		return p.Location.parse(data)

	case encBytes:
		value, n, err := parseByteString(data, maxValueLength)
		if err != nil {
			return n, err
		}
		p.Bytes = value
		return n, nil

	case encNamespace:
		count, parsed, err := vi64.Parse(data)
		if err != nil {
			return parsed, err
		}
		if count > maxNamespaceFields {
			return parsed, errTooManyFields
		}
		if count > uint64(len(data)-parsed) {
			return parsed, io.ErrUnexpectedEOF
		}
		p.Namespace = make([][]byte, 0, count)
		for range count {
			field, n, err := parseByteString(data[parsed:], 0)
			parsed += n
			if err != nil {
				return parsed, err
			}
			p.Namespace = append(p.Namespace, field)
		}
		return parsed, nil

	case encLocationFilter:
		return p.Filter.parseValue(data, version)

	case encParameters:
		value, n, err := parseByteString(data, maxValueLength)
		if err != nil {
			return n, err
		}
		consumed, err := p.Params.parseWith(value, fillParameters22, version)
		if err != nil {
			return n, err
		}
		// The Length bounds the nested block exactly: bytes left over are a
		// framing disagreement, like a control message's trailing bytes.
		if consumed != len(value) {
			return n, errTrailingBytes
		}
		return n, nil
	}
	return 0, fmt.Errorf("unhandled parameter encoding %v", encoding)
}

// Get returns the first parameter with the given Type.
func (pp Parameters) Get(typ uint64) (Parameter, bool) {
	for _, p := range pp {
		if p.Type == typ {
			return p, true
		}
	}
	return Parameter{}, false
}

func (pp Parameters) String() string {
	parts := make([]string, 0, len(pp))
	for _, p := range pp {
		parts = append(parts, p.String())
	}
	return "[" + strings.Join(parts, ", ") + "]"
}
