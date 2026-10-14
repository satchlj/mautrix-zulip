package connector

import (
	"context"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"github.com/rs/zerolog"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/database"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/format"
	"maunium.net/go/mautrix/id"

	"go.mau.fi/mautrix-zulip/pkg/zid"
	"go.mau.fi/mautrix-zulip/pkg/zulip/messages"
)

// markdownEscaper escapes characters that would otherwise be interpreted as Zulip markdown.
// Zulip doesn't use underscores for emphasis, so those are left alone.
var markdownEscaper = strings.NewReplacer(
	`\`, `\\`,
	"*", `\*`,
	"`", "\\`",
)

func (zc *ZulipClient) newHTMLParser(portal *bridgev2.Portal) *format.HTMLParser {
	return &format.HTMLParser{
		TabsToSpaces:   4,
		Newline:        "\n",
		HorizontalLine: "\n***\n",
		PillConverter: func(displayname, mxid, eventID string, ctx format.Context) string {
			return zc.convertPill(ctx.Ctx, displayname, mxid, eventID)
		},
		BoldConverter: func(s string, ctx format.Context) string {
			return fmt.Sprintf("**%s**", s)
		},
		ItalicConverter: func(s string, ctx format.Context) string {
			return fmt.Sprintf("*%s*", s)
		},
		StrikethroughConverter: func(s string, ctx format.Context) string {
			return fmt.Sprintf("~~%s~~", s)
		},
		UnderlineConverter: func(s string, ctx format.Context) string {
			return s
		},
		MathConverter: func(s string, ctx format.Context) string {
			return fmt.Sprintf("$$%s$$", s)
		},
		MathBlockConverter: func(s string, ctx format.Context) string {
			return format.DefaultMonospaceBlockConverter(s, "math", ctx)
		},
		SpoilerConverter: func(text, reason string, ctx format.Context) string {
			return format.DefaultMonospaceBlockConverter(text, "spoiler "+reason, ctx)
		},
		LinkConverter: func(text, href string, ctx format.Context) string {
			if text == href || text == "" {
				return href
			}
			return fmt.Sprintf("[%s](%s)", text, href)
		},
		TextConverter: func(s string, ctx format.Context) string {
			if ctx.TagStack.Has("code") || ctx.TagStack.Has("pre") {
				return s
			}
			return markdownEscaper.Replace(s)
		},
		ImageConverter: func(src, alt, title, width, height string, isEmoji bool) string {
			if alt != "" {
				return alt
			}
			return title
		},
	}
}

func (zc *ZulipClient) convertPill(ctx context.Context, displayname, mxid, eventID string) string {
	if strings.HasPrefix(mxid, "@") {
		userID := id.UserID(mxid)
		if ghostID, ok := zc.Main.Bridge.Matrix.ParseGhostMXID(userID); ok {
			if displayname == "" || displayname == mxid {
				if ghost, _ := zc.Main.Bridge.GetExistingGhostByID(ctx, ghostID); ghost != nil {
					displayname = ghost.Name
				}
			}
			return fmt.Sprintf("@**%s|%d**", displayname, zid.ParseUserID(ghostID))
		} else if user, _ := zc.Main.Bridge.GetExistingUserByMXID(ctx, userID); user != nil {
			if login := user.GetDefaultLogin(); login != nil {
				if displayname == "" || displayname == mxid {
					displayname = login.RemoteName
				}
				return fmt.Sprintf("@**%s|%d**", displayname, zid.ParseUserLoginID(login.ID))
			}
		}
		return displayname
	} else if strings.HasPrefix(mxid, "!") {
		portal, _ := zc.Main.Bridge.GetPortalByMXID(ctx, id.RoomID(mxid))
		if portal != nil {
			if link := zc.topicLink(ctx, portal, eventID); link != "" {
				return link
			}
		}
	}
	return format.DefaultPillConverter(displayname, mxid, eventID, format.NewContext(ctx))
}

// topicLink makes a Zulip channel link (#**channel>topic** or #**channel>topic@123**) for a topic portal.
func (zc *ZulipClient) topicLink(ctx context.Context, portal *bridgev2.Portal, eventID string) string {
	parsed, err := zid.ParsePortalID(portal.ID)
	if err != nil || parsed.IsDM() {
		return ""
	}
	stream := zc.getStream(parsed.StreamID)
	if stream == nil {
		return ""
	}
	suffix := ""
	if eventID != "" {
		msg, _ := zc.Main.Bridge.DB.Message.GetPartByMXID(ctx, id.EventID(eventID))
		if msg != nil {
			suffix = "@" + string(msg.ID)
		}
	}
	return fmt.Sprintf("#**%s>%s%s**", stream.Name, zc.getTopicName(portal), suffix)
}

// convertMatrixText converts the text of a Matrix message into Zulip markdown.
func (zc *ZulipClient) convertMatrixText(ctx context.Context, portal *bridgev2.Portal, content *event.MessageEventContent) string {
	var text string
	if containsZulipMath(content.Body) {
		// The body is the markdown the user typed. Converting the HTML would mangle the math
		// (e.g. subscripts turned into italics, escaped backslashes), so let Zulip parse the original instead.
		text = content.Body
	} else if content.Format == event.FormatHTML && content.FormattedBody != "" {
		text = zc.newHTMLParser(portal).Parse(content.FormattedBody, format.NewContext(ctx))
	} else {
		text = content.Body
	}
	if content.MsgType == event.MsgEmote {
		text = "/me " + text
	}
	return text
}

// containsZulipMath checks if text contains Zulip math syntax: $$inline$$ or a ```math block.
func containsZulipMath(text string) bool {
	if strings.Contains(text, "```math") {
		return true
	}
	start := strings.Index(text, "$$")
	return start >= 0 && strings.Contains(text[start+2:], "$$")
}

// encodeHashComponent encodes a string for use in Zulip narrow URLs, like Zulip's own encodeHashComponent.
func encodeHashComponent(s string) string {
	escaped := url.PathEscape(s)
	escaped = strings.NewReplacer("%", ".", "(", ".28", ")", ".29", ".", ".2E", "+", ".2B").Replace(escaped)
	return escaped
}

func (zc *ZulipClient) messageNarrowURL(msg *messages.Message) string {
	base := strings.TrimSuffix(zc.UserLogin.Metadata.(*zid.UserLoginMetadata).URL, "/")
	if msg.StreamID != 0 {
		streamName := msg.DisplayRecipient.Channel
		if stream := zc.getStream(msg.StreamID); stream != nil {
			streamName = stream.Name
		}
		return fmt.Sprintf(
			"%s/#narrow/channel/%d-%s/topic/%s/near/%d",
			base, msg.StreamID, encodeHashComponent(strings.ReplaceAll(streamName, " ", "-")),
			encodeHashComponent(msg.Subject), msg.ID,
		)
	}
	userIDs := make([]string, 0, len(msg.DisplayRecipient.Users))
	for _, user := range msg.DisplayRecipient.Users {
		userIDs = append(userIDs, strconv.Itoa(user.ID))
	}
	return fmt.Sprintf("%s/#narrow/dm/%s-dm/near/%d", base, strings.Join(userIDs, ","), msg.ID)
}

// makeReplyQuote creates the same quote prefix that Zulip's "Quote and reply" feature does,
// as Zulip doesn't have native replies.
func (zc *ZulipClient) makeReplyQuote(ctx context.Context, replyTo *database.Message) string {
	if replyTo == nil {
		return ""
	}
	replyToID := zid.ParseMessageID(replyTo.ID)
	resp, err := messages.NewService(zc.Client).FetchSingleMessage(
		ctx, replyToID, messages.ApplyMarkdownSingleMessage(false), messages.AllowEmptyTopicNameSingleMessage(),
	)
	if err != nil {
		zerolog.Ctx(ctx).Err(err).Int("reply_to_id", replyToID).Msg("Failed to fetch reply target message")
		return ""
	}
	msg := &resp.Message
	quote := format.DefaultMonospaceBlockConverter(msg.Content, "quote", format.NewContext(ctx))
	return fmt.Sprintf(
		"@_**%s|%d** [said](%s):\n%s\n\n",
		msg.SenderFullName, msg.SenderID, zc.messageNarrowURL(msg), quote,
	)
}
