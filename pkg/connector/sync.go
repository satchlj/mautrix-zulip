package connector

import (
	"cmp"
	"context"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/rs/zerolog"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/database"
	"maunium.net/go/mautrix/bridgev2/networkid"
	"maunium.net/go/mautrix/bridgev2/simplevent"

	"go.mau.fi/mautrix-zulip/pkg/zid"
	"go.mau.fi/mautrix-zulip/pkg/zulip/channels"
	"go.mau.fi/mautrix-zulip/pkg/zulip/realtime"
	"go.mau.fi/mautrix-zulip/pkg/zulip/realtime/events"
)

type syncCandidate struct {
	key   networkid.PortalKey
	maxID int
	muted bool
	// The first unread message ID, or 0 if there are no unread messages.
	firstUnread int
	// If true, Zulip didn't tell us whether the conversation has unread messages.
	readStateUnknown bool
}

func allUnreadIDLists(unread *realtime.UnreadMessages) [][]int {
	lists := make([][]int, 0, len(unread.Streams)+len(unread.PMs)+len(unread.Huddles))
	for _, st := range unread.Streams {
		lists = append(lists, st.UnreadMessageIDs)
	}
	for _, pm := range unread.PMs {
		lists = append(lists, pm.UnreadMessageIDs)
	}
	for _, huddle := range unread.Huddles {
		lists = append(lists, huddle.UnreadMessageIDs)
	}
	return lists
}

// syncChats runs after each event queue registration. On the first sync after login, it creates rooms for the
// most recently active conversations. On later syncs (i.e. when the event queue expired, for example because the
// bridge was offline), it catches up on everything that was missed: new messages are backfilled, rooms are
// created for conversations that became active, and the read state of all rooms is reconciled with Zulip.
func (zc *ZulipClient) syncChats(ctx context.Context, state *realtime.RegisterEventQueueResponse) {
	log := zerolog.Ctx(ctx).With().Str("action", "sync chats").Logger()
	ctx = log.WithContext(ctx)
	meta := zc.UserLogin.Metadata.(*zid.UserLoginMetadata)
	isFirstSync := meta.SyncedMaxMessageID == 0

	zc.cacheLock.Lock()
	clear(zc.streams)
	for _, sub := range state.Subscriptions {
		zc.streams[sub.StreamID] = &sub
	}
	clear(zc.topicPolicies)
	for _, ut := range state.UserTopics {
		zc.topicPolicies[topicRef{StreamID: ut.StreamID, TopicKey: zid.TopicKey(ut.TopicName)}] = ut.VisibilityPolicy
	}
	if state.RealmEmptyTopicDisplayName != "" {
		zc.emptyTopicName = state.RealmEmptyTopicDisplayName
	}
	zc.cacheLock.Unlock()

	candidates := zc.collectSyncCandidates(ctx, state)
	existingPortals := make(map[networkid.PortalKey]struct{})
	userPortals, err := zc.Main.Bridge.DB.UserPortal.GetAllForLogin(ctx, zc.UserLogin.UserLogin)
	if err != nil {
		log.Err(err).Msg("Failed to get existing portals")
		return
	}
	for _, up := range userPortals {
		existingPortals[up.Portal] = struct{}{}
	}

	type syncTask struct {
		cand   *syncCandidate
		create bool
	}
	var tasks []syncTask
	var created, resynced int
	for _, cand := range candidates {
		_, exists := existingPortals[cand.key]
		var shouldCreate bool
		if !exists && (!cand.muted || zc.Main.Config.BridgeMuted) {
			if isFirstSync {
				shouldCreate = created < zc.Main.Config.InitialChatLimit
			} else {
				// Conversations that had new messages while the bridge wasn't listening
				shouldCreate = cand.maxID > meta.SyncedMaxMessageID
			}
		}
		if shouldCreate {
			created++
		} else if !exists {
			continue
		} else if cand.maxID > meta.SyncedMaxMessageID {
			resynced++
		}
		tasks = append(tasks, syncTask{cand: cand, create: shouldCreate})
	}
	log.Info().
		Int("candidate_count", len(candidates)).
		Int("create_count", created).
		Int("backfill_count", resynced).
		Int("existing_count", len(existingPortals)).
		Bool("first_sync", isFirstSync).
		Msg("Planned chat sync")
	newSyncedMaxID := max(state.MaxMessageID, meta.SyncedMaxMessageID)

	// Room creation and backfilling make several requests to Zulip and the homeserver, so limit how many portals
	// are synced in parallel to avoid hitting rate limits. This runs in the background so that new events keep
	// flowing while the initial sync is in progress. Events for a portal are always handled in order, so a new
	// message arriving for a portal that hasn't been synced yet is fine.
	go func() {
		zc.syncLock.Lock()
		defer zc.syncLock.Unlock()
		const parallelSyncs = 3
		sema := make(chan struct{}, parallelSyncs)
		var wg sync.WaitGroup
		for _, task := range tasks {
			select {
			case sema <- struct{}{}:
			case <-ctx.Done():
				return
			}
			wg.Add(1)
			go func() {
				defer func() {
					<-sema
					wg.Done()
				}()
				zc.syncPortal(ctx, task.cand, task.create)
			}()
		}
		wg.Wait()
		// The poll loop owns the login metadata, so let it save the new value.
		zc.pendingSyncedMaxID.Store(int64(newSyncedMaxID))
		log.Info().Msg("Finished syncing chats")
	}()
}

