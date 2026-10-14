package connector

import (
	"context"
	"strconv"
	"time"

	"github.com/rs/zerolog"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/database"
	"maunium.net/go/mautrix/bridgev2/networkid"
	"maunium.net/go/mautrix/bridgev2/simplevent"

	"go.mau.fi/mautrix-zulip/pkg/msgconv"
	"go.mau.fi/mautrix-zulip/pkg/msgconv/zulipemoji"
	"go.mau.fi/mautrix-zulip/pkg/zid"
	"go.mau.fi/mautrix-zulip/pkg/zulip/realtime/events"
)

func (zc *ZulipClient) handleZulipEvent(ctx context.Context, rawEvt events.Event) bool {
	log := zerolog.Ctx(ctx)
	log.Trace().Any("data", rawEvt).Msg("Event data")
	switch evt := rawEvt.(type) {
	case *events.Heartbeat:
		return true
	case *events.Message:
		return zc.handleMessage(ctx, evt)
	case *events.UpdateMessage:
		return zc.handleUpdateMessage(ctx, evt)
	case *events.DeleteMessage:
		return zc.handleDeleteMessage(ctx, evt)
	case *events.Reaction:
		return zc.handleReaction(ctx, evt)
	case *events.UpdateMessageFlags:
		return zc.handleUpdateMessageFlags(ctx, evt)
	case *events.Typing:
		return zc.handleTyping(ctx, evt)
	case *events.Subscription:
		return zc.handleSubscription(ctx, evt)
	case *events.Stream:
		return zc.handleStream(ctx, evt)
	case *events.UserTopic:
		return zc.handleUserTopic(ctx, evt)
	default:
		log.Debug().Type("event_type", evt).Msg("Unsupported event type")
		return true
	}
}

// queueIfPortalExists queues an event only if the portal already has a room. QueueRemoteEvent would otherwise
// create database rows for portals that may never get a room (e.g. for typing notifications in every topic).
func (zc *ZulipClient) queueIfPortalExists(ctx context.Context, evt bridgev2.RemoteEvent) bridgev2.EventHandlingResult {
	portal, err := zc.Main.Bridge.GetExistingPortalByKey(ctx, evt.GetPortalKey())
	if err != nil {
		return bridgev2.EventHandlingResultFailed.WithError(err)
	} else if portal == nil || portal.MXID == "" {
		return bridgev2.EventHandlingResultIgnored
	}
	return zc.UserLogin.QueueRemoteEvent(evt)
}

func isSuccess(res bridgev2.EventHandlingResult) bool {
	return res.Success || res.Ignored
}

func (zc *ZulipClient) shouldCreatePortal(msg *events.MessageData) bool {
	if msg.StreamID == 0 || zc.Main.Config.BridgeMuted {
		return true
	}
	return !zc.isMuted(msg.StreamID, &msg.Subject)
}

func (zc *ZulipClient) makeMessageEvent(msg *events.MessageData, localID string) *simplevent.Message[*events.MessageData] {
	if msg.StreamID != 0 {
		zc.rememberTopicName(msg.StreamID, msg.Subject)
	}
	return &simplevent.Message[*events.MessageData]{
		EventMeta: simplevent.EventMeta{
			Type: bridgev2.RemoteEventMessage,
			LogContext: func(c zerolog.Context) zerolog.Context {
				return c.
					Int("msg_id", msg.ID).
					Int("stream_id", msg.StreamID).
					Int("recipient_id", msg.RecipientID)
			},
			PortalKey:    zc.makePortalKey(msg),
			Sender:       zc.makeEventSender(msg.SenderID),
			CreatePortal: zc.shouldCreatePortal(msg),
			Timestamp:    time.Unix(int64(msg.Timestamp), 0),
			StreamOrder:  int64(msg.ID),
		},
		Data:          msg,
		ID:            zid.MakeMessageID(msg.ID),
		TransactionID: networkid.TransactionID(localID),
		ConvertMessageFunc: func(ctx context.Context, portal *bridgev2.Portal, intent bridgev2.MatrixAPI, data *events.MessageData) (*bridgev2.ConvertedMessage, error) {
			return msgconv.ToMatrix(ctx, portal, intent, zc.UserLogin, data)
		},
	}
}

