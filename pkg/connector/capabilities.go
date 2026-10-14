package connector

import (
	"context"

	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/event"

	"go.mau.fi/mautrix-zulip/pkg/zid"
)

func (zc *ZulipConnector) GetBridgeInfoVersion() (info, capabilities int) {
	return 1, 2
}

func (zc *ZulipConnector) GetCapabilities() *bridgev2.NetworkGeneralCapabilities {
	return &bridgev2.NetworkGeneralCapabilities{}
}

var zulipFileFeatures = &event.FileFeatures{
	MimeTypes: map[string]event.CapabilitySupportLevel{
		"*/*": event.CapLevelFullySupported,
	},
	Caption: event.CapLevelPartialSupport,
	// Zulip's default upload limit
	MaxSize: 25 * 1024 * 1024,
}

var zulipCaps = &event.RoomFeatures{
	ID: "fi.mau.zulip.capabilities.2026_10_14",
	Formatting: event.FormattingFeatureMap{
		event.FmtBold:               event.CapLevelFullySupported,
		event.FmtItalic:             event.CapLevelFullySupported,
		event.FmtStrikethrough:      event.CapLevelFullySupported,
		event.FmtInlineCode:         event.CapLevelFullySupported,
		event.FmtCodeBlock:          event.CapLevelFullySupported,
		event.FmtSyntaxHighlighting: event.CapLevelFullySupported,
		event.FmtBlockquote:         event.CapLevelFullySupported,
		event.FmtInlineLink:         event.CapLevelFullySupported,
		event.FmtUserLink:           event.CapLevelFullySupported,
		event.FmtRoomLink:           event.CapLevelPartialSupport,
		event.FmtEventLink:          event.CapLevelPartialSupport,
		event.FmtAtRoomMention:      event.CapLevelPartialSupport,
		event.FmtUnorderedList:      event.CapLevelFullySupported,
		event.FmtOrderedList:        event.CapLevelFullySupported,
		event.FmtListStart:          event.CapLevelFullySupported,
		event.FmtSpoiler:            event.CapLevelFullySupported,
		event.FmtSpoilerReason:      event.CapLevelFullySupported,
		event.FmtMath:               event.CapLevelFullySupported,
		event.FmtHeaders:            event.CapLevelFullySupported,
		event.FmtHorizontalLine:     event.CapLevelPartialSupport,
		event.FmtUnderline:          event.CapLevelDropped,
	},
	File: event.FileFeatureMap{
		event.MsgImage:      zulipFileFeatures,
		event.MsgVideo:      zulipFileFeatures,
		event.MsgAudio:      zulipFileFeatures,
		event.MsgFile:       zulipFileFeatures,
		event.CapMsgGIF:     zulipFileFeatures,
		event.CapMsgSticker: zulipFileFeatures,
	},
	MaxTextLength:       10000,
	LocationMessage:     event.CapLevelPartialSupport,
	Thread:              event.CapLevelUnsupported,
	Reply:               event.CapLevelPartialSupport,
	Edit:                event.CapLevelFullySupported,
	Delete:              event.CapLevelFullySupported,
	Reaction:            event.CapLevelFullySupported,
	ReactionCount:       0,
	ReadReceipts:        true,
	TypingNotifications: true,
	MarkAsUnread:        true,
}

func (zc *ZulipClient) GetCapabilities(ctx context.Context, portal *bridgev2.Portal) *event.RoomFeatures {
	parsed, _ := zid.ParsePortalID(portal.ID)
	if parsed.IsDM() {
		caps := zulipCaps.Clone()
		caps.ID += "+dm"
		return caps
	}
	return zulipCaps
}
