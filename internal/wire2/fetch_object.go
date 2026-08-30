package wire2

import (
	"fmt"

	"github.com/Eyevinn/locmaf/vi64"
)

// GroupOrder is the order Groups are delivered in
// (draft-ietf-moq-transport-18, Section 10.2.8). It decides how a FETCH
// response's Group ID Deltas are applied, so a fetch reader cannot decode
// without it.
type GroupOrder uint8

const (
	// GroupOrderAscending is the default for a FETCH when the parameter is
	// omitted.
	GroupOrderAscending  GroupOrder = 0x1
	GroupOrderDescending GroupOrder = 0x2
)

func (o GroupOrder) String() string {
	switch o {
	case GroupOrderAscending:
		return "ascending"
	case GroupOrderDescending:
		return "descending"
	}
	return fmt.Sprintf("invalid group order: %#x", uint8(o))
}

// Valid reports whether o is one of the two defined orders. Anything else is a
// PROTOCOL_VIOLATION.
func (o GroupOrder) Valid() bool {
	return o == GroupOrderAscending || o == GroupOrderDescending
}

// Serialization Flags of a FETCH response Object (Section 11.4.4.1). Below 128
// the value is a bitfield; at or above it, only the End of Range indicators in
// Table 7 are defined and everything else is a PROTOCOL_VIOLATION.
const (
	fetchSubgroupModeMask     = 0x03
	fetchFlagObjectIDDelta    = 0x04
	fetchFlagGroupIDDelta     = 0x08
	fetchFlagPriority         = 0x10
	fetchFlagProperties       = 0x20
	fetchFlagDatagram         = 0x40
	fetchSerializationFlagMax = 0x7F
)

// FetchSubgroupMode is the two-bit Subgroup encoding in the Serialization
// Flags. Unlike the subgroup stream's equivalent it has no reserved value: all
// four are defined.
type FetchSubgroupMode uint8

const (
	// FetchSubgroupZero means the Subgroup ID is zero.
	FetchSubgroupZero FetchSubgroupMode = 0x00
	// FetchSubgroupPrior means the Subgroup ID is the prior Object's.
	FetchSubgroupPrior FetchSubgroupMode = 0x01
	// FetchSubgroupPriorPlusOne means the Subgroup ID is the prior Object's
	// plus one.
	FetchSubgroupPriorPlusOne FetchSubgroupMode = 0x02
	// FetchSubgroupExplicit means the Subgroup ID field is present.
	FetchSubgroupExplicit FetchSubgroupMode = 0x03
)

// EndOfRange is what an End of Range indicator says about the Objects it
// covers: everything between the previous serialized Object and this Location,
// inclusive.
type EndOfRange uint64

const (
	// EndOfRangeNone means the record is an ordinary Object.
	EndOfRangeNone EndOfRange = 0
	// EndOfRangeNonExistent means the covered Objects are known not to exist.
	EndOfRangeNonExistent EndOfRange = 0x8C
	// EndOfRangeUnknown means the publisher cannot determine their status.
	EndOfRangeUnknown EndOfRange = 0x10C
)

func (e EndOfRange) String() string {
	switch e {
	case EndOfRangeNone:
		return "none"
	case EndOfRangeNonExistent:
		return "END_OF_NON_EXISTENT_RANGE"
	case EndOfRangeUnknown:
		return "END_OF_UNKNOWN_RANGE"
	}
	return fmt.Sprintf("invalid end of range: %#x", uint64(e))
}

// FetchObject is one record on a FETCH data stream: either an Object, or an
// End of Range indicator standing in for a run of Objects that were not
// serialized (Section 11.4.4).
//
// The wire format encodes almost every field as "same as the prior Object" or
// a delta against it, so these are the resolved values; FetchReader and
// FetchWriter hold the prior-Object state that resolving needs.
type FetchObject struct {
	GroupID    uint64
	SubgroupID uint64
	ObjectID   uint64
	Priority   uint8
	Properties KVPList
	Payload    []byte

	// EndOfRange is EndOfRangeNone for an ordinary Object. Otherwise this
	// record carries only a Location, and every Object from the previous one
	// up to and including it either does not exist or has unknown status.
	EndOfRange EndOfRange

	// Datagram records that the Object's Forwarding Preference is Datagram, so
	// it has no Subgroup ID. A FETCH response carries Objects of both
	// preferences, and this is how it distinguishes them.
	Datagram bool
}

