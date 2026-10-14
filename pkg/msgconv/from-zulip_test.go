package msgconv

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"maunium.net/go/mautrix/event"
)

func TestLimitContentSize(t *testing.T) {
	content := &event.MessageEventContent{
		Body:          "short",
		Format:        event.FormatHTML,
		FormattedBody: strings.Repeat("<span></span>", 3000),
	}
	limitContentSize(content)
	assert.Empty(t, content.FormattedBody)
	assert.Equal(t, "short", content.Body)

	content = &event.MessageEventContent{Body: strings.Repeat("<", 100000)}
	limitContentSize(content)
	assert.LessOrEqual(t, jsonLen(content.Body), maxContentBytes+100)
}
