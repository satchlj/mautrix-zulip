package events

const UserTopicType EventType = "user_topic"

type UserTopic struct {
	ID   int       `json:"id"`
	Type EventType `json:"type"`
	UserTopicData
}

type UserTopicData struct {
	StreamID         int    `json:"stream_id"`
	TopicName        string `json:"topic_name"`
	LastUpdated      int    `json:"last_updated"`
	VisibilityPolicy int    `json:"visibility_policy"`
}

func (e *UserTopic) EventID() int {
	return e.ID
}

func (e *UserTopic) EventType() EventType {
	return e.Type
}

func (e *UserTopic) EventOp() string {
	return string(UserTopicType)
}

const (
	VisibilityPolicyNone     = 0
	VisibilityPolicyMuted    = 1
	VisibilityPolicyUnmuted  = 2
	VisibilityPolicyFollowed = 3
)