// FetchReader reads Objects from a stream opened with FETCH_HEADER.
//
// The delta encoding refers to two different "prior Objects": Group ID and
// Object ID advance past an End of Range indicator, while Subgroup ID and
// Priority carry over from the last *actual* Object, skipping indicators
// entirely (Section 11.4.4.2). The reader tracks both.
type FetchReader struct {
	// GroupOrder decides the direction a Group ID Delta moves in. A FETCH that
	// omitted the parameter is Ascending.
	GroupOrder GroupOrder

	r byteReader

	priorGroupID  uint64
	priorObjectID uint64
	havePrior     bool

	priorSubgroupID uint64
	priorPriority   uint8
	havePriorObject bool
}

// NewFetchReader returns a reader for the Objects that follow a FETCH_HEADER
// on r, delivered in the given Group Order.
func NewFetchReader(r byteReader, order GroupOrder) *FetchReader {
	if !order.Valid() {
		order = GroupOrderAscending
	}
	return &FetchReader{GroupOrder: order, r: r}
}

// Next reads the next record. It returns io.EOF, and only io.EOF, when the
// stream ends cleanly between records.
func (f *FetchReader) Next() (*FetchObject, error) {
	flags, err := vi64.Read(f.r)
	if err != nil {
		return nil, err
	}

	if flags > fetchSerializationFlagMax {
		return f.readEndOfRange(EndOfRange(flags))
	}

	obj := &FetchObject{
		Datagram: flags&fetchFlagDatagram != 0,
	}

	// The first record must carry both deltas, since there is no prior Object
	// for anything else to refer to.
	hasGroupDelta := flags&fetchFlagGroupIDDelta != 0
	hasObjectDelta := flags&fetchFlagObjectIDDelta != 0
	if !f.havePrior && (!hasGroupDelta || !hasObjectDelta) {
		return nil, errFetchObjectNeedsPrior
	}

	if hasGroupDelta {
		delta, err := vi64.Read(f.r)
		if err != nil {
			return nil, unexpectedEOF(err)
		}
		if obj.GroupID, err = f.resolveGroupID(delta); err != nil {
			return nil, err
		}
	} else {
		obj.GroupID = f.priorGroupID
	}

	// The Subgroup ID field sits between the two deltas on the wire even
	// though the Object ID depends on the Group ID Delta.
	mode := FetchSubgroupMode(flags & fetchSubgroupModeMask)
	if obj.Datagram {
		// A Datagram Object has no Subgroup ID and the mode bits are to be
		// ignored, so no field is read whatever they say.
		mode = FetchSubgroupZero
	}
	switch mode {
	case FetchSubgroupZero:
		obj.SubgroupID = 0
	case FetchSubgroupPrior, FetchSubgroupPriorPlusOne:
		if !f.havePriorObject {
			return nil, errFetchObjectNeedsPrior
		}
		obj.SubgroupID = f.priorSubgroupID
		if mode == FetchSubgroupPriorPlusOne {
			if obj.SubgroupID == maxUint64 {
				return nil, errSubgroupIDOverflow
			}
			obj.SubgroupID++
		}
	case FetchSubgroupExplicit:
		if obj.SubgroupID, err = vi64.Read(f.r); err != nil {
			return nil, unexpectedEOF(err)
		}
	}

	if hasObjectDelta {
		delta, err := vi64.Read(f.r)
		if err != nil {
			return nil, unexpectedEOF(err)
		}
		// A new Group restarts numbering, so the delta is the absolute Object
		// ID; within a Group it is added to the prior ID. Note that it is
		// added as-is, unlike a subgroup stream's delta, which is plus one.
		if hasGroupDelta {
			obj.ObjectID = delta
		} else if obj.ObjectID, err = addWithoutOverflow(f.priorObjectID, delta); err != nil {
			return nil, err
		}
	} else if obj.ObjectID, err = addWithoutOverflow(f.priorObjectID, 1); err != nil {
		return nil, err
	}

	if flags&fetchFlagPriority != 0 {
		priority, err := f.r.ReadByte()
		if err != nil {
			return nil, unexpectedEOF(err)
		}
		obj.Priority = priority
	} else {
		if !f.havePriorObject {
			return nil, errFetchObjectNeedsPrior
		}
		obj.Priority = f.priorPriority
	}

	if flags&fetchFlagProperties != 0 {
		if obj.Properties, err = readProperties(f.r); err != nil {
			return nil, err
		}
	}

	payloadLen, err := vi64.Read(f.r)
	if err != nil {
		return nil, unexpectedEOF(err)
	}
	if payloadLen > 0 {
		if obj.Payload, err = readPayload(f.r, payloadLen); err != nil {
			return nil, err
		}
	}

	f.remember(obj)
	return obj, nil
}

