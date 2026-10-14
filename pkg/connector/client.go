package connector

import (
	"context"
	"log/slog"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	slogzerolog "github.com/samber/slog-zerolog/v2"
	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/networkid"

	"go.mau.fi/mautrix-zulip/pkg/zid"
	"go.mau.fi/mautrix-zulip/pkg/zulip"
	"go.mau.fi/mautrix-zulip/pkg/zulip/realtime/events"
)

type ZulipClient struct {
	Main      *ZulipConnector
	UserLogin *bridgev2.UserLogin
	Client    *zulip.Client

	stopPoll    atomic.Pointer[context.CancelFunc]
	pollStopped atomic.Pointer[chan struct{}]
	ownUserID   int

	cacheLock sync.RWMutex
	// Subscribed channels by ID
	streams map[int]*events.SubscriptionData
	// Topic visibility policies, keyed by stream ID and lowercased topic name
	topicPolicies map[topicRef]int
	// Case-preserved topic names, keyed by portal ID
	topicNames map[networkid.PortalID]string
	// Portals that were marked as unread from Zulip, so that the flag can be cleared when they're read
	markedUnread map[networkid.PortalKey]struct{}

	emptyTopicName string
	syncLock       sync.Mutex
	// Message IDs edited from Matrix, used to ignore the echo of the edit
	recentMatrixEdits sync.Map
	// The current event queue ID, used as the queue_id when sending messages
	queueID atomic.Pointer[string]
	// Sender names and avatars seen in messages, used for users that can't be fetched (e.g. system bots)
	senderInfo sync.Map
	// Set by background chat syncs when they finish, saved into the login metadata by the poll loop
	pendingSyncedMaxID atomic.Int64
}

type topicRef struct {
	StreamID int
	TopicKey string
}

func (zc *ZulipConnector) LoadUserLogin(ctx context.Context, login *bridgev2.UserLogin) error {
	meta := login.Metadata.(*zid.UserLoginMetadata)
	httpClient := &http.Client{Timeout: 180 * time.Second}
	zulipLog := login.Log.With().Str("component", "zulip").Logger()
	cli, err := zulip.NewClient(
		zulip.Credentials(meta.URL, meta.Email, meta.Token),
		zulip.WithCustomUserAgent(mautrix.DefaultUserAgent),
		zulip.WithLogger(slog.New(slogzerolog.Option{Logger: &zulipLog}.NewZerologHandler())),
		zulip.WithHTTPClient(httpClient),
	)
	if err != nil {
		return err
	}
	login.Client = &ZulipClient{
		Main:      zc,
		Client:    cli,
		UserLogin: login,
		ownUserID: zid.ParseUserLoginID(login.ID),

		streams:        make(map[int]*events.SubscriptionData),
		topicPolicies:  make(map[topicRef]int),
		topicNames:     make(map[networkid.PortalID]string),
		markedUnread:   make(map[networkid.PortalKey]struct{}),
		emptyTopicName: "general chat",
	}
	return nil
}

var _ bridgev2.NetworkAPI = (*ZulipClient)(nil)

func (zc *ZulipClient) Connect(ctx context.Context) {
	go zc.pollQueue(ctx)
}

func (zc *ZulipClient) Disconnect() {
	pollStop := zc.pollStopped.Load()
	if cancel := zc.stopPoll.Swap(nil); cancel != nil {
		(*cancel)()
	}
	if pollStop != nil {
		select {
		case <-*pollStop:
		case <-time.After(5 * time.Second):
			zc.UserLogin.Log.Warn().Msg("Timed out waiting for poll to stop")
		}
	}
}

func (zc *ZulipClient) IsLoggedIn() bool {
	return zc.Client != nil
}

func (zc *ZulipClient) LogoutRemote(ctx context.Context) {
	// We don't own the api token, so don't do anything
}

func (zc *ZulipClient) getStream(streamID int) *events.SubscriptionData {
	zc.cacheLock.RLock()
	defer zc.cacheLock.RUnlock()
	return zc.streams[streamID]
}

func (zc *ZulipClient) setStream(stream events.SubscriptionData) {
	zc.cacheLock.Lock()
	defer zc.cacheLock.Unlock()
	zc.streams[stream.StreamID] = &stream
}

func (zc *ZulipClient) rememberTopicName(streamID int, topic string) {
	zc.cacheLock.Lock()
	defer zc.cacheLock.Unlock()
	zc.topicNames[zid.MakeTopicPortalID(streamID, topic)] = topic
}

// isMuted returns whether the given stream or topic is muted on Zulip. Use an empty topic for the stream itself.
func (zc *ZulipClient) isMuted(streamID int, topic *string) bool {
	zc.cacheLock.RLock()
	defer zc.cacheLock.RUnlock()
	streamMuted := false
	if stream := zc.streams[streamID]; stream != nil {
		streamMuted = stream.Muted()
	}
	if topic == nil {
		return streamMuted
	}
	switch zc.topicPolicies[topicRef{StreamID: streamID, TopicKey: zid.TopicKey(*topic)}] {
	case events.VisibilityPolicyMuted:
		return true
	case events.VisibilityPolicyUnmuted, events.VisibilityPolicyFollowed:
		return false
	default:
		return streamMuted
	}
}
