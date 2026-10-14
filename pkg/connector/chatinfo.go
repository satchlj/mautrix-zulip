package connector

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"go.mau.fi/util/ptr"
	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/database"
	"maunium.net/go/mautrix/bridgev2/networkid"
	"maunium.net/go/mautrix/event"

	"go.mau.fi/mautrix-zulip/pkg/zid"
	"go.mau.fi/mautrix-zulip/pkg/zulip/channels"
	"go.mau.fi/mautrix-zulip/pkg/zulip/messages"
	"go.mau.fi/mautrix-zulip/pkg/zulip/narrow"
	"go.mau.fi/mautrix-zulip/pkg/zulip/realtime/events"
	"go.mau.fi/mautrix-zulip/pkg/zulip/users"
)

func (zc *ZulipClient) IsThisUser(ctx context.Context, userID networkid.UserID) bool {
	return zid.ParseUserID(userID) == zc.ownUserID
}

func (zc *ZulipClient) GetChatInfo(ctx context.Context, portal *bridgev2.Portal) (*bridgev2.ChatInfo, error) {
	parsed, err := zid.ParsePortalID(portal.ID)
	if err != nil {
		return nil, err
	} else if parsed.IsDM() {
		return zc.wrapDMInfo(parsed.UserIDs), nil
	}
	stream, err := zc.fetchStream(ctx, parsed.StreamID)
	if err != nil {
		return nil, err
	}
	return zc.wrapTopicInfo(stream, zc.getTopicName(portal)), nil
}

// fetchStream returns the cached info of a channel, or fetches it from the server if it's not cached.
func (zc *ZulipClient) fetchStream(ctx context.Context, streamID int) (*events.SubscriptionData, error) {
	if stream := zc.getStream(streamID); stream != nil {
		return stream, nil
	}
	resp, err := channels.NewService(zc.Client).GetChannelByID(ctx, streamID)
	if err != nil {
		return nil, fmt.Errorf("failed to get channel info: %w", err)
	}
	// Not caching this, because the channel isn't subscribed (otherwise it would've been in the cache already)
	return &events.SubscriptionData{
		StreamID:    resp.Stream.StreamID,
		Name:        resp.Stream.Name,
		Description: resp.Stream.Description,
	}, nil
}

// getTopicName returns the case-preserved name of the topic a portal is for.
func (zc *ZulipClient) getTopicName(portal *bridgev2.Portal) string {
	zc.cacheLock.RLock()
	name, ok := zc.topicNames[portal.ID]
	zc.cacheLock.RUnlock()
	if ok {
		return name
	} else if meta, ok := portal.Metadata.(*zid.PortalMetadata); ok && meta.Topic != "" {
		return meta.Topic
	}
	parsed, _ := zid.ParsePortalID(portal.ID)
	return parsed.TopicKey
}

func (zc *ZulipClient) topicDisplayName(topic string) string {
	if topic == "" {
		return zc.emptyTopicName
	}
	return topic
}

func (zc *ZulipClient) wrapTopicInfo(stream *events.SubscriptionData, topic string) *bridgev2.ChatInfo {
	mutedUntil := &bridgev2.Unmuted
	if zc.isMuted(stream.StreamID, &topic) {
		mutedUntil = &event.MutedForever
	}
	return &bridgev2.ChatInfo{
		Name:  ptr.Ptr(zc.Main.Config.FormatTopicName(stream.Name, zc.topicDisplayName(topic))),
		Topic: &stream.Description,
		Members: &bridgev2.ChatMemberList{
			// Channels can have thousands of subscribers, so only add participants to topic rooms as they appear.
			IsFull: false,
			MemberMap: map[networkid.UserID]bridgev2.ChatMember{
				zid.MakeUserID(zc.ownUserID): {
					EventSender: zc.makeEventSender(zc.ownUserID),
					Membership:  event.MembershipJoin,
				},
			},
		},
		Type:        ptr.Ptr(database.RoomTypeDefault),
		CanBackfill: true,
		UserLocal: &bridgev2.UserLocalPortalInfo{
			MutedUntil: mutedUntil,
		},
		ExtraUpdates: func(ctx context.Context, portal *bridgev2.Portal) bool {
			meta := portal.Metadata.(*zid.PortalMetadata)
			if meta.Topic != topic {
				meta.Topic = topic
				return true
			}
			return false
		},
	}
}

func (zc *ZulipClient) wrapDMInfo(members []int) *bridgev2.ChatInfo {
	var otherUserID networkid.UserID
	portalType := database.RoomTypeGroupDM
	if len(members) == 1 {
		otherUserID = zid.MakeUserID(members[0])
		portalType = database.RoomTypeDM
	}
	memberMap := zc.makeMemberMap(members)
	memberMap[zid.MakeUserID(zc.ownUserID)] = bridgev2.ChatMember{
		EventSender: zc.makeEventSender(zc.ownUserID),
		Membership:  event.MembershipJoin,
	}
	return &bridgev2.ChatInfo{
		Members: &bridgev2.ChatMemberList{
			IsFull:           true,
			TotalMemberCount: len(memberMap),
			MemberMap:        memberMap,
			OtherUserID:      otherUserID,
		},
		Type:        &portalType,
		CanBackfill: true,
	}
}

