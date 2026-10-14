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

func TestParse_InlineKatex(t *testing.T) {
	input := `<p>loss <span class="katex"><span class="katex-mathml"><math><semantics><mrow><mi>K</mi></mrow><annotation encoding="application/x-tex">K(w)</annotation></semantics></math></span><span class="katex-html" aria-hidden="true"><span class="base">K(w)</span></span></span> here</p>`
	out, _, _, err := Parse(context.Background(), nil, "https://chat.example.com", input, nil)
	require.NoError(t, err)
	assert.Equal(t, `loss <span data-mx-maths="K(w)"><code>K(w)</code></span> here`, out)
}

func TestParse_KatexInsideSpoiler(t *testing.T) {
	input := `<div class="spoiler-block"><div class="spoiler-header"><p>Details</p></div><div class="spoiler-content" aria-hidden="true"><p>see <span class="katex"><span class="katex-mathml"><math><semantics><mrow></mrow><annotation encoding="application/x-tex">x^2</annotation></semantics></math></span><span class="katex-html">huge</span></span> and <a href="#narrow/near/5">this</a></p></div></div>`
	out, _, _, err := Parse(context.Background(), nil, "https://chat.example.com", input, nil)
	require.NoError(t, err)
	assert.Contains(t, out, `<span data-mx-maths="x^2"><code>x^2</code></span>`)
	assert.NotContains(t, out, "huge")
	assert.Contains(t, out, `href="https://chat.example.com/#narrow/near/5"`)
	assert.Contains(t, out, `data-mx-spoiler="Details"`)
}
