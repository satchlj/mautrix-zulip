package zid

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPortalIDRoundTrip(t *testing.T) {
	parsed, err := ParsePortalID(MakeTopicPortalID(12, "Weekly Sync: a:b"))
	require.NoError(t, err)
	assert.Equal(t, 12, parsed.StreamID)
	assert.Equal(t, "weekly sync: a:b", parsed.TopicKey)
	assert.False(t, parsed.IsDM())

	parsed, err = ParsePortalID(MakeTopicPortalID(5, ""))
	require.NoError(t, err)
	assert.Equal(t, 5, parsed.StreamID)
	assert.Equal(t, "", parsed.TopicKey)

	parsed, err = ParsePortalID(MakeDMPortalID([]int{9, 3, 9}))
	require.NoError(t, err)
	assert.Equal(t, []int{3, 9}, parsed.UserIDs)
	assert.True(t, parsed.IsDM())

	_, err = ParsePortalID("stream:1")
	assert.Error(t, err)
}
