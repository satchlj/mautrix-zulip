package zuliphtml

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const quoteReplyHTML = `<p><span class="user-mention silent" data-user-id="12">Alice</span> <a href="https://chat.example.com/#narrow/channel/1-general/topic/foo/near/12345">said</a>:</p>
<blockquote>
<p>original text</p>
</blockquote>
<p>my reply</p>`

func TestParse_QuoteReply(t *testing.T) {
	var checked int
	out, _, _, err := Parse(context.Background(), nil, "https://chat.example.com", quoteReplyHTML, func(id int) bool {
		checked = id
		return true
	})
	require.NoError(t, err)
	assert.Equal(t, 12345, checked)
	assert.Equal(t, "my reply", out)
}

func TestParse_QuoteReplyTargetMissing(t *testing.T) {
	// Mention resolution needs a bridge, so drop the user ID from the mention in this test
	input := strings.Replace(quoteReplyHTML, ` data-user-id="12"`, "", 1)
	out, _, _, err := Parse(context.Background(), nil, "https://chat.example.com", input, func(id int) bool {
		return false
	})
	require.NoError(t, err)
	assert.Contains(t, out, "<blockquote>")
	assert.Contains(t, out, "my reply")
}

func TestParse_NotQuoteReply(t *testing.T) {
	out, _, _, err := Parse(context.Background(), nil, "https://chat.example.com", `<p>hello <strong>world</strong></p>`, func(id int) bool {
		t.Fatal("shouldn't be called")
		return false
	})
	require.NoError(t, err)
	assert.Equal(t, "hello <strong>world</strong>", out)
}
