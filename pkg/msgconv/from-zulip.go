package msgconv

import (
	"context"
	"encoding/json"
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/gabriel-vasile/mimetype"
	"github.com/rs/zerolog"
	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/database"
	"maunium.net/go/mautrix/bridgev2/networkid"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/format"
	"maunium.net/go/mautrix/id"

	"go.mau.fi/mautrix-zulip/pkg/msgconv/zuliphtml"
	"go.mau.fi/mautrix-zulip/pkg/zid"
	"go.mau.fi/mautrix-zulip/pkg/zulip/realtime/events"
)

// MathRenderer renders LaTeX into images uploaded to Matrix.
type MathRenderer interface {
	// RenderMath returns nil if rendering fails, in which case the LaTeX source is used instead.
	RenderMath(ctx context.Context, intent bridgev2.MatrixAPI, roomID id.RoomID, tex string, display bool) *zuliphtml.MathImage
	MathMode() zuliphtml.MathMode
}

func ToMatrix(
	ctx context.Context, portal *bridgev2.Portal, intent bridgev2.MatrixAPI, source *bridgev2.UserLogin, data *events.MessageData,
	mathRenderer MathRenderer,
) (*bridgev2.ConvertedMessage, error) {
	meta := source.Metadata.(*zid.UserLoginMetadata)
	var replyTo *networkid.MessageOptionalPartID
	replyTargetExists := func(messageID int) bool {
		target, err := portal.Bridge.DB.Message.GetFirstPartByID(ctx, source.ID, zid.MakeMessageID(messageID))
		if err != nil {
			zerolog.Ctx(ctx).Err(err).Int("reply_to_id", messageID).Msg("Failed to check if reply target exists")
		} else if target != nil && target.Room == portal.PortalKey {
			replyTo = &networkid.MessageOptionalPartID{MessageID: target.ID}
			return true
		}
		return false
	}
	parseOpts := zuliphtml.Options{ReplyTargetExists: replyTargetExists}
	if mathRenderer != nil {
		parseOpts.MathMode = mathRenderer.MathMode()
		parseOpts.RenderMath = func(tex string, display bool) *zuliphtml.MathImage {
			return mathRenderer.RenderMath(ctx, intent, portal.MXID, tex, display)
		}
	}
	html, attachments, mentions, err := zuliphtml.Parse(ctx, portal.Bridge, strings.TrimSuffix(meta.URL, "/"), data.Content, parseOpts)
	if err != nil {
		return nil, err
	}
	content := format.HTMLToContent(html)
	content.Mentions = mentions
	limitContentSize(&content)
	if data.IsMeMessage {
		content.MsgType = event.MsgEmote
		content.Body = strings.TrimPrefix(content.Body, "/me ")
		content.FormattedBody = strings.Replace(content.FormattedBody, "/me ", "", 1)
	}
	textPart := &bridgev2.ConvertedMessagePart{
		Type:    event.EventMessage,
		Content: &content,
	}
	hasText := textPart.Content.Body != "" || textPart.Content.FormattedBody != ""
	var parts []*bridgev2.ConvertedMessagePart
	if len(attachments) == 0 {
		parts = []*bridgev2.ConvertedMessagePart{textPart}
	} else if len(attachments) == 1 && hasText {
		mediaPart := attachmentToMatrix(ctx, portal.MXID, intent, meta, attachments[0])
		parts = []*bridgev2.ConvertedMessagePart{bridgev2.MergeCaption(textPart, mediaPart)}
	} else {
		parts = make([]*bridgev2.ConvertedMessagePart, 0, len(attachments)+1)
		if hasText {
			parts = append(parts, textPart)
		}
		for _, att := range attachments {
			parts = append(parts, attachmentToMatrix(ctx, portal.MXID, intent, meta, att))
		}
	}
	for i, part := range parts {
		if i > 0 {
			part.ID = networkid.PartID(strconv.Itoa(i))
		}
	}
	return &bridgev2.ConvertedMessage{
		ReplyTo: replyTo,
		Parts:   parts,
	}, nil
}

// MakeEdit converts a re-rendered message into an edit of the existing message parts.
func MakeEdit(existing []*database.Message, converted *bridgev2.ConvertedMessage) *bridgev2.ConvertedEdit {
	existingByID := make(map[networkid.PartID]*database.Message, len(existing))
	for _, part := range existing {
		existingByID[part.PartID] = part
	}
	edit := &bridgev2.ConvertedEdit{}
	for _, part := range converted.Parts {
		existingPart, ok := existingByID[part.ID]
		if ok {
			edit.ModifiedParts = append(edit.ModifiedParts, part.ToEditPart(existingPart))
			delete(existingByID, part.ID)
		} else {
			if edit.AddedParts == nil {
				edit.AddedParts = &bridgev2.ConvertedMessage{}
			}
			edit.AddedParts.Parts = append(edit.AddedParts.Parts, part)
		}
	}
	for _, part := range existing {
		if _, stillExists := existingByID[part.PartID]; stillExists {
			edit.DeletedParts = append(edit.DeletedParts, part)
		}
	}
	return edit
}

