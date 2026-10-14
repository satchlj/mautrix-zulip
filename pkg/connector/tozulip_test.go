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
