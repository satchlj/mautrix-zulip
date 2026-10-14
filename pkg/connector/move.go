package connector

import (
	"context"
	"net/http"
	"slices"
	"strconv"
	"time"

	"github.com/rs/zerolog"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/database"
	"maunium.net/go/mautrix/bridgev2/networkid"
	"maunium.net/go/mautrix/bridgev2/simplevent"

	"go.mau.fi/mautrix-zulip/pkg/zid"
	"go.mau.fi/mautrix-zulip/pkg/zulip"
	"go.mau.fi/mautrix-zulip/pkg/zulip/messages"
	"go.mau.fi/mautrix-zulip/pkg/zulip/realtime/events"
)

// handleMove handles messages being moved to a different topic and/or channel.
//
// Since each topic is a separate room, there are two cases:
//   - The entire topic was moved (renamed or moved to another channel): the existing room is re-keyed to the new
//     topic and renamed. If the target topic already had a room, the source room is merged (tombstoned) into it,
//     and the moved messages are re-bridged in the target room.
//   - Only some messages were moved: the moved messages are deleted from the source room and re-bridged in the
//     room of the target topic.
func (zc *ZulipClient) handleMove(ctx context.Context, evt *events.UpdateMessage) bool {
	oldStreamID := *evt.StreamID
	newStreamID := oldStreamID
	if evt.NewStreamID != nil {
		newStreamID = *evt.NewStreamID
	}
	oldTopic := *evt.OrigSubject
	newTopic := oldTopic
	if evt.Subject != nil {
		newTopic = *evt.Subject
	}
	movedIDs := evt.MessageIDs
	if len(movedIDs) == 0 {
		movedIDs = []int{evt.MessageID}
	}
	slices.Sort(movedIDs)
	source := zc.makeTopicPortalKey(oldStreamID, oldTopic)
	target := zc.makeTopicPortalKey(newStreamID, newTopic)
	log := zerolog.Ctx(ctx).With().
		Str("action", "handle move").
		Stringer("source_portal", source).
		Stringer("target_portal", target).
		Str("propagate_mode", ptrStr(evt.PropagateMode)).
		Int("moved_count", len(movedIDs)).
		Logger()
	ctx = log.WithContext(ctx)
	zc.rememberTopicName(newStreamID, newTopic)
	if source == target {
		// Only the case of the topic name changed, so just update the room name.
		zc.queueInfoResync(ctx, target)
		return true
	}

	sourcePortal, err := zc.Main.Bridge.GetExistingPortalByKey(ctx, source)
	if err != nil {
		log.Err(err).Msg("Failed to get source portal of move")
		return false
	} else if sourcePortal == nil || sourcePortal.MXID == "" {
		log.Debug().Msg("Source portal doesn't exist, bridging most recent moved messages as new messages")
		return zc.rebridgeMovedMessages(ctx, movedIDs[max(0, len(movedIDs)-maxRebridgedMessages):])
	}
	// Make sure all previously queued events (e.g. edits) have been handled before touching the source portal.
	zc.waitForPortalQueue(ctx, source, 0)

	// Only messages that were actually bridged into the source room need to be moved on Matrix.
	bridgedMoved := make([]*database.Message, 0, len(movedIDs))
	for _, msgID := range movedIDs {
		existing, err := zc.Main.Bridge.DB.Message.GetFirstPartByID(ctx, zc.UserLogin.ID, zid.MakeMessageID(msgID))
		if err != nil {
			log.Err(err).Msg("Failed to get moved message from database")
			return false
		} else if existing != nil && existing.Room == source {
			bridgedMoved = append(bridgedMoved, existing)
		}
	}
	bridgedIDs := make([]int, len(bridgedMoved))
	for i, msg := range bridgedMoved {
		bridgedIDs[i] = zid.ParseMessageID(msg.ID)
	}
	if len(bridgedIDs) > maxRebridgedMessages {
		bridgedIDs = bridgedIDs[len(bridgedIDs)-maxRebridgedMessages:]
	}

	wholeTopic := ptrStr(evt.PropagateMode) == string(messages.PropagateModeAll)
	if !wholeTopic {
		// If every message that's bridged in the source room was moved, treat it as a whole topic move too.
		count, err := zc.Main.Bridge.DB.Message.CountMessagesInPortal(ctx, source)
		if err != nil {
			log.Err(err).Msg("Failed to count messages in source portal")
		} else if len(bridgedMoved) > 0 && count <= len(bridgedMoved) {
			wholeTopic = true
		}
	}

	if wholeTopic {
		result, _, err := zc.Main.Bridge.ReIDPortal(ctx, source, target)
		if err != nil {
			log.Err(err).Msg("Failed to re-ID portal after topic move")
			return false
		}
		log.Debug().Stringer("result", result).Msg("Re-ID'd portal after topic move")
		zc.queueInfoResync(ctx, target)
		if result == bridgev2.ReIDResultSourceTombstonedIntoTarget {
			// The source room was merged into an existing room for the target topic,
			// so the messages need to be bridged again in the target room.
			return zc.rebridgeMovedMessages(ctx, bridgedIDs)
		}
		return true
	}

	for _, msg := range bridgedMoved {
		res := zc.UserLogin.QueueRemoteEvent(&simplevent.MessageRemove{
			EventMeta: simplevent.EventMeta{
				Type:      bridgev2.RemoteEventMessageRemove,
				PortalKey: source,
				// Redact as the original sender, as the user who moved the message may not have permission.
				Sender: zc.makeEventSender(zid.ParseUserID(msg.SenderID)),
			},
			TargetMessage: msg.ID,
		})
		if !isSuccess(res) {
			return false
		}
	}
	// Wait for the removals to finish so the message IDs are free again before re-bridging the messages.
	zc.waitForPortalQueue(ctx, source, 0)
	return zc.rebridgeMovedMessages(ctx, bridgedIDs)
}

