package events

const UpdateMessageFlagsType EventType = "update_message_flags"

type UpdateMessageFlags struct {
	ID        int       `json:"id"`
	Type      EventType `json:"type"`
	Op        string    `json:"op"`
	Operation string    `json:"operation"`
	Flag      string    `json:"flag"`
	Messages  []int     `json:"messages"`
	All       bool      `json:"all"`
	// Only present when op is "remove" and flag is "read".
	MessageDetails map[string]MessageFlagDetails `json:"message_details"`
}

type MessageFlagDetails struct {
	Type      string `json:"type"`
	Mentioned bool   `json:"mentioned"`
	UserIDs   []int  `json:"user_ids"`
	StreamID  int    `json:"stream_id"`
	Topic     string `json:"topic"`
}

func (e *UpdateMessageFlags) EventID() int {
	return e.ID
}

func (e *UpdateMessageFlags) EventType() EventType {
	return e.Type
}

func (e *UpdateMessageFlags) EventOp() string {
	if e.Op == "" {
		return e.Operation
	}
	return e.Op
}
