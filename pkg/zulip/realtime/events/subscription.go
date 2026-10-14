package events

const SubscriptionType EventType = "subscription"

type Subscription struct {
	ID            int                `json:"id"`
	Type          EventType          `json:"type"`
	Op            string             `json:"op"`
	Subscriptions []SubscriptionData `json:"subscriptions"`

	// Fields for op=update
	StreamID int    `json:"stream_id"`
	Property string `json:"property"`
	Value    any    `json:"value"`
}

type SubscriptionData struct {
	StreamID    int    `json:"stream_id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	IsMuted     bool   `json:"is_muted"`
	InHomeView  *bool  `json:"in_home_view,omitempty"`
}

func (s *SubscriptionData) Muted() bool {
	if s.InHomeView != nil && !*s.InHomeView {
		return true
	}
	return s.IsMuted
}

func (e *Subscription) EventID() int {
	return e.ID
}

func (e *Subscription) EventType() EventType {
	return e.Type
}

func (e *Subscription) EventOp() string {
	return e.Op
}