// readEndOfRange reads the Location of an End of Range indicator: its
// Serialization Flags are followed by a Group ID and an Object ID, and by
// nothing else.
//
// The two IDs are absolute, not deltas. Section 11.4.4.2 names them "the Group
// ID and Object ID fields", where Section 11.4.4.1 is careful to say "Group ID
// Delta" everywhere -- and the delta reading would make the common case, an
// indicator covering Objects in the Group it follows, inexpressible: an
// ascending Group ID Delta always advances at least one Group. Its wording
// predates delta encoding, which arrived in draft-18 without touching it, so
// it still means what it did in draft-17 when those fields were absolute.
//
// Object Payload Length is not written. Section 11.4.4.2 does not name it
// among the fields it removes and Figure 27 has it unbracketed, which reads
// like it stays -- but moxygen, quiche, moqtail, moq-go and aiomoqt all resume
// at the next Object's Serialization Flags instead, so a zero length here
// desynchronises every one of them. moxygen's
// MoQFramerV18Test.FetchEndOfRangeSetsPriorGroupAndObject puts an ordinary
// Object immediately after an indicator and is the clearest statement of it.
func (f *FetchReader) readEndOfRange(kind EndOfRange) (*FetchObject, error) {
	switch kind {
	case EndOfRangeNonExistent, EndOfRangeUnknown:
	default:
		return nil, errInvalidSerializationFlags
	}

	obj := &FetchObject{EndOfRange: kind}
	var err error
	if obj.GroupID, err = vi64.Read(f.r); err != nil {
		return nil, unexpectedEOF(err)
	}
	if obj.ObjectID, err = vi64.Read(f.r); err != nil {
		return nil, unexpectedEOF(err)
	}

	// Only the Location advances: Subgroup ID and Priority still refer to the
	// last real Object.
	f.priorGroupID = obj.GroupID
	f.priorObjectID = obj.ObjectID
	f.havePrior = true
	return obj, nil
}

// resolveGroupID applies a Group ID Delta in the direction the Group Order
// says, or returns the delta itself when there is no prior Object.
func (f *FetchReader) resolveGroupID(delta uint64) (uint64, error) {
	if !f.havePrior {
		return delta, nil
	}
	if f.GroupOrder == GroupOrderDescending {
		if delta >= f.priorGroupID {
			return 0, errGroupIDOverflow
		}
		return f.priorGroupID - delta - 1, nil
	}
	return addObjectID(f.priorGroupID, delta)
}

func (f *FetchReader) remember(obj *FetchObject) {
	f.priorGroupID = obj.GroupID
	f.priorObjectID = obj.ObjectID
	f.havePrior = true
	f.priorSubgroupID = obj.SubgroupID
	f.priorPriority = obj.Priority
	f.havePriorObject = true
}

// FetchWriter serializes records onto a FETCH data stream.
//
// It picks between the two encodings that actually matter -- "same Group,
// Object ID relative" and "new Group, Object ID absolute" -- and otherwise
// writes fields explicitly rather than hunting for the shortest form. The
// compact alternatives (inherit the prior Priority, derive the Subgroup ID)
// buy a byte or two per Object at the cost of a second, subtly different copy
// of the prior-Object state machine, and the reader has to handle every form
// either way.
type FetchWriter struct {
	GroupOrder GroupOrder

	priorGroupID  uint64
	priorObjectID uint64
	havePrior     bool
}