func (zc *ZulipClient) handleMessage(ctx context.Context, evt *events.Message) bool {
	return isSuccess(zc.UserLogin.QueueRemoteEvent(zc.makeMessageEvent(&evt.Message, evt.LocalID)))
}

// getMessagePortal finds the portal a previously bridged message is in.
func (zc *ZulipClient) getMessagePortal(ctx context.Context, messageID int) (*database.Message, error) {
	return zc.Main.Bridge.DB.Message.GetFirstPartByID(ctx, zc.UserLogin.ID, zid.MakeMessageID(messageID))
}

func (zc *ZulipClient) handleUpdateMessage(ctx context.Context, evt *events.UpdateMessage) bool {
	log := zerolog.Ctx(ctx).With().Int("message_id", evt.MessageID).Logger()
	ctx = log.WithContext(ctx)
	if evt.RenderedContent != nil && !evt.RenderingOnly && zc.isMatrixEditEcho(evt) {
		log.Debug().Msg("Ignoring echo of edit sent from Matrix")
	} else if evt.RenderedContent != nil && !evt.RenderingOnly {
		existing, err := zc.getMessagePortal(ctx, evt.MessageID)
		if err != nil {
			log.Err(err).Msg("Failed to get edit target message")
			return false
		} else if existing == nil {
			log.Debug().Msg("Edit target message not found")
		} else {
			res := zc.UserLogin.QueueRemoteEvent(&simplevent.Message[*events.UpdateMessage]{
				EventMeta: simplevent.EventMeta{
					Type:      bridgev2.RemoteEventEdit,
					PortalKey: existing.Room,
					Sender:    zc.makeEventSender(ptrOr(evt.UserID, existing.SenderID)),
					Timestamp: time.Unix(int64(evt.EditTimestamp), 0),
					LogContext: func(c zerolog.Context) zerolog.Context {
						return c.Int("msg_id", evt.MessageID)
					},
				},
				Data:            evt,
				ID:              zid.MakeMessageID(evt.MessageID),
				TargetMessage:   zid.MakeMessageID(evt.MessageID),
				ConvertEditFunc: zc.convertEdit,
			})
			if !isSuccess(res) {
				return false
			}
		}
	}
	if evt.StreamID != nil && evt.OrigSubject != nil && (evt.Subject != nil || evt.NewStreamID != nil) {
		return zc.handleMove(ctx, evt)
	}
	return true
}

// isMatrixEditEcho checks whether an edit event is the echo of an edit that was just sent from Matrix.
func (zc *ZulipClient) isMatrixEditEcho(evt *events.UpdateMessage) bool {
	if evt.UserID == nil || *evt.UserID != zc.ownUserID {
		return false
	}
	editedAt, ok := zc.recentMatrixEdits.LoadAndDelete(evt.MessageID)
	return ok && time.Since(editedAt.(time.Time)) < 5*time.Minute
}

func ptrOr(val *int, fallback networkid.UserID) int {
	if val != nil {
		return *val
	}
	return zid.ParseUserID(fallback)
}

func (zc *ZulipClient) convertEdit(
	ctx context.Context, portal *bridgev2.Portal, intent bridgev2.MatrixAPI, existing []*database.Message, data *events.UpdateMessage,
) (*bridgev2.ConvertedEdit, error) {
	converted, err := msgconv.ToMatrix(ctx, portal, intent, zc.UserLogin, &events.MessageData{
		ID:      data.MessageID,
		Content: *data.RenderedContent,
	})
	if err != nil {
		return nil, err
	}
	return msgconv.MakeEdit(existing, converted), nil
}

func (zc *ZulipClient) handleDeleteMessage(ctx context.Context, evt *events.DeleteMessage) bool {
	messageIDs := evt.MessageIDs
	if evt.MessageID != nil {
		messageIDs = append(messageIDs, *evt.MessageID)
	}
	for _, msgID := range messageIDs {
		existing, err := zc.getMessagePortal(ctx, msgID)
		if err != nil {
			zerolog.Ctx(ctx).Err(err).Int("message_id", msgID).Msg("Failed to get deleted message")
			return false
		} else if existing == nil {
			continue
		}
		res := zc.UserLogin.QueueRemoteEvent(&simplevent.MessageRemove{
			EventMeta: simplevent.EventMeta{
				Type:      bridgev2.RemoteEventMessageRemove,
				PortalKey: existing.Room,
				// Zulip doesn't say who deleted the message, so just use the sender
				Sender: zc.makeEventSender(zid.ParseUserID(existing.SenderID)),
				LogContext: func(c zerolog.Context) zerolog.Context {
					return c.Int("msg_id", msgID)
				},
			},
			TargetMessage: existing.ID,
		})
		if !isSuccess(res) {
			return false
		}
	}
	return true
}

