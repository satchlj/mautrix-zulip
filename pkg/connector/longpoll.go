package connector

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/rs/zerolog"
	"maunium.net/go/mautrix/bridgev2/status"

	"go.mau.fi/mautrix-zulip/pkg/zid"
	"go.mau.fi/mautrix-zulip/pkg/zulip"
	"go.mau.fi/mautrix-zulip/pkg/zulip/realtime"
	"go.mau.fi/mautrix-zulip/pkg/zulip/realtime/events"
)

func (zc *ZulipClient) pollQueue(ctx context.Context) {
	rtc := realtime.NewService(zc.Client)
	log := zc.UserLogin.Log.With().Str("component", "zulip poll").Logger()
	ctx = log.WithContext(ctx)
	stopChan := make(chan struct{})
	ctx, cancel := context.WithCancel(ctx)
	defer func() {
		cancel()
		close(stopChan)
		zc.UserLogin.Log.Debug().Msg("Polling stopped")
	}()
	if oldCancel := zc.stopPoll.Swap(&cancel); oldCancel != nil {
		(*oldCancel)()
	}
	zc.pollStopped.Store(&stopChan)
	zc.UserLogin.BridgeState.Send(status.BridgeState{StateEvent: status.StateConnecting})

	var connectedSent bool
	var failingEventID, failCount int
	const maxEventAttempts = 3
	// Zulip Cloud occasionally serves error pages for a few seconds. Only report a disconnection once polling
	// has been failing for a while, so that short blips don't show up as disconnections in clients.
	const pollErrorGracePeriod = time.Minute
	var pollFailingSince time.Time
	retryDelay := 5 * time.Second
	sleep := func() bool {
		select {
		case <-time.After(retryDelay):
			retryDelay = min(retryDelay*2, 5*time.Minute)
			return true
		case <-ctx.Done():
			return false
		}
	}
	meta := zc.UserLogin.Metadata.(*zid.UserLoginMetadata)
	// Always register a new queue on startup, so that the initial state (unread messages, subscriptions, etc.)
	// is fetched and the chats are synced. Old queues expire after a few minutes anyway.
	meta.QueueID = ""
	for {
		if meta.QueueID == "" {
			err := zc.registerQueue(ctx, rtc)
			if ctx.Err() != nil {
				return
			} else if err != nil {
				log.Err(err).Msg("Failed to register event queue")
				connectedSent = false
				if httpStatus := zulip.HTTPStatus(err); httpStatus == http.StatusUnauthorized || httpStatus == http.StatusForbidden {
					zc.UserLogin.BridgeState.Send(status.BridgeState{
						StateEvent: status.StateBadCredentials,
						Error:      "zulip-invalid-credentials",
						Message:    err.Error(),
					})
					return
				}
				zc.UserLogin.BridgeState.Send(status.BridgeState{
					StateEvent: status.StateTransientDisconnect,
					Error:      "zulip-queue-register-error",
					Info: map[string]any{
						"go_error": err.Error(),
					},
				})
				if !sleep() {
					return
				}
				continue
			}
			retryDelay = 5 * time.Second
			zc.UserLogin.BridgeState.Send(status.BridgeState{StateEvent: status.StateConnected})
			connectedSent = true
		}
		resp, err := rtc.GetEventsEventQueue(
			ctx, meta.QueueID, realtime.LastEventID(meta.LastEventID), realtime.DontBlock(!connectedSent),
		)
		if ctx.Err() != nil {
			return
		} else if err != nil {
			log.Err(err).Msg("Failed to poll event queue")
			if zulip.IsCode(err, zulip.ErrBadEventQueueID) {
				meta.QueueID = ""
				err = zc.UserLogin.Save(ctx)
				if err != nil {
					log.Err(err).Msg("Failed to save cleared queue ID")
				}
				continue
			}
			if pollFailingSince.IsZero() {
				pollFailingSince = time.Now()
			}
			if time.Since(pollFailingSince) >= pollErrorGracePeriod {
				zc.UserLogin.BridgeState.Send(status.BridgeState{
					StateEvent: status.StateTransientDisconnect,
					Error:      "zulip-event-poll-error",
					Info: map[string]any{
						"go_error": err.Error(),
					},
				})
				connectedSent = false
			}
			if !sleep() {
				return
			}
			continue
		}
		retryDelay = 5 * time.Second
		pollFailingSince = time.Time{}
		if !connectedSent {
			zc.UserLogin.BridgeState.Send(status.BridgeState{StateEvent: status.StateConnected})
			connectedSent = true
		}
		saveNeeded := len(resp.Events) > 0
		for _, evt := range resp.Events {
			ok := zc.handleZulipEvent(ctx, evt)
			if !ok {
				if evt.EventID() == failingEventID {
					failCount++
				} else {
					failingEventID, failCount = evt.EventID(), 1
				}
				if failCount < maxEventAttempts {
					log.Warn().Int("event_id", evt.EventID()).Int("attempt", failCount).Msg("Failed to handle event, retrying")
					if !sleep() {
						return
					}
					break
				}
				log.Error().Int("event_id", evt.EventID()).Msg("Failed to handle event repeatedly, skipping it")
			}
			if evt.EventID() >= 0 {
				meta.LastEventID = evt.EventID()
			}
		}
		if syncedMaxID := int(zc.pendingSyncedMaxID.Swap(0)); syncedMaxID > meta.SyncedMaxMessageID {
			meta.SyncedMaxMessageID = syncedMaxID
			saveNeeded = true
		}
		if saveNeeded {
			err = zc.UserLogin.Save(ctx)
			if err != nil {
				log.Err(err).Msg("Failed to save last event ID")
			}
		}
	}
}

