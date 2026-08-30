package moqtransport

import "github.com/Eyevinn/moqtransport/internal/wire2"

// Types the wire format defines and the API hands straight through. They are
// aliases rather than copies so that nothing has to be converted on the way in
// or out, and so there is one definition of each to keep correct.
type (
	// Location is a {Group, Object} pair.
	Location = wire2.Location

	// KeyValuePair is a MOQT Key-Value-Pair. The parity of its Type selects
	// the value: an even Type carries ValueVarInt, an odd Type ValueBytes.
	KeyValuePair = wire2.KeyValuePair

	// KVPList is a sequence of Key-Value-Pairs, used for Object and Track
	// Properties.
	KVPList = wire2.KVPList

	// ObjectStatus marks an Object as normal or as the end of a Group or
	// Track.
	ObjectStatus = wire2.ObjectStatus

	// GroupOrder is the order Groups are delivered in.
	GroupOrder = wire2.GroupOrder
)

const (
	// ObjectStatusNormal is implicit for any Object with a payload.
	ObjectStatusNormal = wire2.ObjectStatusNormal
	// ObjectStatusEndOfGroup says no Object in this Group at or above this
	// Object ID exists.
	ObjectStatusEndOfGroup = wire2.ObjectStatusEndOfGroup
	// ObjectStatusEndOfTrack says no Object at or after this Location exists.
	ObjectStatusEndOfTrack = wire2.ObjectStatusEndOfTrack

	// GroupOrderAscending delivers Groups in increasing Group ID order.
	GroupOrderAscending = wire2.GroupOrderAscending
	// GroupOrderDescending delivers Groups in decreasing Group ID order.
	GroupOrderDescending = wire2.GroupOrderDescending
)

// ObjectForwardingPreference is how a publisher sends an Object
// (draft-ietf-moq-transport-18, Section 11.2.1). It is a property of the
// individual Object and may vary within a Track, but within a subscription an
// Object MUST be sent according to it.
type ObjectForwardingPreference int

const (
	// ObjectForwardingPreferenceSubgroup sends the Object on a subgroup
	// stream, which is reliable and ordered within the subgroup.
	ObjectForwardingPreferenceSubgroup ObjectForwardingPreference = iota
	// ObjectForwardingPreferenceDatagram sends the Object in a single
	// datagram, which may be dropped without notice if it exceeds the
	// session's maximum datagram size.
	ObjectForwardingPreferenceDatagram
)

func (p ObjectForwardingPreference) String() string {
	if p == ObjectForwardingPreferenceDatagram {
		return "datagram"
	}
	return "subgroup"
}

// An Object is a MOQT Object as the application sees it, with the wire
// format's delta encoding and omitted fields already resolved.
type Object struct {
	GroupID  uint64
	ObjectID uint64

	// SubgroupID is meaningless when ForwardingPreference is Datagram: a
	// datagram Object has no Subgroup.
	SubgroupID           uint64
	ForwardingPreference ObjectForwardingPreference

	// Priority is the publisher priority that applies to this Object, whether
	// it was on the wire or inherited from the subscription.
	Priority uint8

	// Properties are the Object Properties. They are relay-visible and
	// forwarded; anything the transport should not see belongs in Payload.
	Properties KVPList

	// Status is ObjectStatusNormal for an Object with a payload. A
	// non-Normal status always has an empty payload and no properties.
	Status  ObjectStatus
	Payload []byte
}
