package connector

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/database"
	"maunium.net/go/mautrix/bridgev2/networkid"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/format"

	"go.mau.fi/mautrix-zulip/pkg/msgconv"
	"go.mau.fi/mautrix-zulip/pkg/zulip/realtime/events"
)

func TestHTMLToZulipMarkdown(t *testing.T) {
	zc := &ZulipClient{}
	parser := zc.newHTMLParser(nil)
	for input, expected := range map[string]string{
		"<strong>bold</strong> and <em>italic</em>":             "**bold** and *italic*",
		"<del>gone</del> <u>under</u>":                          "~~gone~~ under",
		"a * b `c`":                                             "a \\* b \\`c\\`",
		"<code>a * b</code>":                                    "`a * b`",
		`<pre><code class="language-go">x := *y</code></pre>`:   "```go\nx := *y\n```",
		`<a href="https://example.com">link</a>`:                "[link](https://example.com)",
		`<a href="https://example.com">https://example.com</a>`: "https://example.com",
		"<blockquote>quoted</blockquote>reply":                  "> quoted\nreply",
		`<span data-mx-spoiler="why">secret</span>`:             "```spoiler why\nsecret\n```",
		`<span data-mx-maths="x^2">x^2</span>`:                  "$$x^2$$",
		"<ul><li>one</li><li>two</li></ul>":                     "* one\n* two",
		"snake_case_name":                                       "snake_case_name",
	} {
		assert.Equal(t, expected, parser.Parse(input, format.NewContext(context.Background())), input)
	}
}

func TestConvertMatrixText_Emote(t *testing.T) {
	zc := &ZulipClient{}
	text := zc.convertMatrixText(context.Background(), nil, &event.MessageEventContent{MsgType: event.MsgEmote, Body: "waves"})
	assert.Equal(t, "/me waves", text)
}

func TestReactionRoundTrip(t *testing.T) {
	name, emojiID, err := matrixReactionToZulip("👍️")
	require.NoError(t, err)
	assert.Equal(t, "+1", name)
	emoji, incomingID := reactionToMatrix(&events.ReactionData{EmojiName: "+1", EmojiCode: "1f44d", ReactionType: "unicode_emoji"})
	assert.Equal(t, "👍", emoji)
	assert.Equal(t, emojiID, incomingID)

	name, emojiID, err = matrixReactionToZulip(":party_parrot:")
	require.NoError(t, err)
	assert.Equal(t, "party_parrot", name)
	emoji, incomingID = reactionToMatrix(&events.ReactionData{EmojiName: "party_parrot", EmojiCode: "42", ReactionType: "realm_emoji"})
	assert.Equal(t, ":party_parrot:", emoji)
	assert.Equal(t, emojiID, incomingID)

	_, _, err = matrixReactionToZulip("not an emoji")
	assert.Error(t, err)
}

func TestEncodeHashComponent(t *testing.T) {
	assert.Equal(t, "weekly.20sync.2E.28v2.29", encodeHashComponent("weekly sync.(v2)"))
}

func TestMakeEdit(t *testing.T) {
	existing := []*database.Message{{PartID: ""}, {PartID: "1"}}
	converted := &bridgev2.ConvertedMessage{Parts: []*bridgev2.ConvertedMessagePart{
		{ID: "", Content: &event.MessageEventContent{Body: "new"}},
	}}
	edit := msgconv.MakeEdit(existing, converted)
	require.Len(t, edit.ModifiedParts, 1)
	assert.Equal(t, existing[0], edit.ModifiedParts[0].Part)
	require.Len(t, edit.DeletedParts, 1)
	assert.Equal(t, networkid.PartID("1"), edit.DeletedParts[0].PartID)
	assert.Nil(t, edit.AddedParts)
}

func TestConvertMatrixText_Math(t *testing.T) {
	zc := &ZulipClient{}
	text := zc.convertMatrixText(context.Background(), nil, &event.MessageEventContent{
		MsgType:       event.MsgText,
		Body:          `**see** $$a_1 + \frac{b}{c}$$`,
		Format:        event.FormatHTML,
		FormattedBody: `<strong>see</strong> $$a<em>1 + \frac{b}{c}$$`,
	})
	assert.Equal(t, `**see** $$a_1 + \frac{b}{c}$$`, text)
	assert.False(t, containsZulipMath("costs $$ and more"))
	assert.True(t, containsZulipMath("```math\nx\n```"))
}

func TestConvertMatrixText_Collapsible(t *testing.T) {
	zc := &ZulipClient{}
	convert := func(body, formatted string) string {
		content := &event.MessageEventContent{MsgType: event.MsgText, Body: body}
		if formatted != "" {
			content.Format = event.FormatHTML
			content.FormattedBody = formatted
		}
		return zc.convertMatrixText(context.Background(), nil, content)
	}
	// Typed into Beeper's composer, which escapes the HTML tags
	assert.Equal(t,
		"```spoiler Goals for the next **1–3** months\nHidden **bold** text\n\n* item one\n```",
		convert(
			"<details><summary>Goals for the next 1–3 months</summary>\n\nHidden bold text\n\nitem one\n\n</details>",
			"&lt;details&gt;&lt;summary&gt;Goals for the next <strong>1–3</strong> months&lt;/summary&gt;\n\n<p>Hidden <strong>bold</strong> text</p>\n<ul>\n<li>item one</li>\n</ul>\n&lt;/details&gt;",
		),
	)
	// Real <details> elements, e.g. when editing a message that came from Zulip
	assert.Equal(t,
		"```spoiler Part **6**\nhidden\n```\n\nafter",
		convert("Part 6\nhidden\nafter", "<details><summary>Part <strong>6</strong></summary>\n<p>hidden</p>\n</details><p>after</p>"),
	)
	// Plain text, nested sections, and sections on a single line
	assert.Equal(t,
		"before\n````spoiler Outer\nfirst\n```spoiler Inner\nsecond\n```\n````\nafter",
		convert("before\n<details><summary>Outer</summary>\nfirst\n<details><summary>Inner</summary>second</details>\n</details>\nafter", ""),
	)
	assert.Equal(t, "```spoiler\nno title\n```", convert("<details>no title</details>", ""))
	// Code blocks and unclosed sections are left alone
	assert.Equal(t,
		"```html\n<details><summary>x</summary>\n</details>\n```",
		convert("", "<pre><code class=\"language-html\">&lt;details&gt;&lt;summary&gt;x&lt;/summary&gt;\n&lt;/details&gt;\n</code></pre>"),
	)
	assert.Equal(t, "<details><summary>Open</summary>\nnever closed", convert("<details><summary>Open</summary>\nnever closed", ""))
	// Zulip's own syntax typed into Beeper loses the title, but stays collapsible
	assert.Equal(t,
		"```spoiler\nHidden **bold** text\n- item one\n```",
		convert("Hidden **bold** text\n- item one", "<pre><code class=\"language-spoiler\">Hidden **bold** text\n- item one\n</code></pre>"),
	)
}