func (zc *ZulipClient) handleReaction(ctx context.Context, evt *events.Reaction) bool {
	existing, err := zc.getMessagePortal(ctx, evt.MessageID)
	if err != nil {
		zerolog.Ctx(ctx).Err(err).Msg("Failed to get reaction target message")
		return false
	} else if existing == nil {
		zerolog.Ctx(ctx).Debug().Int("target_message_id", evt.MessageID).Msg("Reaction target message not found")
		return true
	}
	evtType := bridgev2.RemoteEventReaction
	if evt.Op == "remove" {
		evtType = bridgev2.RemoteEventReactionRemove
	}
	emoji, emojiID := reactionToMatrix(&evt.ReactionData)
	return isSuccess(zc.UserLogin.QueueRemoteEvent(&simplevent.Reaction{
		EventMeta: simplevent.EventMeta{
			Type:      evtType,
			PortalKey: existing.Room,
			Sender:    zc.makeEventSender(evt.UserID),
			LogContext: func(c zerolog.Context) zerolog.Context {
				return c.Int("msg_id", evt.MessageID).Str("emoji_name", evt.EmojiName)
			},
		},
		TargetMessage: existing.ID,
		EmojiID:       emojiID,
		Emoji:         emoji,
	}))
}

// reactionToMatrix converts a Zulip reaction into a Matrix reaction key and a bridge emoji ID.
// Unicode emojis use the emoji code as the ID, other types use "type:name" so that they can be sent back to Zulip.
func reactionToMatrix(data *events.ReactionData) (emoji string, emojiID networkid.EmojiID) {
	if data.ReactionType == "unicode_emoji" || data.ReactionType == "" {
		emoji = zulipemoji.UnifiedToUnicode(data.EmojiCode)
		if emoji != "" {
			return emoji, networkid.EmojiID(data.EmojiCode)
		}
	}
	return ":" + data.EmojiName + ":", networkid.EmojiID(data.ReactionType + ":" + data.EmojiName)
}

func (zc *ZulipClient) handleUpdateMessageFlags(ctx context.Context, evt *events.UpdateMessageFlags) bool {
	if evt.Flag != "read" {
		return true
	}
	if evt.EventOp() == "remove" {
		return zc.handleMarkedUnread(ctx, evt)
	}
	if evt.All {
		return zc.markAllRead(ctx)
	}
	// Group the newly read messages by portal and send a read receipt for the latest one in each portal
	latestPerPortal := make(map[networkid.PortalKey]*database.Message)
	for _, msgID := range evt.Messages {
		existing, err := zc.getMessagePortal(ctx, msgID)
		if err != nil {
			zerolog.Ctx(ctx).Err(err).Int("message_id", msgID).Msg("Failed to get read message")
			return false
		} else if existing == nil {
			continue
		}
		prev := latestPerPortal[existing.Room]
		if prev == nil || zid.ParseMessageID(prev.ID) < msgID {
			latestPerPortal[existing.Room] = existing
		}
	}
	for portalKey, msg := range latestPerPortal {
		if !isSuccess(zc.queueReadReceipt(portalKey, zid.ParseMessageID(msg.ID))) {
			return false
		}
	}
	return true
}

