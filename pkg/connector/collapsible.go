package connector

import (
	"regexp"
	"strings"
)

var (
	htmlDetailsWithSummaryRegex = regexp.MustCompile(`(?i)<details(?:\s[^>]*)?>\s*<summary(?:\s[^>]*)?>`)
	htmlDetailsRegex            = regexp.MustCompile(`(?i)<details(?:\s[^>]*)?>`)
	htmlSummaryEndRegex         = regexp.MustCompile(`(?i)</summary\s*>`)
	htmlDetailsEndRegex         = regexp.MustCompile(`(?i)</details\s*>`)

	detailsStartLineRegex = regexp.MustCompile(`^\s*<details>\s*(?:<summary>(.*?)</summary>)?\s*(.*)$`)
	detailsEndLineRegex   = regexp.MustCompile(`^(.*?)\s*</details>\s*$`)
	codeFenceRegex        = regexp.MustCompile("^\\s{0,3}(`{3,}|~{3,})\\s*(\\S*)")
	backtickRunRegex      = regexp.MustCompile("`+")
)

// detailsHTMLToMarkers replaces <details> and <summary> elements in Matrix HTML with the same text that
// a user would type to make a collapsible section, so that convertCollapsibleSections handles both cases.
func detailsHTMLToMarkers(htmlData string) string {
	if !strings.Contains(strings.ToLower(htmlData), "<details") {
		return htmlData
	}
	htmlData = htmlDetailsWithSummaryRegex.ReplaceAllString(htmlData, "<p>&lt;details&gt;&lt;summary&gt;")
	htmlData = htmlSummaryEndRegex.ReplaceAllString(htmlData, "&lt;/summary&gt;</p>")
	htmlData = htmlDetailsRegex.ReplaceAllString(htmlData, "<p>&lt;details&gt;</p>")
	return htmlDetailsEndRegex.ReplaceAllString(htmlData, "<p>&lt;/details&gt;</p>")
}

type collapsibleSection struct {
	startLine string
	title     string
	lines     []string
}

// convertCollapsibleSections converts collapsible sections written as HTML
// (<details><summary>Title</summary> ... </details>) into Zulip spoiler blocks (```spoiler Title ... ```).
// Beeper escapes HTML typed into the composer, so the tags arrive as text. Code blocks are left alone,
// and unclosed sections are kept as they were.
func convertCollapsibleSections(text string) string {
	if !strings.Contains(text, "<details>") {
		return text
	}
	var stack []*collapsibleSection
	var output []string
	appendLine := func(line string) {
		if len(stack) > 0 {
			stack[len(stack)-1].lines = append(stack[len(stack)-1].lines, line)
		} else {
			output = append(output, line)
		}
	}
	var codeFence string
	queue := strings.Split(text, "\n")
	for len(queue) > 0 {
		line := queue[0]
		queue = queue[1:]
		if codeFence != "" {
			if strings.HasPrefix(strings.TrimSpace(line), codeFence) {
				codeFence = ""
			}
			appendLine(line)
			continue
		}
		if match := codeFenceRegex.FindStringSubmatch(line); match != nil {
			// Zulip's own spoiler and quote blocks contain markdown, so sections inside them are still converted
			if info := strings.ToLower(match[2]); info != "spoiler" && info != "quote" {
				codeFence = match[1]
			}
			appendLine(line)
			continue
		}
		if match := detailsStartLineRegex.FindStringSubmatch(line); match != nil {
			stack = append(stack, &collapsibleSection{startLine: line, title: strings.TrimSpace(match[1])})
			if rest := strings.TrimSpace(match[2]); rest != "" {
				queue = append([]string{rest}, queue...)
			}
			continue
		}
		if match := detailsEndLineRegex.FindStringSubmatch(line); match != nil && len(stack) > 0 {
			if before := strings.TrimSpace(match[1]); before != "" {
				appendLine(before)
			}
			section := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			appendLine(section.toZulip())
			continue
		}
		appendLine(line)
	}
	// Restore any sections that were never closed
	for len(stack) > 0 {
		section := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		appendLine(section.startLine)
		for _, line := range section.lines {
			appendLine(line)
		}
	}
	return strings.Join(output, "\n")
}

func (cs *collapsibleSection) toZulip() string {
	lines := cs.lines
	for len(lines) > 0 && strings.TrimSpace(lines[0]) == "" {
		lines = lines[1:]
	}
	for len(lines) > 0 && strings.TrimSpace(lines[len(lines)-1]) == "" {
		lines = lines[:len(lines)-1]
	}
	content := strings.Join(lines, "\n")
	// The fence must be longer than any backtick run inside, such as the fence of a nested section
	fenceLength := 3
	for _, run := range backtickRunRegex.FindAllString(content, -1) {
		fenceLength = max(fenceLength, len(run)+1)
	}
	fence := strings.Repeat("`", fenceLength)
	header := fence + "spoiler"
	if cs.title != "" {
		header += " " + cs.title
	}
	return header + "\n" + content + "\n" + fence
}
