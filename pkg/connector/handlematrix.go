package connector

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync/atomic"
	"time"

	"github.com/rs/zerolog"
	"go.mau.fi/util/ptr"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/database"
	"maunium.net/go/mautrix/bridgev2/networkid"
	"maunium.net/go/mautrix/event"

	"go.mau.fi/mautrix-zulip/pkg/msgconv/zulipemoji"
	"go.mau.fi/mautrix-zulip/pkg/zid"
	"go.mau.fi/mautrix-zulip/pkg/zulip"
	"go.mau.fi/mautrix-zulip/pkg/zulip/messages"
	"go.mau.fi/mautrix-zulip/pkg/zulip/messages/recipient"
)

var (
	_ bridgev2.EditHandlingNetworkAPI         = (*ZulipClient)(nil)
	_ bridgev2.RedactionHandlingNetworkAPI    = (*ZulipClient)(nil)
	_ bridgev2.ReactionHandlingNetworkAPI     = (*ZulipClient)(nil)
	_ bridgev2.ReadReceiptHandlingNetworkAPI  = (*ZulipClient)(nil)
	_ bridgev2.MarkedUnreadHandlingNetworkAPI = (*ZulipClient)(nil)
	_ bridgev2.TypingHandlingNetworkAPI       = (*ZulipClient)(nil)
)

var localIDCounter atomic.Uint64

func (zc *ZulipClient) HandleMatrixMessage(ctx context.Context, msg *bridgev2.MatrixMessage) (*bridgev2.MatrixMessageResponse, error) {
	parsed, err := zid.ParsePortalID(msg.Portal.ID)
	if err != nil {
		return nil, err
	}
	var text string
	switch msg.Content.MsgType {
	case event.MsgText, event.MsgNotice, event.MsgEmote:
		text = zc.convertMatrixText(ctx, msg.Portal, msg.Content)
	case event.MsgImage, event.MsgVideo, event.MsgAudio, event.MsgFile, event.CapMsgSticker, "":
		text, err = zc.uploadMatrixMedia(ctx, msg.Portal, msg.Content)
		if err != nil {
			return nil, err
		}
	case event.MsgLocation:
		text = msg.Content.Body
		if geoURI := msg.Content.GeoURI; geoURI != "" {
			text = fmt.Sprintf("%s\n%s", text, geoURI)
		}
	default:
		return nil, fmt.Errorf("%w %s", bridgev2.ErrUnsupportedMessageType, msg.Content.MsgType)
	}
	text = zc.makeReplyQuote(ctx, msg.ReplyTo) + text

	localID := fmt.Sprintf("%d.%d", time.Now().Unix(), localIDCounter.Add(1))
	txnID := networkid.TransactionID(localID)
	msg.AddPendingToIgnore(txnID)
	sendOpts := []messages.SendMessageOption{
		messages.WithLocalID(ptr.Val(zc.queueID.Load()), localID),
	}
	srv := messages.NewService(zc.Client)
	var resp *messages.SendMessageResponse
	if parsed.IsDM() {
		resp, err = srv.SendMessageToUsers(ctx, recipient.ToUsers(parsed.UserIDs), text, sendOpts...)
	} else {
		resp, err = srv.SendMessageToChannelTopic(ctx, recipient.ToChannel(parsed.StreamID), zc.getTopicName(msg.Portal), text, sendOpts...)
	}
	if err != nil {
		msg.RemovePending(txnID)
		return nil, wrapZulipError(err)
	}
	response := &bridgev2.MatrixMessageResponse{
		DB: &database.Message{
			ID:       zid.MakeMessageID(resp.ID),
			SenderID: zid.MakeUserID(zc.ownUserID),
		},
		StreamOrder:   int64(resp.ID),
		RemovePending: txnID,
	}
	if zc.shouldRenderSentMath(text) {
		portalKey, sentID := msg.Portal.PortalKey, resp.ID
		response.PostSave = func(ctx context.Context, _ *database.Message) {
			// Run in the background, as this is called inside the portal event loop
			go zc.renderSentMath(zc.UserLogin.Log.WithContext(zc.Main.Bridge.BackgroundCtx), portalKey, sentID)
		}
	}
	return response, nil
}

func (zc *ZulipClient) shouldRenderSentMath(text string) bool {
	return zc.Main.mathRenderer() != nil && containsZulipMath(text)
}

// renderSentMath replaces the LaTeX source in a message sent from Matrix with the rendered math,
// by editing the Matrix message (using double puppeting) to match Zulip's rendering of the message.
func (zc *ZulipClient) renderSentMath(ctx context.Context, portalKey networkid.PortalKey, messageID int) {
	resp, err := messages.NewService(zc.Client).FetchSingleMessage(
		ctx, messageID, messages.ApplyMarkdownSingleMessage(true), messages.AllowEmptyTopicNameSingleMessage(),
	)
	if err != nil {
		zerolog.Ctx(ctx).Err(err).Int("message_id", messageID).Msg("Failed to fetch sent message to render math")
		return
	}
	zc.UserLogin.QueueRemoteEvent(zc.makeRenderedEdit(portalKey, zc.ownUserID, messageID, resp.Message.Content, time.Now()))
}

