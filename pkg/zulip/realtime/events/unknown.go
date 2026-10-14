package events

const UnknownType EventType = "unknown"

type Unknown map[string]any

func (e *Unknown) EventID() int {
	// The ID must be returned correctly even for unknown events,
	// otherwise the event queue position would be reset.
	if id, ok := (*e)["id"].(float64); ok {
		return int(id)
	}
	return -1
}

func (e *Unknown) EventType() EventType {
	return "unknown"
}

func (e *Unknown) EventOp() string {
	return "unknown"
}
