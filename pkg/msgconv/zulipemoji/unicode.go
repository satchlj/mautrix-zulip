package zulipemoji

import (
	"fmt"
	"strconv"
	"strings"
)

func UnifiedToUnicode(input string) string {
	input = strings.TrimPrefix(input, "emoji-")
	parts := strings.Split(input, "-")
	output := make([]rune, len(parts))
	for i, part := range parts {
		val, err := strconv.ParseInt(part, 16, 32)
		if err != nil {
			return ""
		}
		output[i] = rune(val)
	}
	return string(output)
}

// UnicodeToCode converts a unicode emoji into the code format Zulip uses (lowercase hex codepoints joined by
// dashes, without variation selectors).
func UnicodeToCode(emoji string) string {
	parts := make([]string, 0, 2)
	for _, r := range emoji {
		if r == 0xfe0f || r == 0xfe0e {
			continue
		}
		parts = append(parts, fmt.Sprintf("%04x", r))
	}
	return strings.Join(parts, "-")
}

// UnicodeToName finds the Zulip emoji code and canonical name for a unicode emoji.
func UnicodeToName(emoji string) (code, name string, ok bool) {
	code = UnicodeToCode(emoji)
	name, ok = codeToName[code]
	if !ok {
		// Zulip doesn't support skin tones, so fall back to the emoji without skin tone modifiers.
		parts := strings.Split(code, "-")
		baseParts := parts[:0:0]
		for _, part := range parts {
			if part < "1f3fb" || part > "1f3ff" || len(part) != 5 {
				baseParts = append(baseParts, part)
			}
		}
		baseCode := strings.Join(baseParts, "-")
		name, ok = codeToName[baseCode]
		if ok {
			code = baseCode
		}
	}
	return
}
