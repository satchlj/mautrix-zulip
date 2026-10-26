package zuliphtml

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"maunium.net/go/mautrix/id"
)

const quoteReplyHTML = `<p><span class="user-mention silent" data-user-id="12">Alice</span> <a href="https://chat.example.com/#narrow/channel/1-general/topic/foo/near/12345">said</a>:</p>
<blockquote>
<p>original text</p>
</blockquote>
<p>my reply</p>`

func TestParse_QuoteReply(t *testing.T) {
	var checked int
	out, _, _, err := Parse(context.Background(), nil, "https://chat.example.com", quoteReplyHTML, Options{ReplyTargetExists: func(id int) bool {
		checked = id
		return true
	}})
	require.NoError(t, err)
	assert.Equal(t, 12345, checked)
	assert.Equal(t, "my reply", out)
}

func TestParse_QuoteReplyTargetMissing(t *testing.T) {
	// Mention resolution needs a bridge, so drop the user ID from the mention in this test
	input := strings.Replace(quoteReplyHTML, ` data-user-id="12"`, "", 1)
	out, _, _, err := Parse(context.Background(), nil, "https://chat.example.com", input, Options{ReplyTargetExists: func(id int) bool {
		return false
	}})
	require.NoError(t, err)
	assert.Contains(t, out, "<blockquote>")
	assert.Contains(t, out, "my reply")
}

func TestParse_NotQuoteReply(t *testing.T) {
	out, _, _, err := Parse(context.Background(), nil, "https://chat.example.com", `<p>hello <strong>world</strong></p>`, Options{ReplyTargetExists: func(id int) bool {
		t.Fatal("shouldn't be called")
		return false
	}})
	require.NoError(t, err)
	assert.Equal(t, "hello <strong>world</strong>", out)
}

func TestParse_InlineKatex(t *testing.T) {
	input := `<p>loss <span class="katex"><span class="katex-mathml"><math><semantics><mrow><mi>K</mi></mrow><annotation encoding="application/x-tex">K(w)</annotation></semantics></math></span><span class="katex-html" aria-hidden="true"><span class="base">K(w)</span></span></span> here</p>`
	out, _, _, err := Parse(context.Background(), nil, "https://chat.example.com", input, Options{})
	require.NoError(t, err)
	assert.Equal(t, `loss <span data-mx-maths="K(w)"><code>K(w)</code></span> here`, out)
}

func TestParse_KatexInsideSpoiler(t *testing.T) {
	input := `<div class="spoiler-block"><div class="spoiler-header"><p>Details</p></div><div class="spoiler-content" aria-hidden="true"><p>see <span class="katex"><span class="katex-mathml"><math><semantics><mrow></mrow><annotation encoding="application/x-tex">x^2</annotation></semantics></math></span><span class="katex-html">huge</span></span> and <a href="#narrow/near/5">this</a></p></div></div>`
	out, _, _, err := Parse(context.Background(), nil, "https://chat.example.com", input, Options{})
	require.NoError(t, err)
	assert.Contains(t, out, `<span data-mx-maths="x^2"><code>x^2</code></span>`)
	assert.NotContains(t, out, "huge")
	assert.Contains(t, out, `href="https://chat.example.com/#narrow/near/5"`)
	assert.Contains(t, out, `<details><summary>Details</summary>`)
	assert.Contains(t, out, `<p>see `)
}

func TestParse_MathImages(t *testing.T) {
	input := `<p>loss <span class="katex"><span class="katex-mathml"><math><semantics><mrow></mrow><annotation encoding="application/x-tex">K(w)</annotation></semantics></math></span><span class="katex-html">big</span></span></p><p><span class="katex-display"><span class="katex"><span class="katex-mathml"><math><semantics><mrow></mrow><annotation encoding="application/x-tex">x^2</annotation></semantics></math></span></span></span></p>`
	render := func(tex string, display bool) *MathImage {
		return &MathImage{URL: id.ContentURIString("mxc://example.com/" + map[bool]string{true: "d", false: "i"}[display]), Width: 10, Height: 5}
	}
	out, atts, _, err := Parse(context.Background(), nil, "https://chat.example.com", input, Options{RenderMath: render, MathMode: MathModeInline})
	require.NoError(t, err)
	assert.Empty(t, atts)
	assert.Contains(t, out, `<span><img src="mxc://example.com/i" alt="$K(w)$" title="K(w)" width="10" height="5"/></span>`)
	assert.Contains(t, out, `<div><img src="mxc://example.com/d" alt="$$x^2$$"`)

	out, atts, _, err = Parse(context.Background(), nil, "https://chat.example.com", input, Options{RenderMath: render, MathMode: MathModeAttachments})
	require.NoError(t, err)
	require.Len(t, atts, 1)
	assert.Equal(t, "x^2", atts[0].TeX)
	assert.Contains(t, out, `<span data-mx-maths="K(w)"><code>K(w)</code></span>`)
}

func TestParse_InlineImage(t *testing.T) {
	input := "<p>That's not a blogpost, it's a novella <br>\n<img alt=\"Screenshot 2026-10-15 at 17.55.30.png\" class=\"inline-image\" data-original-content-type=\"image/png\" data-original-dimensions=\"672x578\" data-original-src=\"/user_uploads/96296/abc/Screenshot.png\" src=\"/user_uploads/thumbnail/96296/abc/Screenshot.png/840x560.webp\"></p>\n<p>after</p>"
	out, attachments, _, err := Parse(context.Background(), nil, "https://chat.example.com", input, Options{})
	require.NoError(t, err)
	assert.Equal(t, "<p>That&#39;s not a blogpost, it&#39;s a novella</p>\n<p>after</p>", out)
	require.Len(t, attachments, 1)
	assert.Equal(t, "https://chat.example.com/user_uploads/96296/abc/Screenshot.png", attachments[0].URL)
	assert.Equal(t, "Screenshot 2026-10-15 at 17.55.30.png", attachments[0].FileName)
}

func TestParse_ImageOnly(t *testing.T) {
	input := `<p><img alt="a.png" class="inline-image" data-original-src="/user_uploads/1/x/a.png" src="/user_uploads/thumbnail/1/x/a.png/840x560.webp"><img alt="b.png" class="image-loading-placeholder" data-original-src="/user_uploads/1/y/b.png" src="/static/images/loading/loader-black.svg"></p>`
	out, attachments, _, err := Parse(context.Background(), nil, "https://chat.example.com", input, Options{})
	require.NoError(t, err)
	assert.Equal(t, "", strings.TrimSpace(out))
	require.Len(t, attachments, 2)
	assert.Equal(t, "https://chat.example.com/user_uploads/1/y/b.png", attachments[1].URL)
}

func TestParse_SpoilerBlock(t *testing.T) {
	input := "<div class=\"spoiler-block\"><div class=\"spoiler-header\">\n<p>Part <strong>6</strong></p>\n</div><div class=\"spoiler-content\" aria-hidden=\"true\">\n<p>hidden</p>\n</div></div><p>after</p>"
	out, _, _, err := Parse(context.Background(), nil, "https://chat.example.com", input, Options{})
	require.NoError(t, err)
	assert.Equal(t, "<details><summary>Part <strong>6</strong></summary>\n<p>hidden</p>\n</details><p>after</p>", out)

	input = `<div class="spoiler-block"><div class="spoiler-header"></div><div class="spoiler-content" aria-hidden="true"><p>hidden</p></div></div>`
	out, _, _, err = Parse(context.Background(), nil, "https://chat.example.com", input, Options{})
	require.NoError(t, err)
	assert.Equal(t, "<details><summary>Spoiler</summary><p>hidden</p></details>", out)
}
