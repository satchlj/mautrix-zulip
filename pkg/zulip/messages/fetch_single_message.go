package messages

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"go.mau.fi/mautrix-zulip/pkg/zulip"
)

type FetchSingleMessageResponse struct {
	zulip.APIResponseBase
	fetchSingleMessageResponseData
}

type fetchSingleMessageResponseData struct {
	Message    Message `json:"message"`
	RawContent string  `json:"raw_content"`
}

func (f *FetchSingleMessageResponse) UnmarshalJSON(b []byte) error {
	if err := json.Unmarshal(b, &f.APIResponseBase); err != nil {
		return err
	}

	if err := json.Unmarshal(b, &f.fetchSingleMessageResponseData); err != nil {
		return err
	}

	return nil
}

type fetchSingleMessageOptions struct {
	applyMarkdown struct {
		fieldName string
		value     *bool
	}
	allowEmptyTopicName bool
}

type FetchSingleMessageOption func(*fetchSingleMessageOptions)

func ApplyMarkdownSingleMessage(applyMarkdown bool) FetchSingleMessageOption {
	return func(o *fetchSingleMessageOptions) {
		o.applyMarkdown.fieldName = "apply_markdown"
		o.applyMarkdown.value = &applyMarkdown
	}
}

// AllowEmptyTopicNameSingleMessage makes the server return the actual empty topic name instead of
// realm_empty_topic_display_name for messages in the "general chat" topic.
func AllowEmptyTopicNameSingleMessage() FetchSingleMessageOption {
	return func(o *fetchSingleMessageOptions) {
		o.allowEmptyTopicName = true
	}
}

func (svc *Service) FetchSingleMessage(ctx context.Context, messageID int, options ...FetchSingleMessageOption) (*FetchSingleMessageResponse, error) {
	const (
		method = http.MethodGet
		path   = "/api/v1/messages"
	)

	patchPath := fmt.Sprintf("%s/%d", path, messageID)

	msg := map[string]any{}

	opts := fetchSingleMessageOptions{}
	for _, opt := range options {
		opt(&opts)
	}

	if opts.applyMarkdown.value != nil {
		msg[opts.applyMarkdown.fieldName] = *opts.applyMarkdown.value
	}

	if opts.allowEmptyTopicName {
		msg["allow_empty_topic_name"] = true
	}

	resp := FetchSingleMessageResponse{}
	if err := svc.client.DoRequest(ctx, method, patchPath, msg, &resp); err != nil {
		return nil, err
	}

	return &resp, nil
}