// maxRebridgedMessages limits how many moved messages are re-sent into the target room.
// Re-bridged messages show up at the end of the room, so re-sending a whole history isn't useful.
const maxRebridgedMessages = 50

func ptrStr[T ~string](val *T) string {
	if val == nil {
		return ""
	}
	return string(*val)
}

// rebridgeMovedMessages fetches messages from Zulip and bridges them as new messages into their current room.
func (zc *ZulipClient) rebridgeMovedMessages(ctx context.Context, messageIDs []int) bool {
	if len(messageIDs) == 0 {
		return true
	}
	fetched, err := zc.fetchMessagesByID(ctx, messageIDs)
	if err != nil {
		zerolog.Ctx(ctx).Err(err).Msg("Failed to fetch moved messages")
		// Don't block the event stream forever if fetching fails
		return true
	}
	for _, msg := range fetched {
		data := restMessageToEvent(&msg)
		if !isSuccess(zc.UserLogin.QueueRemoteEvent(zc.makeMessageEvent(data, ""))) {
			return false
		}
	}
	return true
}

func (zc *ZulipClient) fetchMessagesByID(ctx context.Context, messageIDs []int) ([]messages.Message, error) {
	srv := messages.NewService(zc.Client)
	output := make([]messages.Message, 0, len(messageIDs))
	for chunk := range slices.Chunk(messageIDs, 100) {
		resp, err := srv.GetMessages(
			ctx,
			messages.MessageIDs(chunk),
			messages.ApplyMarkdownMessage(true),
			messages.AllowEmptyTopicName(),
		)
		if zulip.HTTPStatus(err) == http.StatusBadRequest {
			// The message_ids parameter requires Zulip 10.0, fall back to fetching messages one by one
			for _, msgID := range chunk {
				resp, err = srv.GetMessages(
					ctx,
					messages.Anchor(strconv.Itoa(msgID)),
					messages.NumBefore(0),
					messages.NumAfter(0),
					messages.ApplyMarkdownMessage(true),
					messages.AllowEmptyTopicName(),
				)
				if err != nil {
					return output, err
				}
				output = append(output, resp.Messages...)
			}
			continue
		} else if err != nil {
			return output, err
		}
		output = append(output, resp.Messages...)
	}
	return output, nil
}

// waitForPortalQueue waits until all events that have been queued for a portal so far have been handled.
// If timeout is zero, it waits until the context is canceled.
func (zc *ZulipClient) waitForPortalQueue(ctx context.Context, portalKey networkid.PortalKey, timeout time.Duration) {
	done := make(chan struct{})
	res := zc.UserLogin.QueueRemoteEvent(&simplevent.EventMeta{
		Type:      bridgev2.RemoteEventUnknown,
		PortalKey: portalKey,
		PreHandleFunc: func(ctx context.Context, portal *bridgev2.Portal) {
			close(done)
		},
	})
	if !res.Queued {
		// Either handled synchronously or dropped, nothing to wait for.
		return
	}
	var timeoutChan <-chan time.Time
	if timeout > 0 {
		timeoutChan = time.After(timeout)
	}
	select {
	case <-done:
	case <-timeoutChan:
		zerolog.Ctx(ctx).Warn().Stringer("portal_key", portalKey).Msg("Timed out waiting for portal event queue")
	case <-ctx.Done():
	}
}