// wrapZulipError converts Zulip API errors into errors that are shown nicely to the Matrix user.
func wrapZulipError(err error) error {
	var zulipErr zulip.ErrorResp
	if errors.As(err, &zulipErr) && zulipErr.Inner.Msg() != "" {
		return bridgev2.WrapErrorInStatus(err).
			WithMessage(zulipErr.Inner.Msg()).
			WithIsCertain(true).
			WithSendNotice(true)
	}
	return err
}

func (zc *ZulipClient) uploadMatrixMedia(ctx context.Context, portal *bridgev2.Portal, content *event.MessageEventContent) (string, error) {
	mxc := content.URL
	if content.File != nil {
		mxc = content.File.URL
	}
	data, err := zc.Main.Bridge.Bot.DownloadMedia(ctx, mxc, content.File)
	if err != nil {
		return "", fmt.Errorf("%w: %w", bridgev2.ErrMediaDownloadFailed, err)
	}
	fileName := content.FileName
	if fileName == "" {
		fileName = content.Body
	}
	if fileName == "" {
		fileName = "file"
	}
	resp, err := messages.NewService(zc.Client).UploadFileFromBytes(ctx, fileName, data)
	if err != nil {
		return "", fmt.Errorf("%w: %w", bridgev2.ErrMediaReuploadFailed, wrapZulipError(err))
	}
	uri := resp.URL
	if uri == "" {
		uri = resp.URI
	}
	link := fmt.Sprintf("[%s](%s)", strings.NewReplacer("[", "\\[", "]", "\\]").Replace(fileName), uri)
	if content.FileName != "" && content.Body != "" && content.Body != content.FileName {
		// The body is a caption
		caption := zc.convertMatrixText(ctx, portal, content)
		return caption + "\n" + link, nil
	}
	return link, nil
}

func (zc *ZulipClient) HandleMatrixEdit(ctx context.Context, msg *bridgev2.MatrixEdit) error {
	switch msg.Content.MsgType {
	case event.MsgText, event.MsgNotice, event.MsgEmote:
	default:
		return bridgev2.WrapErrorInStatus(errors.New("only text messages can be edited on Zulip")).
			WithErrorAsMessage().
			WithIsCertain(true).
			WithSendNotice(true).
			WithErrorReason(event.MessageStatusUnsupported)
	}
	text := zc.convertMatrixText(ctx, msg.Portal, msg.Content)
	if msg.EditTarget.ReplyTo.MessageID != "" {
		replyTo, err := zc.Main.Bridge.DB.Message.GetFirstOrSpecificPartByID(ctx, msg.Portal.Receiver, msg.EditTarget.ReplyTo)
		if err == nil && replyTo != nil {
			text = zc.makeReplyQuote(ctx, replyTo) + text
		}
	}
	msgID := zid.ParseMessageID(msg.EditTarget.ID)
	if !zc.shouldRenderSentMath(text) {
		// Ignore the echo of the edit, unless the message has math, in which case the echo is used to
		// replace the LaTeX source in the Matrix message with the rendered math.
		zc.recentMatrixEdits.Store(msgID, time.Now())
	}
	_, err := messages.NewService(zc.Client).EditMessage(ctx, msgID, messages.NewContent(text))
	if err != nil {
		zc.recentMatrixEdits.Delete(msgID)
	}
	return wrapZulipError(err)
}

func (zc *ZulipClient) HandleMatrixMessageRemove(ctx context.Context, msg *bridgev2.MatrixMessageRemove) error {
	_, err := messages.NewService(zc.Client).DeleteMessage(ctx, zid.ParseMessageID(msg.TargetMessage.ID))
	return wrapZulipError(err)
}

var customEmojiKeyRegex = regexp.MustCompile(`^:([^:\s]+):$`)

// matrixReactionToZulip converts a Matrix reaction key into a Zulip emoji name and the bridge emoji ID.
// The emoji ID format matches the one generated by reactionToMatrix.
func matrixReactionToZulip(key string) (name string, emojiID networkid.EmojiID, err error) {
	if match := customEmojiKeyRegex.FindStringSubmatch(key); match != nil {
		return match[1], networkid.EmojiID(string(zulip.RealmEmojiType) + ":" + match[1]), nil
	}
	code, name, ok := zulipemoji.UnicodeToName(key)
	if !ok {
		return "", "", bridgev2.WrapErrorInStatus(fmt.Errorf("reaction %s isn't supported on Zulip", key)).
			WithErrorAsMessage().
			WithIsCertain(true).
			WithSendNotice(false).
			WithErrorReason(event.MessageStatusUnsupported)
	}
	return name, networkid.EmojiID(code), nil
}

