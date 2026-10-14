package connector

import (
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/networkid"

	"go.mau.fi/mautrix-zulip/pkg/zid"
	"go.mau.fi/mautrix-zulip/pkg/zulip/realtime/events"
)

func (zc *ZulipClient) makeEventSender(id int) bridgev2.EventSender {
	return bridgev2.EventSender{
		IsFromMe:    id == zc.ownUserID,
		SenderLogin: zid.MakeUserLoginID(id),
		Sender:      zid.MakeUserID(id),
	}
}

func (zc *ZulipClient) makeTopicPortalKey(streamID int, topic string) (pk networkid.PortalKey) {
	if zc.UserLogin.Bridge.Config.SplitPortals {
		pk.Receiver = zc.UserLogin.ID
	}
	pk.ID = zid.MakeTopicPortalID(streamID, topic)
	return pk
}

// makeDMPortalKey makes a portal key for a direct message conversation.
// The user ID list may or may not include the logged-in user.
func (zc *ZulipClient) makeDMPortalKey(userIDs []int) networkid.PortalKey {
	others := make([]int, 0, len(userIDs))
	for _, id := range userIDs {
		if id != zc.ownUserID {
			others = append(others, id)
		}
	}
	if len(others) == 0 {
		// Direct messages with yourself
		others = append(others, zc.ownUserID)
	}
	return networkid.PortalKey{
		ID:       zid.MakeDMPortalID(others),
		Receiver: zc.UserLogin.ID,
	}
}

func (zc *ZulipClient) makePortalKey(message *events.MessageData) networkid.PortalKey {
	if message.StreamID != 0 {
		return zc.makeTopicPortalKey(message.StreamID, message.Subject)
	}
	userIDs := make([]int, len(message.DisplayRecipient.Users))
	for i, user := range message.DisplayRecipient.Users {
		userIDs[i] = user.ID
	}
	return zc.makeDMPortalKey(userIDs)
}
