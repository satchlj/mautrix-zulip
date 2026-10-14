package zid

type UserLoginMetadata struct {
	URL   string `json:"url"`
	Email string `json:"email"`
	Token string `json:"token"`

	QueueID     string `json:"queue_id,omitempty"`
	LastEventID int    `json:"last_event_id,omitempty"`
	// The max_message_id at the time of the last successful initial sync.
	// Topics with newer messages than this were active while the bridge wasn't listening.
	SyncedMaxMessageID int `json:"synced_max_message_id,omitempty"`
}

type PortalMetadata struct {
	// The case-preserved topic name. The portal ID only contains the lowercased version.
	Topic string `json:"topic,omitempty"`
}
