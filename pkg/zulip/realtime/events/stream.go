package events

const StreamType EventType = "stream"

type Stream struct {
	ID       int       `json:"id"`
	Type     EventType `json:"type"`
	Op       string    `json:"op"`
	StreamID int       `json:"stream_id"`
	Name     string    `json:"name"`
	Property string    `json:"property"`
	Value    any       `json:"value"`
}

func (e *Stream) EventID() int {
	return e.ID
}

func (e *Stream) EventType() EventType {
	return e.Type
}

func (e *Stream) EventOp() string {
	return e.Op
}