// queueReadReceipt marks a portal as read up to (and including) the given Zulip message ID.
// The message doesn't need to be in the portal: the latest bridged message at or before it is used as the target.
func (zc *ZulipClient) queueReadReceipt(portalKey networkid.PortalKey, upToMessageID int) bridgev2.EventHandlingResult {
	zc.cacheLock.Lock()
	_, wasMarkedUnread := zc.markedUnread[portalKey]
	delete(zc.markedUnread, portalKey)
	zc.cacheLock.Unlock()
	if wasMarkedUnread {
		zc.UserLogin.QueueRemoteEvent(&simplevent.MarkUnread{
			EventMeta: simplevent.EventMeta{
				Type:      bridgev2.RemoteEventMarkUnread,
				PortalKey: portalKey,
				Sender:    zc.makeEventSender(zc.ownUserID),
			},
			Unread: false,
		})
	}
	receipt := &simplevent.Receipt{
		EventMeta: simplevent.EventMeta{
			Type:      bridgev2.RemoteEventReadReceipt,
			PortalKey: portalKey,
			Sender:    zc.makeEventSender(zc.ownUserID),
			LogContext: func(c zerolog.Context) zerolog.Context {
				return c.Int("read_up_to_msg_id", upToMessageID)
			},
		},
	}
	// Resolve the target inside the portal's event loop, so that messages queued earlier (e.g. backfill) are included.
	receipt.PreHandleFunc = func(ctx context.Context, portal *bridgev2.Portal) {
		target, oldest := zc.findReadTarget(ctx, portal.PortalKey, upToMessageID)
		if target != nil {
			receipt.LastTarget = target.ID
		} else if oldest != nil {
			// More unread messages than we looked at, mark everything before the oldest checked one as read.
			receipt.ReadUpTo = oldest.Timestamp.Add(-time.Millisecond)
		}
	}
	return zc.UserLogin.QueueRemoteEvent(receipt)
}

// findReadTarget finds the latest bridged message in a portal with an ID less than or equal to maxID.
// If none of the recent messages match, it returns the oldest message that was checked.
func (zc *ZulipClient) findReadTarget(ctx context.Context, portalKey networkid.PortalKey, maxID int) (target, oldest *database.Message) {
	msgs, err := zc.Main.Bridge.DB.Message.GetLastNInPortal(ctx, portalKey, 500)
	if err != nil {
		zerolog.Ctx(ctx).Err(err).Msg("Failed to get messages to find read receipt target")
		return nil, nil
	}
	var targetID int
	for _, msg := range msgs {
		msgID := zid.ParseMessageID(msg.ID)
		if msgID <= maxID && msgID > targetID {
			target, targetID = msg, msgID
		}
		if oldest == nil || msg.Timestamp.Before(oldest.Timestamp) {
			oldest = msg
		}
	}
	return
}

func (zc *ZulipClient) markAllRead(ctx context.Context) bool {
	userPortals, err := zc.Main.Bridge.DB.UserPortal.GetAllForLogin(ctx, zc.UserLogin.UserLogin)
	if err != nil {
		zerolog.Ctx(ctx).Err(err).Msg("Failed to get user portals to mark all as read")
		return false
	}
	for _, up := range userPortals {
		lastMsg, err := zc.Main.Bridge.DB.Message.GetLastPartAtOrBeforeTime(ctx, up.Portal, time.Now().Add(time.Minute))
		if err != nil {
			zerolog.Ctx(ctx).Err(err).Msg("Failed to get last message in portal to mark as read")
			continue
		} else if lastMsg != nil {
			zc.queueReadReceipt(up.Portal, zid.ParseMessageID(lastMsg.ID))
		}
	}
	return true
}

func (zc *ZulipClient) handleMarkedUnread(ctx context.Context, evt *events.UpdateMessageFlags) bool {
	portals := make(map[networkid.PortalKey]struct{})
	for msgIDStr, details := range evt.MessageDetails {
		var portalKey networkid.PortalKey
		if details.Type == "stream" {
			portalKey = zc.makeTopicPortalKey(details.StreamID, details.Topic)
		} else if msgID, err := strconv.Atoi(msgIDStr); err == nil {
			existing, err := zc.getMessagePortal(ctx, msgID)
			if err != nil || existing == nil {
				portalKey = zc.makeDMPortalKey(details.UserIDs)
			} else {
				portalKey = existing.Room
			}
		}
		portals[portalKey] = struct{}{}
	}
	for portalKey := range portals {
		zc.cacheLock.Lock()
		zc.markedUnread[portalKey] = struct{}{}
		zc.cacheLock.Unlock()
		res := zc.queueIfPortalExists(ctx, &simplevent.MarkUnread{
			EventMeta: simplevent.EventMeta{
				Type:      bridgev2.RemoteEventMarkUnread,
				PortalKey: portalKey,
				Sender:    zc.makeEventSender(zc.ownUserID),
			},
			Unread: true,
		})
		if !isSuccess(res) {
			return false
		}
	}
	return true
}

