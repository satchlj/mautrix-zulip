package messages

import (
	"context"
	"encoding/json"
	"net/http"

	"go.mau.fi/mautrix-zulip/pkg/zulip"
)

type SetTypingStatusResponse struct {
	zulip.APIResponseBase
}

type TypingOp string

const (
	TypingStart TypingOp = "start"
	TypingStop  TypingOp = "stop"
)

func (svc *Service) setTypingStatus(ctx context.Context, msg map[string]any) error {
	resp := SetTypingStatusResponse{}
	return svc.client.DoRequest(ctx, http.MethodPost, "/api/v1/typing", msg, &resp)
}

// SetTypingStatusDirect sends a typing notification to a direct message conversation.
func (svc *Service) SetTypingStatusDirect(ctx context.Context, op TypingOp, userIDs []int) error {
	to, err := json.Marshal(userIDs)
	if err != nil {
		return err
	}
	return svc.setTypingStatus(ctx, map[string]any{
		"op":   op,
		"type": "direct",
		"to":   string(to),
	})
}

// SetTypingStatusTopic sends a typing notification to a channel topic.
func (svc *Service) SetTypingStatusTopic(ctx context.Context, op TypingOp, streamID int, topic string) error {
	return svc.setTypingStatus(ctx, map[string]any{
		"op":        op,
		"type":      "stream",
		"stream_id": streamID,
		"topic":     topic,
	})
}
