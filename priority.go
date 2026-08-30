package moqtransport

// Priority scheduling, and where MOQT's model and the transport's do not meet.
//
// draft-ietf-moq-transport-18 Section 7.2 orders schedulable objects
// lexicographically by four things: the subscriber priority of the request
// that caused them, then the publisher priority of the object, then the
// subscription's Group Order, then the Subgroup ID (or Object ID for a
// datagram). Both priorities are 0-255, lower first.
//
// quic-go's scheduler -- once a release carries it; see PrioritizedStream --
// offers RFC 9218's model instead: three bits of urgency, plus a boolean
// saying whether a stream shares its bucket round-robin or is served in
// stream-ID order. So roughly sixteen bits of ordering key have to be reduced
// to three, and one of the four terms has no equivalent at all.
//
// Rather than hide that behind a formula, the reduction is a named,
// replaceable step. What the default gives up is written on it.

// StreamPriority is what a MOQT priority reduces to at the transport: RFC
// 9218's urgency and incremental flag.
type StreamPriority struct {
	// Urgency is 0 through 7, lower scheduled first. Values outside that range
	// are clamped by the transport.
	Urgency int8

	// Incremental shares an urgency level round-robin between the streams in
	// it. Non-incremental streams in the same level are served in stream-ID
	// order instead, which for a publisher that opens streams as it produces
	// them is creation order.
	Incremental bool
}

// ObjectPriority is the full MOQT ordering key for one schedulable object,
// which is what Section 7.2 sorts on.
type ObjectPriority struct {
	// SubscriberPriority is the priority of the request that caused this
	// object to be sent. It is the primary term.
	SubscriberPriority uint8

	// PublisherPriority is the priority of the object itself, from the
	// subgroup header or the track's default.
	PublisherPriority uint8

	// GroupOrder is the subscription's, and decides whether lower or higher
	// Group IDs go first among objects that are otherwise equal.
	GroupOrder GroupOrder

	// GroupID and SubgroupID are the last two terms, and only break ties
	// between objects of the same request at the same priority.
	GroupID    uint64
	SubgroupID uint64
}

// PriorityMapper reduces a MOQT priority to a transport one.
//
// Implement it to choose which distinctions matter for a given application:
// the mapping is lossy, and which part of the key is worth the three bits
// depends on whether an application varies subscriber priority, publisher
// priority, or neither.
type PriorityMapper interface {
	MapPriority(ObjectPriority) StreamPriority
}

// PriorityMapperFunc adapts a function to [PriorityMapper].
type PriorityMapperFunc func(ObjectPriority) StreamPriority

func (f PriorityMapperFunc) MapPriority(p ObjectPriority) StreamPriority { return f(p) }

// DefaultPriorityMapper is used when a session sets none.
//
// It spends the urgency bits on the subscriber priority, which Section 7.2
// makes the primary term, by taking its top three bits. That is monotonic --
// a numerically higher-priority subscription never lands in a worse bucket --
// and it is all three bits will hold.
//
// What it gives up:
//
//   - Publisher priority is dropped entirely. In a session where every
//     subscription uses the default subscriber priority of 128, which is the
//     common case, every stream lands in the same bucket and only the
//     incremental flag below still separates anything. An application whose
//     publisher varies priority per subgroup -- audio ahead of video, say --
//     should use [PublisherPriorityMapper] instead, or write its own.
//   - Descending Group Order cannot be expressed. Non-incremental scheduling
//     serves the lowest stream ID first, which for a publisher opening streams
//     as it produces them is Ascending; there is no arrangement of urgency and
//     incremental that serves the newest group first. So Descending falls back
//     to incremental, which at least shares the level round-robin rather than
//     systematically starving the newest groups behind the oldest.
//   - Group ID and Subgroup ID are not used directly. They are approximated by
//     stream-ID order under non-incremental scheduling, which holds only while
//     streams are opened in that order.
var DefaultPriorityMapper PriorityMapper = PriorityMapperFunc(func(p ObjectPriority) StreamPriority {
	return StreamPriority{
		Urgency:     int8(p.SubscriberPriority >> 5),
		Incremental: p.GroupOrder == GroupOrderDescending,
	}
})

// PublisherPriorityMapper spends the urgency bits on the publisher priority
// instead, for the common shape where every subscription shares the default
// subscriber priority and the publisher is the one making distinctions.
//
// It inverts the default's trade: Section 7.2's primary term is the one
// dropped, so a session that does vary subscriber priority will not honour it.
var PublisherPriorityMapper PriorityMapper = PriorityMapperFunc(func(p ObjectPriority) StreamPriority {
	return StreamPriority{
		Urgency:     int8(p.PublisherPriority >> 5),
		Incremental: p.GroupOrder == GroupOrderDescending,
	}
})

// PrioritizedStream is a [SendStream] whose transport can schedule between
// streams. Adapters implement it where the transport allows.
//
// It is a separate interface rather than part of SendStream because no
// released quic-go has the underlying call: RFC 9218-style stream priorities
// landed on quic-go master in August 2026, after v0.61.0. Until a release
// carries it, nothing implements this and applyPriority does nothing --
// which is why the mapping is worth settling now and costs nothing to carry.
//
// webtransport-go exposes no equivalent at all, so a WebTransport session
// stays unscheduled even after that bump.
type PrioritizedStream interface {
	SendStream

	// SetPriority sets the scheduling priority for data sent on this stream,
	// using RFC 9218's urgency and incremental parameters.
	SetPriority(urgency int8, incremental bool)
}

// applyPriority tells the transport how to schedule a stream, if it can.
//
// A transport that cannot is not an error and not worth reporting: the stream
// still carries its data, just without a say in the order.
func applyPriority(stream SendStream, mapper PriorityMapper, p ObjectPriority) {
	prioritized, ok := stream.(PrioritizedStream)
	if !ok {
		return
	}
	if mapper == nil {
		mapper = DefaultPriorityMapper
	}
	priority := mapper.MapPriority(p)
	prioritized.SetPriority(priority.Urgency, priority.Incremental)
}
