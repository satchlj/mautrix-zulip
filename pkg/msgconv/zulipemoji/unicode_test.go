package zulipemoji

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestUnicodeToName(t *testing.T) {
	for emoji, expected := range map[string][2]string{
		"👍":    {"1f44d", "+1"},
		"❤️":   {"2764", "heart"},
		"👍🏽":   {"1f44d", "+1"},
		"🎉":    {"1f389", "tada"},
		"#️⃣":  {"0023-20e3", "hash"},
		"©️":   {"00a9", "copyright"},
		"👩🏽‍💻": {"1f469-200d-1f4bb", "woman_technologist"},
	} {
		code, name, ok := UnicodeToName(emoji)
		assert.True(t, ok, emoji)
		assert.Equal(t, expected[0], code, emoji)
		assert.Equal(t, expected[1], name, emoji)
	}
}