func (zc *ZulipClient) makeMemberMap(members []int) map[networkid.UserID]bridgev2.ChatMember {
	memberMap := make(map[networkid.UserID]bridgev2.ChatMember, len(members))
	for _, m := range members {
		memberMap[zid.MakeUserID(m)] = bridgev2.ChatMember{
			EventSender: zc.makeEventSender(m),
			Membership:  event.MembershipJoin,
		}
	}
	return memberMap
}

func (zc *ZulipClient) GetUserInfo(ctx context.Context, ghost *bridgev2.Ghost) (*bridgev2.UserInfo, error) {
	user, err := users.NewService(zc.Client).GetUser(ctx, zid.ParseUserID(ghost.ID))
	if err != nil {
		// Cross-realm system bots (like Notification Bot) can't be fetched from the users API,
		// so fall back to the name and avatar from their messages.
		userID := zid.ParseUserID(ghost.ID)
		if _, ok := zc.senderInfo.Load(userID); !ok {
			zc.fetchSenderInfo(ctx, userID)
		}
		if seen, ok := zc.senderInfo.Load(userID); ok {
			info := seen.(*seenSender)
			return &bridgev2.UserInfo{
				Name:   &info.Name,
				Avatar: wrapAvatar(0, zc.absoluteURL(info.AvatarURL), info.Email),
				IsBot:  ptr.Ptr(true),
			}, nil
		}
		return nil, err
	}
	return zc.wrapUserInfo(user.User)
}

// absoluteURL resolves server-relative URLs (like /user_uploads/...) against the Zulip server URL.
func (zc *ZulipClient) absoluteURL(url string) string {
	if strings.HasPrefix(url, "/") && !strings.HasPrefix(url, "//") {
		return strings.TrimSuffix(zc.UserLogin.Metadata.(*zid.UserLoginMetadata).URL, "/") + url
	}
	return url
}

type seenSender struct {
	Name      string
	Email     string
	AvatarURL string
}

// fetchSenderInfo finds a message sent by the given user to learn their name and avatar.
func (zc *ZulipClient) fetchSenderInfo(ctx context.Context, userID int) {
	resp, err := messages.NewService(zc.Client).GetMessages(
		ctx,
		messages.Anchor("newest"),
		messages.NumBefore(1),
		messages.NumAfter(0),
		messages.NarrowMessage(narrow.NewFilter().Add(narrow.New(narrow.Sender, userID))),
	)
	if err == nil && len(resp.Messages) > 0 {
		zc.rememberSender(restMessageToEvent(&resp.Messages[0]))
	}
}

func (zc *ZulipClient) rememberSender(msg *events.MessageData) {
	zc.senderInfo.Store(msg.SenderID, &seenSender{
		Name:      msg.SenderFullName,
		Email:     msg.SenderEmail,
		AvatarURL: msg.AvatarURL,
	})
}

var AvatarClient = &http.Client{
	Timeout: 20 * time.Second,
}

func (zc *ZulipClient) wrapUserInfo(user users.UserData) (*bridgev2.UserInfo, error) {
	var identifiers []string
	if user.DeliveryEmail != "" {
		identifiers = []string{"mailto:" + user.DeliveryEmail}
	}
	return &bridgev2.UserInfo{
		Identifiers: identifiers,
		Name:        &user.FullName,
		Avatar:      wrapAvatar(user.AvatarVersion, zc.absoluteURL(user.AvatarURL), user.DeliveryEmail),
		IsBot:       &user.IsBot,
	}, nil
}

func wrapAvatar(version int, url, email string) *bridgev2.Avatar {
	if url == "" {
		if email == "" {
			return nil
		}
		emailHash := sha256.Sum256([]byte(email))
		url = fmt.Sprintf("https://www.gravatar.com/avatar/%x", emailHash[:])
	}
	return &bridgev2.Avatar{
		ID: networkid.AvatarID(strconv.Itoa(version)),
		Get: func(ctx context.Context) ([]byte, error) {
			req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
			if err != nil {
				return nil, err
			}
			req.Header.Set("User-Agent", mautrix.DefaultUserAgent)
			resp, err := AvatarClient.Do(req)
			if err != nil {
				return nil, err
			}
			defer resp.Body.Close()
			if resp.StatusCode >= 300 {
				return nil, fmt.Errorf("unexpected status code %d", resp.StatusCode)
			}
			return io.ReadAll(resp.Body)
		},
	}
}
