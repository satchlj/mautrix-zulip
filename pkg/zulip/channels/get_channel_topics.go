package channels

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"go.mau.fi/mautrix-zulip/pkg/zulip"
)

type GetChannelTopicsResponse struct {
	zulip.APIResponseBase
	getChannelTopicsResponseData
}

type getChannelTopicsResponseData struct {
	Topics []Topic `json:"topics"`
}

type Topic struct {
	MaxID int    `json:"max_id"`
	Name  string `json:"name"`
}

func (g *GetChannelTopicsResponse) UnmarshalJSON(b []byte) error {
	if err := json.Unmarshal(b, &g.APIResponseBase); err != nil {
		return err
	}

	if err := json.Unmarshal(b, &g.getChannelTopicsResponseData); err != nil {
		return err
	}

	return nil
}

// GetChannelTopics fetches all topics in a channel, sorted by most recent activity.
func (svc *Service) GetChannelTopics(ctx context.Context, streamID int) (*GetChannelTopicsResponse, error) {
	const method = http.MethodGet
	path := fmt.Sprintf("/api/v1/users/me/%d/topics", streamID)

	// Topics are returned in their actual case only if empty_topic_name is allowed.
	msg := map[string]any{
		"allow_empty_topic_name": true,
	}

	resp := GetChannelTopicsResponse{}
	if err := svc.client.DoRequest(ctx, method, path, msg, &resp); err != nil {
		return nil, err
	}

	return &resp, nil
}
