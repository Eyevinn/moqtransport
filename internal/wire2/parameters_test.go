package wire2

import (
	"io"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestParameterEncodingsAreNotParityBased is the point of this whole file.
// Message Parameters are not Key-Value-Pairs: the value encoding comes from
// the parameter's definition, not from the parity of its Type. These four
// cases would all be encoded wrongly by a parity rule.
func TestParameterEncodingsAreNotParityBased(t *testing.T) {
	cases := []struct {
		name  string
		param Parameter
		body  []byte
	}{
		{
			// Type 0x20 is even, so parity would say varint. It is a uint8.
			name:  "SUBSCRIBER_PRIORITY is a uint8 at an even type",
			param: Uint8Parameter(ParamSubscriberPriority, 200),
			body:  []byte{0x20, 200},
		},
		{
			name:  "GROUP_ORDER is a uint8 at an even type",
			param: Uint8Parameter(ParamGroupOrder, 1),
			body:  []byte{0x22, 0x01},
		},
		{
			name:  "FORWARD is a uint8 at an even type",
			param: Uint8Parameter(ParamForward, 0),
			body:  []byte{0x10, 0x00},
		},
		{
			// Type 0x09 is odd, so parity would say length-prefixed bytes.
			// It is a Location: two bare varints with no length.
			name:  "LARGEST_OBJECT is a Location at an odd type",
			param: LocationParameter(ParamLargestObject, Location{Group: 7, Object: 3}),
			body:  []byte{0x09, 0x07, 0x03},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			buf, err := Parameters{tc.param}.appendNum(nil)
			require.NoError(t, err)
			assert.Equal(t, append([]byte{0x01}, tc.body...), buf)

			var got Parameters
			n, err := got.parseNum(buf)
			require.NoError(t, err)
			assert.Equal(t, len(buf), n)
			assert.Equal(t, Parameters{tc.param}, got)
		})
	}
}

func TestParametersRoundTrip(t *testing.T) {
	params := Parameters{
		VarintParameter(ParamObjectDeliveryTimeout, 5000),
		BytesParameter(ParamAuthorizationToken, []byte("a-token")),
		VarintParameter(ParamExpires, 1),
		LocationParameter(ParamLargestObject, Location{Group: 100, Object: 2}),
		VarintParameter(ParamFillTimeout, 250),
		Uint8Parameter(ParamForward, 1),
		Uint8Parameter(ParamSubscriberPriority, 128),
		BytesParameter(ParamSubscriptionFilter, []byte{0x01, 0x02}),
		Uint8Parameter(ParamGroupOrder, 2),
		VarintParameter(ParamNewGroupRequest, 9),
		NamespaceParameter(ParamTrackNamespacePrefix, [][]byte{[]byte("a"), []byte("b")}),
	}
	buf, err := params.appendNum(nil)
	require.NoError(t, err)

	var got Parameters
	n, err := got.parseNum(buf)
	require.NoError(t, err)
	assert.Equal(t, len(buf), n)
	assert.Equal(t, params, got)
}

func TestParametersEmpty(t *testing.T) {
	buf, err := Parameters{}.appendNum(nil)
	require.NoError(t, err)
	assert.Equal(t, []byte{0x00}, buf)

	var got Parameters
	n, err := got.parseNum(buf)
	require.NoError(t, err)
	assert.Equal(t, 1, n)
	assert.Empty(t, got)
}

func TestParametersSortIntoAscendingOrder(t *testing.T) {
	params := Parameters{
		Uint8Parameter(ParamSubscriberPriority, 1), // 0x20
		VarintParameter(ParamExpires, 2),           // 0x08
		VarintParameter(ParamObjectDeliveryTimeout, 3),
	}
	buf, err := params.appendNum(nil)
	require.NoError(t, err)

	var got Parameters
	_, err = got.parseNum(buf)
	require.NoError(t, err)
	require.Len(t, got, 3)
	assert.Equal(t, ParamObjectDeliveryTimeout, got[0].Type)
	assert.Equal(t, ParamExpires, got[1].Type)
	assert.Equal(t, ParamSubscriberPriority, got[2].Type)

	assert.Equal(t, ParamSubscriberPriority, params[0].Type, "the caller's list must not be mutated")
}