func (zc *ZulipClient) PreHandleMatrixReaction(ctx context.Context, msg *bridgev2.MatrixReaction) (bridgev2.MatrixReactionPreResponse, error) {
	key := msg.Content.RelatesTo.Key
	_, emojiID, err := matrixReactionToZulip(key)
	if err != nil {
		return bridgev2.MatrixReactionPreResponse{}, err
	}
	return bridgev2.MatrixReactionPreResponse{
		SenderID: zid.MakeUserID(zc.ownUserID),
		EmojiID:  emojiID,
		Emoji:    key,
	}, nil
}

func (zc *ZulipClient) HandleMatrixReaction(ctx context.Context, msg *bridgev2.MatrixReaction) (*database.Reaction, error) {
	name, _, err := matrixReactionToZulip(msg.Content.RelatesTo.Key)
	if err != nil {
		return nil, err
	}
	_, err = messages.NewService(zc.Client).AddEmojiReaction(ctx, zid.ParseMessageID(msg.TargetMessage.ID), name)
	if err != nil {
		return nil, wrapZulipError(err)
	}
	return &database.Reaction{}, nil
}

func (zc *ZulipClient) HandleMatrixReactionRemove(ctx context.Context, msg *bridgev2.MatrixReactionRemove) error {
	emojiID := string(msg.TargetReaction.EmojiID)
	var opts []messages.RemoveEmojiReactionOption
	if reactionType, name, ok := strings.Cut(emojiID, ":"); ok {
		opts = append(opts,
			messages.RemoveEmojiReactionEmojiName(name),
			messages.RemoveEmojiReactionReactionType(zulip.ReactionType(reactionType)),
		)
	} else {
		opts = append(opts,
			messages.RemoveEmojiReactionEmojiCode(emojiID),
			messages.RemoveEmojiReactionReactionType(zulip.UnicodeEmojiType),
		)
	}
	_, err := messages.NewService(zc.Client).RemoveEmojiReaction(ctx, zid.ParseMessageID(msg.TargetReaction.MessageID), opts...)
	return wrapZulipError(err)
}

// getReadTarget finds the latest bridged message that a read receipt covers.
func (zc *ZulipClient) getReadTarget(ctx context.Context, msg *bridgev2.MatrixReadReceipt) (*database.Message, error) {
	if msg.ExactMessage != nil {
		return msg.ExactMessage, nil
	}
	if msg.ReadUpTo.IsZero() {
		return nil, nil
	}
	return zc.Main.Bridge.DB.Message.GetLastPartAtOrBeforeTime(ctx, msg.Portal.PortalKey, msg.ReadUpTo)
}

func (zc *ZulipClient) HandleMatrixReadReceipt(ctx context.Context, msg *bridgev2.MatrixReadReceipt) error {
	target, err := zc.getReadTarget(ctx, msg)
	if err != nil {
		return err
	} else if target == nil {
		return nil
	}
	filter, err := zc.portalNarrow(msg.Portal)
	if err != nil {
		return err
	}
	zc.cacheLock.Lock()
	delete(zc.markedUnread, msg.Portal.PortalKey)
	zc.cacheLock.Unlock()
	// Mark the target message and everything before it in the conversation as read.
	_, err = messages.NewService(zc.Client).UpdatePersonalMessageFlagsNarrow(
		ctx, string(target.ID), 1000, 0, filter, messages.OperationAdd, messages.FlagRead,
		messages.UpdatePersonalMessageFlagsNarrowIncludeAnchor(),
	)
	return err
}

func (zc *ZulipClient) HandleMarkedUnread(ctx context.Context, msg *bridgev2.MatrixMarkedUnread) error {
	if !msg.Content.Unread {
		return nil
	}
	lastMessage, err := zc.Main.Bridge.DB.Message.GetLastPartAtOrBeforeTime(ctx, msg.Portal.PortalKey, time.Now().Add(time.Minute))
	if err != nil {
		return err
	} else if lastMessage == nil {
		return nil
	}
	_, err = messages.NewService(zc.Client).UpdatePersonalMessageFlags(
		ctx, []int{zid.ParseMessageID(lastMessage.ID)}, messages.OperationRemove, messages.FlagRead,
	)
	return err
}

func (zc *ZulipClient) HandleMatrixTyping(ctx context.Context, msg *bridgev2.MatrixTyping) error {
	parsed, err := zid.ParsePortalID(msg.Portal.ID)
	if err != nil {
		return err
	}
	op := messages.TypingStop
	if msg.IsTyping {
		op = messages.TypingStart
	}
	srv := messages.NewService(zc.Client)
	if parsed.IsDM() {
		return srv.SetTypingStatusDirect(ctx, op, parsed.UserIDs)
	}
	return srv.SetTypingStatusTopic(ctx, op, parsed.StreamID, zc.getTopicName(msg.Portal))
}