// NewFetchWriter returns a writer for Objects on a FETCH data stream.
func NewFetchWriter(order GroupOrder) *FetchWriter {
	if !order.Valid() {
		order = GroupOrderAscending
	}
	return &FetchWriter{GroupOrder: order}
}

// AppendObject writes obj onto buf.
func (f *FetchWriter) AppendObject(buf []byte, obj *FetchObject) ([]byte, error) {
	if obj.EndOfRange != EndOfRangeNone {
		return f.appendEndOfRange(buf, obj)
	}

	// A Group ID Delta cannot express "the same Group": ascending, it always
	// advances at least one. Staying in a Group is encoded by omitting the
	// delta, which also makes the Object ID relative rather than absolute.
	sameGroup := f.havePrior && obj.GroupID == f.priorGroupID

	flags := uint64(fetchFlagObjectIDDelta | fetchFlagPriority)
	if !sameGroup {
		flags |= fetchFlagGroupIDDelta
	}
	if obj.Datagram {
		flags |= fetchFlagDatagram
	} else if obj.SubgroupID != 0 {
		flags |= uint64(FetchSubgroupExplicit)
	}
	if len(obj.Properties) > 0 {
		flags |= fetchFlagProperties
	}
	buf = vi64.Append(buf, flags)

	if !sameGroup {
		groupDelta, err := f.groupDelta(obj.GroupID)
		if err != nil {
			return nil, err
		}
		buf = vi64.Append(buf, groupDelta)
	}
	if !obj.Datagram && obj.SubgroupID != 0 {
		buf = vi64.Append(buf, obj.SubgroupID)
	}
	if sameGroup {
		if obj.ObjectID <= f.priorObjectID {
			return nil, errObjectIDNotIncreasing
		}
		buf = vi64.Append(buf, obj.ObjectID-f.priorObjectID)
	} else {
		// With the Group ID Delta present the Object ID is absolute.
		buf = vi64.Append(buf, obj.ObjectID)
	}
	buf = append(buf, obj.Priority)
	if len(obj.Properties) > 0 {
		buf = obj.Properties.appendLength(buf)
	}
	buf = vi64.Append(buf, uint64(len(obj.Payload)))
	buf = append(buf, obj.Payload...)

	f.remember(obj.GroupID, obj.ObjectID)
	return buf, nil
}

// appendEndOfRange writes an End of Range indicator: its Serialization Flags,
// the absolute Group ID and Object ID it names, and nothing else. See
// readEndOfRange for why no Object Payload Length follows.
func (f *FetchWriter) appendEndOfRange(buf []byte, obj *FetchObject) ([]byte, error) {
	switch obj.EndOfRange {
	case EndOfRangeNonExistent, EndOfRangeUnknown:
	default:
		return nil, errInvalidSerializationFlags
	}
	if len(obj.Payload) > 0 {
		return nil, errEndOfRangeWithPayload
	}

	buf = vi64.Append(buf, uint64(obj.EndOfRange))
	buf = vi64.Append(buf, obj.GroupID)
	buf = vi64.Append(buf, obj.ObjectID)

	f.remember(obj.GroupID, obj.ObjectID)
	return buf, nil
}

// groupDelta encodes a new Group ID against the prior one in the Group Order's
// direction. The first record's delta is the absolute Group ID.
func (f *FetchWriter) groupDelta(groupID uint64) (uint64, error) {
	if !f.havePrior {
		return groupID, nil
	}
	if f.GroupOrder == GroupOrderDescending {
		if groupID >= f.priorGroupID {
			return 0, errGroupIDNotDescending
		}
		return f.priorGroupID - groupID - 1, nil
	}
	if groupID <= f.priorGroupID {
		return 0, errGroupIDNotAscending
	}
	return groupID - f.priorGroupID - 1, nil
}

func (f *FetchWriter) remember(groupID, objectID uint64) {
	f.priorGroupID = groupID
	f.priorObjectID = objectID
	f.havePrior = true
}

// addWithoutOverflow adds delta to prev, reporting the PROTOCOL_VIOLATION that
// a wrap past 2^64-1 is.
func addWithoutOverflow(prev, delta uint64) (uint64, error) {
	if delta > maxUint64-prev {
		return 0, errObjectIDOverflow
	}
	return prev + delta, nil
}