var MediaClient = &http.Client{
	Timeout: 60 * time.Second,
}

func attachmentToMatrix(
	ctx context.Context, roomID id.RoomID, intent bridgev2.MatrixAPI, meta *zid.UserLoginMetadata,
	attachment zuliphtml.Attachment,
) *bridgev2.ConvertedMessagePart {
	if attachment.Math != nil {
		return &bridgev2.ConvertedMessagePart{
			Type: event.EventMessage,
			Content: &event.MessageEventContent{
				MsgType:  event.MsgImage,
				Body:     attachment.FileName,
				FileName: attachment.FileName,
				URL:      attachment.Math.URL,
				Info: &event.FileInfo{
					MimeType: "image/png",
					Width:    attachment.Math.Width,
					Height:   attachment.Math.Height,
				},
			},
		}
	}
	part, err := attachmentToMatrixWithErrors(ctx, roomID, intent, meta, attachment)
	if err != nil {
		return &bridgev2.ConvertedMessagePart{
			Type: event.EventMessage,
			Content: &event.MessageEventContent{
				MsgType: event.MsgNotice,
				Body:    fmt.Sprintf("Failed to fetch attachment: %v", err),
			},
		}
	}
	return part
}

func attachmentToMatrixWithErrors(
	ctx context.Context, roomID id.RoomID, intent bridgev2.MatrixAPI, meta *zid.UserLoginMetadata,
	attachment zuliphtml.Attachment,
) (*bridgev2.ConvertedMessagePart, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, attachment.URL, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to prepare request: %w", err)
	}
	req.Header.Set("User-Agent", mautrix.DefaultUserAgent)
	req.Header.Set("Accept", "*/*")
	if strings.HasPrefix(attachment.URL, meta.URL) {
		req.SetBasicAuth(meta.Email, meta.Token)
	}
	content := &event.MessageEventContent{
		MsgType: attachment.MsgType,
		Body:    attachment.FileName,
		Info:    &event.FileInfo{},
	}
	resp, err := MediaClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() {
		_ = resp.Body.Close()
	}()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("unexpected status code %d", resp.StatusCode)
	}
	content.Info.MimeType = resp.Header.Get("Content-Type")
	content.URL, content.File, err = intent.UploadMediaStream(ctx, roomID, -1, true, func(file io.Writer) (*bridgev2.FileStreamResult, error) {
		n, err := io.Copy(file, resp.Body)
		if err != nil {
			return nil, err
		}
		realFile := file.(*os.File)
		// TODO this is probably unnecessary
		if content.Info.MimeType == "" {
			_, err = realFile.Seek(0, io.SeekStart)
			if err != nil {
				return nil, fmt.Errorf("failed to seek to start: %w", err)
			}
			mime, err := mimetype.DetectReader(realFile)
			if err != nil {
				return nil, fmt.Errorf("failed to detect mime type: %w", err)
			}
			content.Info.MimeType = mime.String()
		}
		if attachment.MsgType == event.MsgImage {
			_, err = realFile.Seek(0, io.SeekStart)
			if err != nil {
				return nil, fmt.Errorf("failed to seek to start: %w", err)
			}
			cfg, _, err := image.DecodeConfig(realFile)
			if err != nil {
				zerolog.Ctx(ctx).Warn().Err(err).Msg("Failed to decode image config")
			} else {
				content.Info.Width = cfg.Width
				content.Info.Height = cfg.Height
			}
		}
		content.Info.Size = int(n)
		return &bridgev2.FileStreamResult{
			FileName: attachment.FileName,
			MimeType: content.Info.MimeType,
		}, nil
	})
	if err != nil {
		return nil, err
	}
	return &bridgev2.ConvertedMessagePart{
		Type:    event.EventMessage,
		Content: content,
	}, nil
}

// Matrix events are limited to 64 KiB, so leave plenty of room for the rest of the event.
const maxContentBytes = 40 * 1024

// jsonLen returns the length of a string after JSON encoding, which escapes characters like < and > as \u003c.
func jsonLen(s string) int {
	data, _ := json.Marshal(s)
	return len(data)
}

// limitContentSize drops the formatted body and truncates the plaintext body if a message is too large to send.
func limitContentSize(content *event.MessageEventContent) {
	if jsonLen(content.Body)+jsonLen(content.FormattedBody) <= maxContentBytes {
		return
	}
	content.Format = ""
	content.FormattedBody = ""
	if jsonLen(content.Body) > maxContentBytes {
		// Worst case every byte expands to 6 bytes when encoded
		cut := min(len(content.Body), maxContentBytes/6)
		for cut > 0 && !utf8.RuneStart(content.Body[cut]) {
			cut--
		}
		content.Body = content.Body[:cut] + "\n\n[message truncated]"
	}
}