func (zc *ZulipClient) syncPortal(ctx context.Context, cand *syncCandidate, create bool) {
	maxID := cand.maxID
	zc.UserLogin.QueueRemoteEvent(&simplevent.ChatResync{
		EventMeta: simplevent.EventMeta{
			Type:         bridgev2.RemoteEventChatResync,
			PortalKey:    cand.key,
			CreatePortal: create,
		},
		GetChatInfoFunc: zc.GetChatInfo,
		CheckNeedsBackfillFunc: func(ctx context.Context, latestMessage *database.Message) (bool, error) {
			return latestMessage == nil || zid.ParseMessageID(latestMessage.ID) < maxID, nil
		},
	})
	if cand.readStateUnknown && cand.firstUnread == 0 {
		// Don't touch the read state
	} else if cand.firstUnread == 0 {
		zc.queueReadReceipt(cand.key, cand.maxID)
	} else if cand.firstUnread > 1 {
		// Matrix read receipts can only mark everything up to a point as read,
		// so mark everything before the first unread message as read.
		zc.queueReadReceipt(cand.key, cand.firstUnread-1)
	}
	// Portal creation can fail, in which case the barrier event is dropped, so use a timeout here.
	zc.waitForPortalQueue(ctx, cand.key, 2*time.Minute)
}

func (zc *ZulipClient) collectSyncCandidates(ctx context.Context, state *realtime.RegisterEventQueueResponse) []*syncCandidate {
	log := zerolog.Ctx(ctx)
	candidateMap := make(map[networkid.PortalKey]*syncCandidate)
	srv := channels.NewService(zc.Client)
	for _, sub := range state.Subscriptions {
		resp, err := srv.GetChannelTopics(ctx, sub.StreamID)
		if err != nil {
			log.Err(err).Int("stream_id", sub.StreamID).Msg("Failed to get topics in channel")
			continue
		}
		for _, topic := range resp.Topics {
			zc.rememberTopicName(sub.StreamID, topic.Name)
			key := zc.makeTopicPortalKey(sub.StreamID, topic.Name)
			candidateMap[key] = &syncCandidate{
				key:   key,
				maxID: topic.MaxID,
				muted: zc.isMuted(sub.StreamID, &topic.Name),
			}
		}
	}
	for _, conv := range state.RecentPrivateConversations {
		key := zc.makeDMPortalKey(conv.UserIDs)
		candidateMap[key] = &syncCandidate{key: key, maxID: conv.MaxMessageID}
	}
	setUnread := func(key networkid.PortalKey, unreadIDs []int) {
		if len(unreadIDs) == 0 {
			return
		}
		cand, ok := candidateMap[key]
		if !ok {
			return
		}
		firstUnread := slices.Min(unreadIDs)
		if cand.firstUnread == 0 || firstUnread < cand.firstUnread {
			cand.firstUnread = firstUnread
		}
	}
	if unread := state.UnreadMsgs; unread != nil && unread.OldUnreadsMissing {
		// Zulip only lists the most recent unread messages, so conversations whose last message is older than
		// the oldest listed unread message may have unread messages too. Leave their read state alone.
		oldestListed := 0
		for _, ids := range allUnreadIDLists(unread) {
			for _, id := range ids {
				if oldestListed == 0 || id < oldestListed {
					oldestListed = id
				}
			}
		}
		for _, cand := range candidateMap {
			if cand.maxID < oldestListed {
				cand.readStateUnknown = true
			}
		}
	}
	if unread := state.UnreadMsgs; unread != nil {
		for _, st := range unread.Streams {
			setUnread(zc.makeTopicPortalKey(st.StreamID, st.Topic), st.UnreadMessageIDs)
		}
		for _, pm := range unread.PMs {
			setUnread(zc.makeDMPortalKey([]int{pm.OtherUserID}), pm.UnreadMessageIDs)
		}
		for _, huddle := range unread.Huddles {
			setUnread(zc.makeDMPortalKey(parseUserIDList(huddle.UserIDsString)), huddle.UnreadMessageIDs)
		}
	}
	candidates := make([]*syncCandidate, 0, len(candidateMap))
	for _, cand := range candidateMap {
		candidates = append(candidates, cand)
	}
	// Most recently active first
	slices.SortFunc(candidates, func(a, b *syncCandidate) int {
		return cmp.Compare(b.maxID, a.maxID)
	})
	return candidates
}

func parseUserIDList(list string) []int {
	parts := strings.Split(list, ",")
	ids := make([]int, 0, len(parts))
	for _, part := range parts {
		id, err := strconv.Atoi(strings.TrimSpace(part))
		if err == nil {
			ids = append(ids, id)
		}
	}
	return ids
}

var syncFetchEventTypes = []events.EventType{
	"message",
	"subscription",
	"recent_private_conversations",
	"update_message_flags",
	"user_topic",
	"realm",
}
