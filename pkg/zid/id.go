package zid

import (
	"fmt"
	"slices"
	"strconv"
	"strings"

	"go.mau.fi/util/exslices"
	"maunium.net/go/mautrix/bridgev2/networkid"
)

func MakeMessageID(id int) networkid.MessageID {
	return networkid.MessageID(strconv.Itoa(id))
}

func ParseMessageID(id networkid.MessageID) int {
	n, _ := strconv.Atoi(string(id))
	return n
}

func ParseUserLoginID(id networkid.UserLoginID) int {
	n, _ := strconv.Atoi(string(id))
	return n
}

func MakeUserLoginID(id int) networkid.UserLoginID {
	return networkid.UserLoginID(strconv.Itoa(id))
}

func ParseUserID(id networkid.UserID) int {
	n, _ := strconv.Atoi(string(id))
	return n
}

func MakeUserID(id int) networkid.UserID {
	return networkid.UserID(strconv.Itoa(id))
}

// TopicKey normalizes a topic name for use in portal IDs. Zulip topic names are case-insensitive,
// so messages sent to "Foo" and "foo" end up in the same topic.
func TopicKey(topic string) string {
	return strings.ToLower(topic)
}

// MakeTopicPortalID creates a portal ID for a single topic in a channel. Each topic is bridged as its own room.
func MakeTopicPortalID(streamID int, topic string) networkid.PortalID {
	return networkid.PortalID(fmt.Sprintf("topic:%d:%s", streamID, TopicKey(topic)))
}

// MakeDMPortalID creates a portal ID for a direct message conversation.
// The user IDs must not include the logged-in user, except for the self-DM, where it's the only user.
func MakeDMPortalID(users []int) networkid.PortalID {
	users = slices.Clone(users)
	slices.Sort(users)
	users = slices.Compact(users)
	userStrings := exslices.CastFunc(users, func(from int) string {
		return strconv.Itoa(from)
	})
	return networkid.PortalID(fmt.Sprintf("dm:%s", strings.Join(userStrings, ",")))
}

type ParsedPortalID struct {
	// For channel topics
	StreamID int
	TopicKey string
	// For direct messages
	UserIDs []int
}

func (p *ParsedPortalID) IsDM() bool {
	return p.UserIDs != nil
}

func ParsePortalID(portalID networkid.PortalID) (parsed ParsedPortalID, err error) {
	prefix, rest, ok := strings.Cut(string(portalID), ":")
	if !ok {
		err = fmt.Errorf("invalid portal ID: %s", portalID)
		return
	}
	switch prefix {
	case "topic":
		streamIDStr, topicKey, ok := strings.Cut(rest, ":")
		if !ok {
			err = fmt.Errorf("invalid topic portal ID: %s", portalID)
			return
		}
		parsed.StreamID, err = strconv.Atoi(streamIDStr)
		if err != nil {
			err = fmt.Errorf("invalid topic portal ID: %s", portalID)
			return
		}
		parsed.TopicKey = topicKey
	case "dm":
		userParts := strings.Split(rest, ",")
		parsed.UserIDs = make([]int, len(userParts))
		for i, up := range userParts {
			parsed.UserIDs[i], err = strconv.Atoi(up)
			if err != nil {
				err = fmt.Errorf("invalid DM portal ID: %s", portalID)
				return
			}
		}
	default:
		err = fmt.Errorf("invalid portal ID prefix: %s", portalID)
	}
	return
}