var queueEventTypes = []events.EventType{
	events.MessageType,
	events.UpdateMessageType,
	events.DeleteMessageType,
	events.ReactionType,
	events.UpdateMessageFlagsType,
	events.TypingType,
	events.SubscriptionType,
	events.StreamType,
	events.UserTopicType,
	events.RealmUserType,
}

func (zc *ZulipClient) registerQueue(ctx context.Context, rtc *realtime.Service) error {
	resp, err := rtc.RegisterEventQueue(
		ctx,
		realtime.EventTypes(queueEventTypes...),
		realtime.FetchEventTypes(syncFetchEventTypes),
		realtime.ClientCapabilities(map[realtime.ClientCapability]bool{
			realtime.NotificationSettingsNull:   true,
			realtime.BulkMessageDeletion:        true,
			realtime.UserAvatarURLFieldOptional: true,
			realtime.StreamTypingNotifications:  true,
			realtime.UserSettingsObject:         true,
			realtime.LinkifierURLTemplate:       true,
			realtime.UserListIncomplete:         false,
			realtime.IncludeDeactivatedGroups:   false,
			realtime.ArchivedChannels:           false,
			realtime.EmptyTopicName:             true,
			realtime.SimplifiedPresenceEvents:   true,
		}),
		realtime.ClientGravatarEvent(true),
		realtime.ApplyMarkdown(true),
	)
	if err != nil {
		return fmt.Errorf("failed to register event queue: %w", err)
	}
	zerolog.Ctx(ctx).Debug().
		Str("queue_id", resp.QueueID).
		Int("last_event_id", resp.LastEventID).
		Int("max_message_id", resp.MaxMessageID).
		Str("zulip_version", resp.ZulipVersion).
		Int("feature_level", resp.ZulipFeatureLevel).
		Msg("Registered event queue")
	meta := zc.UserLogin.Metadata.(*zid.UserLoginMetadata)
	meta.QueueID = resp.QueueID
	meta.LastEventID = resp.LastEventID
	zc.queueID.Store(&resp.QueueID)
	zc.syncChats(ctx, resp)
	return nil
}
