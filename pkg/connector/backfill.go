package connector

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"time"

	"github.com/rs/zerolog"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/networkid"

	"go.mau.fi/mautrix-zulip/pkg/msgconv"
	"go.mau.fi/mautrix-zulip/pkg/zid"
	"go.mau.fi/mautrix-zulip/pkg/zulip/messages"
	"go.mau.fi/mautrix-zulip/pkg/zulip/narrow"
	"go.mau.fi/mautrix-zulip/pkg/zulip/realtime/events"
)

var _ bridgev2.BackfillingNetworkAPI = (*ZulipClient)(nil)

// portalNarrow returns the Zulip narrow that matches all messages in a portal.
func (zc *ZulipClient) portalNarrow(portal *bridgev2.Portal) (narrow.Filter, error) {
	parsed, err := zid.ParsePortalID(portal.ID)
	if err != nil {
		return nil, err
	} else if parsed.IsDM() {
		return narrow.NewFilter().Add(narrow.New(narrow.Dm, parsed.UserIDs)), nil
	}
	return narrow.NewFilter().
		Add(narrow.New(narrow.Channel, parsed.StreamID)).
		Add(narrow.New(narrow.Topic, zc.getTopicName(portal))), nil
}

func (zc *ZulipClient) FetchMessages(ctx context.Context, params bridgev2.FetchMessagesParams) (*bridgev2.FetchMessagesResponse, error) {
	filter, err := zc.portalNarrow(params.Portal)
	if err != nil {
		return nil, err
	}
	count := params.Count
	if count <= 0 {
		count = 100
	} else if count > 1000 {
		count = 1000
	}
	anchor := "newest"
	if !params.Forward {
		if params.Cursor != "" {
			anchor = string(params.Cursor)
		} else if params.AnchorMessage != nil {
			anchor = string(params.AnchorMessage.ID)
		}
	}
	resp, err := messages.NewService(zc.Client).GetMessages(
		ctx,
		messages.Anchor(anchor),
		messages.IncludeAnchor(false),
		messages.NumBefore(count),
		messages.NumAfter(0),
		messages.NarrowMessage(filter),
		messages.ApplyMarkdownMessage(true),
		messages.AllowEmptyTopicName(),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch messages: %w", err)
	}
	var minID int
	if params.Forward && params.AnchorMessage != nil {
		minID = zid.ParseMessageID(params.AnchorMessage.ID)
	}
	allRead := true
	backfillMessages := make([]*bridgev2.BackfillMessage, 0, len(resp.Messages))
	for _, msg := range resp.Messages {
		if msg.ID <= minID {
			continue
		}
		if !slices.Contains(msg.Flags, string(messages.FlagRead)) {
			allRead = false
		}
		data := restMessageToEvent(&msg)
		zc.rememberSender(data)
		if data.StreamID != 0 {
			zc.rememberTopicName(data.StreamID, data.Subject)
		}
		converted, err := msgconv.ToMatrix(ctx, params.Portal, params.Portal.Bridge.Bot, zc.UserLogin, data)
		if err != nil {
			zerolog.Ctx(ctx).Err(err).Int("message_id", msg.ID).Msg("Failed to convert message for backfill")
			continue
		}
		backfillMessages = append(backfillMessages, &bridgev2.BackfillMessage{
			ConvertedMessage: converted,
			Sender:           zc.makeEventSender(msg.SenderID),
			ID:               zid.MakeMessageID(msg.ID),
			Timestamp:        time.Unix(int64(msg.Timestamp), 0),
			StreamOrder:      int64(msg.ID),
			Reactions:        zc.convertBackfillReactions(data.Reactions, msg.Reactions),
		})
	}
	var cursor networkid.PaginationCursor
	if len(resp.Messages) > 0 {
		cursor = networkid.PaginationCursor(strconv.Itoa(resp.Messages[0].ID))
	}
	return &bridgev2.FetchMessagesResponse{
		Messages: backfillMessages,
		Cursor:   cursor,
		HasMore:  !resp.FoundOldest && len(resp.Messages) > 0,
		Forward:  params.Forward,
		// Read state is synced separately after backfilling based on Zulip's unread messages list,
		// but if everything in the batch is read, it's safe to mark it as read immediately.
		MarkRead: allRead,
	}, nil
}

func (zc *ZulipClient) convertBackfillReactions(reactions []events.ReactionData, rest []messages.Reaction) []*bridgev2.BackfillReaction {
	output := make([]*bridgev2.BackfillReaction, 0, len(rest))
	for i := range reactions {
		emoji, emojiID := reactionToMatrix(&reactions[i])
		output = append(output, &bridgev2.BackfillReaction{
			Sender:  zc.makeEventSender(rest[i].UserID),
			EmojiID: emojiID,
			Emoji:   emoji,
		})
	}
	return output
}

// restMessageToEvent converts a message fetched from the REST API into the format used in events.
func restMessageToEvent(msg *messages.Message) *events.MessageData {
	users := make([]events.DisplayRecipientObject, len(msg.DisplayRecipient.Users))
	for i, user := range msg.DisplayRecipient.Users {
		users[i] = events.DisplayRecipientObject{
			ID:       user.ID,
			Email:    user.Email,
			FullName: user.FullName,
		}
	}
	reactions := make([]events.ReactionData, len(msg.Reactions))
	for i, reaction := range msg.Reactions {
		reactions[i] = events.ReactionData{
			EmojiName:    reaction.EmojiName,
			EmojiCode:    reaction.EmojiCode,
			ReactionType: reaction.ReactionType,
		}
	}
	return &events.MessageData{
		ID:          msg.ID,
		Type:        msg.Type,
		AvatarURL:   msg.AvatarURL,
		Client:      msg.Client,
		Content:     msg.Content,
		ContentType: msg.ContentType,
		DisplayRecipient: events.DisplayRecipient{
			IsChannel: msg.DisplayRecipient.IsChannel,
			Channel:   msg.DisplayRecipient.Channel,
			Users:     users,
		},
		IsMeMessage:    msg.IsMeMessage,
		Reactions:      reactions,
		RecipientID:    msg.RecipientID,
		SenderEmail:    msg.SenderEmail,
		SenderFullName: msg.SenderFullName,
		SenderID:       msg.SenderID,
		SenderRealmStr: msg.SenderRealmStr,
		StreamID:       msg.StreamID,
		Subject:        msg.Subject,
		Timestamp:      msg.Timestamp,
	}
}