func (zc *ZulipClient) handleTyping(ctx context.Context, evt *events.Typing) bool {
	if evt.Sender.UserID == zc.ownUserID {
		return true
	}
	var portalKey networkid.PortalKey
	if evt.MessageType == "stream" {
		portalKey = zc.makeTopicPortalKey(evt.StreamID, evt.Topic)
	} else {
		userIDs := make([]int, 0, len(evt.Recipients)+1)
		userIDs = append(userIDs, evt.Sender.UserID)
		for _, r := range evt.Recipients {
			userIDs = append(userIDs, r.UserID)
		}
		portalKey = zc.makeDMPortalKey(userIDs)
	}
	var timeout time.Duration
	if evt.Op == "start" {
		// Zulip clients resend typing notifications every 10 seconds
		timeout = 15 * time.Second
	}
	zc.queueIfPortalExists(ctx, &simplevent.Typing{
		EventMeta: simplevent.EventMeta{
			Type:      bridgev2.RemoteEventTyping,
			PortalKey: portalKey,
			Sender:    zc.makeEventSender(evt.Sender.UserID),
		},
		Timeout: timeout,
	})
	// Typing notifications aren't important enough to block the event stream
	return true
}

func (zc *ZulipClient) handleSubscription(ctx context.Context, evt *events.Subscription) bool {
	switch evt.Op {
	case "add":
		for _, sub := range evt.Subscriptions {
			zc.setStream(sub)
		}
	case "remove":
		zc.cacheLock.Lock()
		for _, sub := range evt.Subscriptions {
			delete(zc.streams, sub.StreamID)
		}
		zc.cacheLock.Unlock()
	case "update":
		stream := zc.getStream(evt.StreamID)
		if stream == nil {
			return true
		}
		updated := *stream
		switch evt.Property {
		case "in_home_view":
			val, _ := evt.Value.(bool)
			updated.InHomeView = &val
		case "is_muted":
			val, _ := evt.Value.(bool)
			updated.IsMuted = val
			updated.InHomeView = nil
		default:
			return true
		}
		zc.setStream(updated)
		zc.resyncStreamPortals(ctx, evt.StreamID)
	}
	return true
}

func (zc *ZulipClient) handleStream(ctx context.Context, evt *events.Stream) bool {
	if evt.Op != "update" || (evt.Property != "name" && evt.Property != "description") {
		return true
	}
	stream := zc.getStream(evt.StreamID)
	if stream == nil {
		return true
	}
	updated := *stream
	val, _ := evt.Value.(string)
	if evt.Property == "name" {
		updated.Name = val
	} else {
		updated.Description = val
	}
	zc.setStream(updated)
	zc.resyncStreamPortals(ctx, evt.StreamID)
	return true
}

func (zc *ZulipClient) handleUserTopic(ctx context.Context, evt *events.UserTopic) bool {
	zc.cacheLock.Lock()
	zc.topicPolicies[topicRef{StreamID: evt.StreamID, TopicKey: zid.TopicKey(evt.TopicName)}] = evt.VisibilityPolicy
	zc.cacheLock.Unlock()
	zc.queueInfoResync(ctx, zc.makeTopicPortalKey(evt.StreamID, evt.TopicName))
	return true
}

// resyncStreamPortals updates the room info (name, topic, mute status) of all existing rooms for topics in a channel.
func (zc *ZulipClient) resyncStreamPortals(ctx context.Context, streamID int) {
	userPortals, err := zc.Main.Bridge.DB.UserPortal.GetAllForLogin(ctx, zc.UserLogin.UserLogin)
	if err != nil {
		zerolog.Ctx(ctx).Err(err).Msg("Failed to get user portals to resync channel")
		return
	}
	for _, up := range userPortals {
		parsed, err := zid.ParsePortalID(up.Portal.ID)
		if err == nil && !parsed.IsDM() && parsed.StreamID == streamID {
			zc.queueInfoResync(ctx, up.Portal)
		}
	}
}

func (zc *ZulipClient) queueInfoResync(ctx context.Context, portalKey networkid.PortalKey) {
	zc.queueIfPortalExists(ctx, &simplevent.ChatResync{
		EventMeta: simplevent.EventMeta{
			Type:      bridgev2.RemoteEventChatResync,
			PortalKey: portalKey,
		},
		GetChatInfoFunc: zc.GetChatInfo,
	})
}
