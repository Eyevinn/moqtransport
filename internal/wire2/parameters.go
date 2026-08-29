package wire2

import (
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/Eyevinn/locmaf/vi64"
)

// Message Parameter types (draft-ietf-moq-transport-18, Section 15.7).
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
	ParamSubscriptionFilter      uint64 = 0x21
	ParamGroupOrder              uint64 = 0x22
	ParamNewGroupRequest         uint64 = 0x32
	ParamTrackNamespacePrefix    uint64 = 0x34
)

// paramEncoding is how a Message Parameter's value is serialized. Unlike a
// Key-Value-Pair, whose value encoding follows from the parity of its Type,
// a Message Parameter's encoding comes from its definition in the registry
// (draft-ietf-moq-transport-18, Section 10.2). This is why an unknown
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
)

type parameterDef struct {
	name       string
	encoding   paramEncoding
	repeatable bool
}

// parameterRegistry is the Message Parameter table. A Type absent from it is a
// PROTOCOL_VIOLATION on receipt and cannot be serialized on send.
//
// FILL_TIMEOUT and RENDEZVOUS_TIMEOUT are recorded as varints: Sections 10.2.5
// and 10.2.6 never name their encoding, unlike every other parameter, but both
// are durations in milliseconds and every other timeout parameter is a varint.
var parameterRegistry = map[uint64]parameterDef{
	ParamObjectDeliveryTimeout:   {"OBJECT_DELIVERY_TIMEOUT", encVarint, false},
	ParamAuthorizationToken:      {"AUTHORIZATION_TOKEN", encBytes, true},
	ParamRendezvousTimeout:       {"RENDEZVOUS_TIMEOUT", encVarint, false},
	ParamSubgroupDeliveryTimeout: {"SUBGROUP_DELIVERY_TIMEOUT", encVarint, false},
	ParamExpires:                 {"EXPIRES", encVarint, false},
	ParamLargestObject:           {"LARGEST_OBJECT", encLocation, false},
	ParamFillTimeout:             {"FILL_TIMEOUT", encVarint, false},
	ParamForward:                 {"FORWARD", encUint8, false},
	ParamSubscriberPriority:      {"SUBSCRIBER_PRIORITY", encUint8, false},
	ParamSubscriptionFilter:      {"SUBSCRIPTION_FILTER", encBytes, false},
	ParamGroupOrder:              {"GROUP_ORDER", encUint8, false},
	ParamNewGroupRequest:         {"NEW_GROUP_REQUEST", encVarint, false},
	ParamTrackNamespacePrefix:    {"TRACK_NAMESPACE_PREFIX", encNamespace, false},
}

// ParameterName returns the registered name of a Message Parameter type.
func ParameterName(typ uint64) string {
	if def, ok := parameterRegistry[typ]; ok {
		return def.name
	}
	return fmt.Sprintf("UNKNOWN(%#x)", typ)
}

// Parameter is one Message Parameter (Section 10.2, Figure 4). Which of the
// value fields carries the value follows from the Type's registered encoding:
// Number for encUint8 and encVarint, Location, Bytes, or Namespace.
type Parameter struct {
	Type      uint64
	Number    uint64
	Location  Location
	Bytes     []byte
	Namespace [][]byte
}

func (p Parameter) String() string {
	switch parameterRegistry[p.Type].encoding {
	case encLocation:
		return fmt.Sprintf("{%s: %v}", ParameterName(p.Type), p.Location)
	case encBytes:
		return fmt.Sprintf("{%s: %v}", ParameterName(p.Type), p.Bytes)
	case encNamespace:
		return fmt.Sprintf("{%s: %v}", ParameterName(p.Type), p.Namespace)
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

// Parameters is a Message Parameter block. Types are delta-encoded on the wire
// and MUST be serialized in ascending order, so the append side sorts a copy.
type Parameters []Parameter

// appendNum writes the block prefixed by its parameter count.
func (pp Parameters) appendNum(buf []byte) ([]byte, error) {
	sorted := make(Parameters, len(pp))
	copy(sorted, pp)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].Type < sorted[j].Type })

	buf = vi64.Append(buf, uint64(len(sorted)))
	var prevType uint64
	for _, p := range sorted {
		def, ok := parameterRegistry[p.Type]
		if !ok {
			return nil, unknownParameterError{parameterType: p.Type}
		}
		buf = vi64.Append(buf, p.Type-prevType)
		prevType = p.Type

		switch def.encoding {
		case encUint8:
			if p.Number > 255 {
				return nil, fmt.Errorf("%s value %d does not fit a uint8", def.name, p.Number)
			}
			buf = append(buf, byte(p.Number))
		case encVarint:
			buf = vi64.Append(buf, p.Number)
		case encLocation:
			buf = p.Location.append(buf)
		case encBytes:
			buf = vi64.Append(buf, uint64(len(p.Bytes)))
			buf = append(buf, p.Bytes...)
		case encNamespace:
			buf = vi64.Append(buf, uint64(len(p.Namespace)))
			for _, f := range p.Namespace {
				buf = vi64.Append(buf, uint64(len(f)))
				buf = append(buf, f...)
			}
		}
	}
	return buf, nil
}

// parseNum reads a count-prefixed block and returns the bytes consumed.
//
// An unrecognized Type is an error rather than something to skip: without the
// registry the parser cannot know how long the value is, so it cannot find the
// next parameter. A repeated Type is an error unless the registry marks the
// parameter repeatable.
func (pp *Parameters) parseNum(data []byte) (int, error) {
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

		def, ok := parameterRegistry[p.Type]
		if !ok {
			return parsed, unknownParameterError{parameterType: p.Type}
		}
		if _, dup := seen[p.Type]; dup && !def.repeatable {
			return parsed, duplicateParameterError{parameterType: p.Type}
		}
		seen[p.Type] = struct{}{}

		n, err = p.parseValue(data[parsed:], def.encoding)
		parsed += n
		if err != nil {
			return parsed, err
		}
		list = append(list, p)
	}
	*pp = list
	return parsed, nil
}

func (p *Parameter) parseValue(data []byte, encoding paramEncoding) (int, error) {
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