// TestParametersRejectUnknownType covers the rule that makes this registry
// load-bearing: without the definition the parser cannot know how long the
// value is, so it cannot skip to the next parameter.
func TestParametersRejectUnknownType(t *testing.T) {
	t.Run("on parse", func(t *testing.T) {
		// One parameter, Type 0x50, one byte of value.
		var got Parameters
		_, err := got.parseNum([]byte{0x01, 0x50, 0x00})
		assert.ErrorContains(t, err, "unknown message parameter type 0x50")
	})

	t.Run("on append", func(t *testing.T) {
		_, err := Parameters{{Type: 0x50, Number: 1}}.appendNum(nil)
		assert.ErrorContains(t, err, "unknown message parameter type 0x50")
	})
}

func TestParametersRejectDuplicates(t *testing.T) {
	t.Run("a non-repeatable type", func(t *testing.T) {
		buf, err := Parameters{
			Uint8Parameter(ParamForward, 1),
			Uint8Parameter(ParamForward, 0),
		}.appendNum(nil)
		require.NoError(t, err)

		var got Parameters
		_, err = got.parseNum(buf)
		assert.ErrorContains(t, err, "duplicate message parameter FORWARD")
	})

	t.Run("AUTHORIZATION_TOKEN may repeat", func(t *testing.T) {
		params := Parameters{
			BytesParameter(ParamAuthorizationToken, []byte("one")),
			BytesParameter(ParamAuthorizationToken, []byte("two")),
		}
		buf, err := params.appendNum(nil)
		require.NoError(t, err)

		var got Parameters
		_, err = got.parseNum(buf)
		require.NoError(t, err)
		assert.Equal(t, params, got)
	})
}

func TestParametersRejectOversizedUint8(t *testing.T) {
	_, err := Parameters{VarintParameter(ParamSubscriberPriority, 256)}.appendNum(nil)
	assert.ErrorContains(t, err, "does not fit a uint8")
}

func TestParametersRejectDeltaOverflow(t *testing.T) {
	// A 9-byte delta of MaxUint64 on top of a first type of 2.
	data := []byte{0x02, 0x02, 0x00}
	data = append(data, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff)
	var got Parameters
	_, err := got.parseNum(data)
	assert.ErrorIs(t, err, errDeltaTypeOverflow)
}

func TestParametersRejectOversizedCount(t *testing.T) {
	var got Parameters
	_, err := got.parseNum([]byte{0x40, 0x02, 0x00})
	assert.ErrorIs(t, err, io.ErrUnexpectedEOF)
}

func TestParametersTruncated(t *testing.T) {
	buf, err := Parameters{
		LocationParameter(ParamLargestObject, Location{Group: 1, Object: 2}),
	}.appendNum(nil)
	require.NoError(t, err)
	for i := range buf[:len(buf)-1] {
		var got Parameters
		_, err := got.parseNum(buf[:i])
		assert.Error(t, err, "truncating to %d bytes", i)
	}
}

func TestParametersNamespaceFieldLimit(t *testing.T) {
	ns := make([][]byte, 33)
	for i := range ns {
		ns[i] = []byte("x")
	}
	buf, err := Parameters{NamespaceParameter(ParamTrackNamespacePrefix, ns)}.appendNum(nil)
	require.NoError(t, err)

	var got Parameters
	_, err = got.parseNum(buf)
	assert.ErrorIs(t, err, errTooManyFields)
}

func TestParametersGetAndName(t *testing.T) {
	params := Parameters{Uint8Parameter(ParamForward, 1)}
	p, ok := params.Get(ParamForward)
	require.True(t, ok)
	assert.Equal(t, uint64(1), p.Number)

	_, ok = params.Get(ParamExpires)
	assert.False(t, ok)

	assert.Equal(t, "FORWARD", ParameterName(ParamForward))
	assert.Contains(t, ParameterName(0x99), "UNKNOWN")
}
