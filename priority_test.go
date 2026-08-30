package moqtransport

import (
	"errors"
	"testing"

	"github.com/Eyevinn/moqtransport/internal/wire2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// prioritizedStream is a SendStream that can be scheduled, which no released
// quic-go currently gives us. It stands in for the one that will.
type prioritizedStream struct {
	SendStream
	calls []StreamPriority
}

func (s *prioritizedStream) SetPriority(urgency int8, incremental bool) {
	s.calls = append(s.calls, StreamPriority{Urgency: urgency, Incremental: incremental})
}

// The default spends the urgency bits on the subscriber priority, which
// Section 7.2 makes the primary term. The mapping is coarse but monotonic: a
// numerically higher-priority subscription never lands in a worse bucket.
func TestDefaultPriorityMapperIsMonotonicInSubscriberPriority(t *testing.T) {
	previous := int8(-1)
	for subscriber := 0; subscriber < 256; subscriber++ {
		got := DefaultPriorityMapper.MapPriority(ObjectPriority{
			SubscriberPriority: uint8(subscriber),
		})
		assert.GreaterOrEqual(t, got.Urgency, previous, "subscriber priority %d", subscriber)
		assert.GreaterOrEqual(t, got.Urgency, int8(0))
		assert.LessOrEqual(t, got.Urgency, int8(7))
		previous = got.Urgency
	}

	// The ends of the range reach the ends of the urgency scale.
	assert.Equal(t, int8(0), DefaultPriorityMapper.MapPriority(ObjectPriority{SubscriberPriority: 0}).Urgency)
	assert.Equal(t, int8(7), DefaultPriorityMapper.MapPriority(ObjectPriority{SubscriberPriority: 255}).Urgency)
	assert.Equal(t, int8(4), DefaultPriorityMapper.MapPriority(
		ObjectPriority{SubscriberPriority: DefaultSubscriberPriority}).Urgency)
}

// What the default gives up, stated as a test so that changing it is a
// deliberate act rather than a silent one.
func TestDefaultPriorityMapperLosses(t *testing.T) {
	// Publisher priority is dropped: two objects differing only in it map the
	// same way. This is the case worth knowing about, because a session where
	// every subscription uses the default subscriber priority has nothing else
	// to separate its streams.
	audio := DefaultPriorityMapper.MapPriority(ObjectPriority{
		SubscriberPriority: DefaultSubscriberPriority, PublisherPriority: 0,
	})
	video := DefaultPriorityMapper.MapPriority(ObjectPriority{
		SubscriberPriority: DefaultSubscriberPriority, PublisherPriority: 255,
	})
	assert.Equal(t, audio, video, "publisher priority is not represented")

	// Group and Subgroup IDs are not represented either; they survive only as
	// stream-ID order under non-incremental scheduling.
	first := DefaultPriorityMapper.MapPriority(ObjectPriority{GroupID: 1, SubgroupID: 0})
	later := DefaultPriorityMapper.MapPriority(ObjectPriority{GroupID: 9000, SubgroupID: 4})
	assert.Equal(t, first, later)
}

// Ascending Group Order is approximated by non-incremental scheduling, which
// serves the lowest stream ID first. Descending has no equivalent at all, so
// it falls back to round-robin rather than to an order that is actively wrong.
func TestPriorityMapperGroupOrder(t *testing.T) {
	for _, mapper := range []PriorityMapper{DefaultPriorityMapper, PublisherPriorityMapper} {
		ascending := mapper.MapPriority(ObjectPriority{GroupOrder: GroupOrderAscending})
		assert.False(t, ascending.Incremental, "ascending is stream-ID order")

		descending := mapper.MapPriority(ObjectPriority{GroupOrder: GroupOrderDescending})
		assert.True(t, descending.Incremental, "descending cannot be expressed, so it shares the level")
	}
}

// The publisher-priority mapper inverts the trade for the common shape where
// every subscription shares the default subscriber priority.
func TestPublisherPriorityMapper(t *testing.T) {
	assert.Equal(t, int8(0), PublisherPriorityMapper.MapPriority(
		ObjectPriority{PublisherPriority: 0, SubscriberPriority: 255}).Urgency)
	assert.Equal(t, int8(7), PublisherPriorityMapper.MapPriority(
		ObjectPriority{PublisherPriority: 255, SubscriberPriority: 0}).Urgency)
}

// A transport that cannot schedule is left alone, which is every transport
// today: no released quic-go carries the call, and webtransport-go exposes
// none at all.
func TestApplyPriorityIgnoresAnUnschedulableStream(t *testing.T) {
	stream, _ := newSendCapture(t)
	assert.NotPanics(t, func() {
		applyPriority(stream, DefaultPriorityMapper, ObjectPriority{SubscriberPriority: 0})
	})
}

func TestApplyPriorityUsesTheSessionMapper(t *testing.T) {
	plain, _ := newSendCapture(t)
	stream := &prioritizedStream{SendStream: plain}

	applyPriority(stream, nil, ObjectPriority{SubscriberPriority: 32})
	require.Len(t, stream.calls, 1)
	assert.Equal(t, StreamPriority{Urgency: 1}, stream.calls[0], "a nil mapper is the default")

	applyPriority(stream, PriorityMapperFunc(func(ObjectPriority) StreamPriority {
		return StreamPriority{Urgency: 6, Incremental: true}
	}), ObjectPriority{SubscriberPriority: 32})
	require.Len(t, stream.calls, 2)
	assert.Equal(t, StreamPriority{Urgency: 6, Incremental: true}, stream.calls[1])
}

// The whole key reaches the mapper when a subgroup is opened, which is the
// only place all four terms are known at once.
func TestOpenSubgroupPassesTheWholeKey(t *testing.T) {
	filter, err := wire2.SubscriptionFilterParameter(wire2.SubscriptionFilter{Type: wire2.FilterLargestObject})
	require.NoError(t, err)

	req, _, session := newIncomingSubscribe(t, wire2.Parameters{
		wire2.Uint8Parameter(wire2.ParamSubscriberPriority, 40),
		wire2.Uint8Parameter(wire2.ParamGroupOrder, uint8(GroupOrderDescending)),
		filter,
	})
	defer req.cancel(StreamErrorCancelled, errors.New("test over"))

	var got ObjectPriority
	session.mapper = PriorityMapperFunc(func(p ObjectPriority) StreamPriority {
		got = p
		return StreamPriority{}
	})

	subscription, err := req.Accept()
	require.NoError(t, err)
	assert.Equal(t, GroupOrderDescending, subscription.GroupOrder())

	_, err = subscription.OpenSubgroup(12, 3, 200)
	require.NoError(t, err)

	assert.Equal(t, uint8(40), got.SubscriberPriority, "from the SUBSCRIBE")
	assert.Equal(t, uint8(200), got.PublisherPriority, "from the subgroup header")
	assert.Equal(t, GroupOrderDescending, got.GroupOrder, "from the subscription")
	assert.Equal(t, uint64(12), got.GroupID)
	assert.Equal(t, uint64(3), got.SubgroupID)

	// And the reduction reaches the stream.
	require.Len(t, session.prioritized, 1)
	assert.Len(t, session.prioritized[0].calls, 1)
}

// A subscription that inherits the priority through the header's
// DEFAULT_PRIORITY bit still reports the header's value, since that is what
// the Objects on the stream will carry.
func TestOpenSubgroupPriorityWithSubscriptionDefault(t *testing.T) {
	req, _, session := newIncomingSubscribe(t, nil)
	defer req.cancel(StreamErrorCancelled, errors.New("test over"))

	var got ObjectPriority
	session.mapper = PriorityMapperFunc(func(p ObjectPriority) StreamPriority {
		got = p
		return StreamPriority{}
	})

	subscription, err := req.Accept()
	require.NoError(t, err)
	_, err = subscription.OpenSubgroup(0, 0, 77, WithSubscriptionPriority())
	require.NoError(t, err)

	assert.Equal(t, DefaultSubscriberPriority, got.SubscriberPriority, "the SUBSCRIBE said nothing")
	assert.Equal(t, uint8(77), got.PublisherPriority)
	assert.Equal(t, GroupOrderAscending, got.GroupOrder, "the SUBSCRIBE said nothing")
}

// A session with no mapper set uses the default rather than nothing.
func TestSessionPriorityMapperDefault(t *testing.T) {
	s := &Session{}
	// Comparing the funcs themselves proves nothing; compare what they do.
	assert.Equal(t,
		DefaultPriorityMapper.MapPriority(ObjectPriority{SubscriberPriority: 200}),
		s.priorityMapper().MapPriority(ObjectPriority{SubscriberPriority: 200}))

	s.PriorityMapper = PriorityMapperFunc(func(ObjectPriority) StreamPriority {
		return StreamPriority{Urgency: 5}
	})
	assert.Equal(t, int8(5), s.priorityMapper().MapPriority(ObjectPriority{}).Urgency)
}
