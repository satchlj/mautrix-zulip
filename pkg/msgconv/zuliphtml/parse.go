package zuliphtml

import (
	"context"
	"fmt"
	"path"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"

	"go.mau.fi/mautrix-zulip/pkg/msgconv/zulipemoji"
	"go.mau.fi/mautrix-zulip/pkg/zid"
)

type MathImage struct {
	URL    id.ContentURIString
	Width  int
	Height int
}

type MathMode string

const (
	// MathModeInline renders all math as images inside the formatted message.
	MathModeInline MathMode = "inline"
	// MathModeAttachments keeps inline math as LaTeX source and sends display math as separate image attachments.
	MathModeAttachments MathMode = "attachments"
)

type Options struct {
	// If set, Zulip's "quote and reply" prefix is detected, and if the function returns true for the quoted
	// message ID, the quote is removed so that it can be rendered as a native reply instead.
	ReplyTargetExists func(int) bool
	// If set, math is rendered into images. The function should return nil if rendering fails.
	RenderMath func(tex string, display bool) *MathImage
	MathMode   MathMode
}

// Parse converts Zulip-rendered message HTML into Matrix HTML.
func Parse(
	ctx context.Context, bridge *bridgev2.Bridge, baseURL, inputHTML string, opts Options,
) (outputHTML string, attachments []Attachment, mentions *event.Mentions, err error) {
	parser := &zulipHTMLParser{
		ctx:     ctx,
		br:      bridge,
		baseURL: baseURL,
		opts:    opts,
	}
	err = parser.Parse(inputHTML)
	return parser.output, parser.attachments, &parser.mentions, err
}

type Attachment struct {
	URL      string
	MsgType  event.MessageType
	FileName string
	// If set, the attachment is a rendered math image that has already been uploaded to Matrix.
	Math *MathImage
	TeX  string
}

type zulipHTMLParser struct {
	ctx         context.Context
	br          *bridgev2.Bridge
	baseURL     string
	output      string
	attachments []Attachment
	mentions    event.Mentions

	opts zulipHTMLParserOptions
}

type zulipHTMLParserOptions = Options

func (zhp *zulipHTMLParser) Parse(input string) error {
	node, err := html.Parse(strings.NewReader(input))
	if err != nil {
		return err
	}
	bodyNode := findChild(node, atom.Body, "")
	if bodyNode != nil {
		zhp.stripQuoteReply(bodyNode)
		node = bodyNode.FirstChild
		if node == nil {
			node = &html.Node{Type: html.TextNode}
		}
	}
	if node.NextSibling == nil && node.DataAtom == atom.P {
		node = node.FirstChild
	}
	node = buildWithChild(&html.Node{
		Type:       html.DocumentNode,
		FirstChild: node,
	})
	err = zhp.processChildren(node)
	if err != nil {
		return err
	}
	var buf strings.Builder
	err = html.Render(&buf, node)
	if err != nil {
		return err
	}
	zhp.output = buf.String()
	return nil
}

func (zhp *zulipHTMLParser) processChildren(node *html.Node) error {
	for child := node.FirstChild; child != nil; {
		// Handlers may remove the node from the tree, which clears its NextSibling
		next := child.NextSibling
		err := zhp.processSingleNode(child)
		child = next
		if err != nil {
			return err
		}
	}
	return nil
}

func (zhp *zulipHTMLParser) processSingleNode(node *html.Node) error {
	if node.Type != html.ElementNode {
		return nil
	}
	classes := getClass(node.Attr)
	handler, ok := handlers[node.DataAtom][firstClass(classes)]
	if ok {
		handled, err := handler(zhp, node)
		if err != nil {
			return err
		} else if handled {
			return nil
		}
	}
	switch node.DataAtom {
	case atom.A:
		hrefIdx := getAttributeIndex(node.Attr, "href")
		if hrefIdx >= 0 {
			node.Attr[hrefIdx].Val = zhp.makeAbsoluteURL(node.Attr[hrefIdx].Val)
		}
	}
	return zhp.processChildren(node)
}

var handlers = map[atom.Atom]map[string]func(*zulipHTMLParser, *html.Node) (bool, error){
	atom.A: {
		"stream":       (*zulipHTMLParser).processChannelLink,
		"stream-topic": (*zulipHTMLParser).processTopicLink,
		"message-link": (*zulipHTMLParser).processMessageLink,
	},
	atom.Div: {
		"codehilite":           (*zulipHTMLParser).processCodeBlock,
		"message_inline_image": (*zulipHTMLParser).processInlineMedia,
		"spoiler-block":        (*zulipHTMLParser).processSpoilerBlock,
	},
	atom.Span: {
		"emoji":         (*zulipHTMLParser).processEmoji,
		"katex-display": (*zulipHTMLParser).processKatex,
		"katex":         (*zulipHTMLParser).processInlineKatex,
		"user-mention":  (*zulipHTMLParser).processUserMention,
		"topic-mention": (*zulipHTMLParser).processTopicMention,
	},
	atom.Img: {
		"emoji":                     (*zulipHTMLParser).processCustomEmoji,
		"inline-image":              (*zulipHTMLParser).processInlineImage,
		"image-loading-placeholder": (*zulipHTMLParser).processInlineImage,
	},
	atom.Audio: {
		"": (*zulipHTMLParser).processInlineAudio,
	},
}

func (zhp *zulipHTMLParser) processChannelLink(node *html.Node) (bool, error) {
	return false, nil
}

func (zhp *zulipHTMLParser) processTopicLink(node *html.Node) (bool, error) {
	return false, nil
}

func (zhp *zulipHTMLParser) processMessageLink(node *html.Node) (bool, error) {
	return false, nil
}

func (zhp *zulipHTMLParser) processCodeBlock(node *html.Node) (bool, error) {
	codeBlockLanguage, _ := getAttribute(node.Attr, "data-code-language")
	codeBlock := nodeText(node)
	var codeAttr []html.Attribute
	if codeBlockLanguage != "" {
		codeAttr = []html.Attribute{{
			Key: "class",
			Val: fmt.Sprintf("language-%s", strings.ToLower(codeBlockLanguage)),
		}}
	}
	*node = rebuildNode(node, &html.Node{
		Type:     html.ElementNode,
		DataAtom: atom.Pre,
		FirstChild: buildWithChild(&html.Node{
			Type:     html.ElementNode,
			DataAtom: atom.Code,
			Attr:     codeAttr,
			FirstChild: &html.Node{
				Type: html.TextNode,
				Data: codeBlock,
			},
		}),
	})
	return true, nil
}

func (zhp *zulipHTMLParser) processInlineMedia(node *html.Node) (bool, error) {
	mediaLink := findChild(node, atom.A, "")
	if mediaLink == nil {
		return false, nil
	}
	href, ok := getAttribute(mediaLink.Attr, "href")
	if !ok {
		return false, nil
	}
	msgType := event.MsgImage
	if findChild(mediaLink, atom.Video, "") != nil {
		msgType = event.MsgVideo
	}
	title, _ := getAttribute(mediaLink.Attr, "title")
	if title == "" {
		parts := strings.SplitN(href, "?", 2)
		title = path.Base(parts[0])
	}
	zhp.attachments = append(zhp.attachments, Attachment{
		URL:      zhp.makeAbsoluteURL(href),
		MsgType:  msgType,
		FileName: title,
	})
	node.Parent.RemoveChild(node)
	return true, nil
}

// processInlineImage handles the inline image format used by newer Zulip servers, where an uploaded image is
// a bare <img> inside the paragraph that points at a server-side thumbnail, with the original in data-original-src.
func (zhp *zulipHTMLParser) processInlineImage(node *html.Node) (bool, error) {
	src, ok := getAttribute(node.Attr, "data-original-src")
	if !ok || src == "" {
		src, ok = getAttribute(node.Attr, "src")
		if !ok || src == "" || slices.Contains(getClass(node.Attr), "image-loading-placeholder") {
			return false, nil
		}
	}
	msgType := event.MsgImage
	if contentType, _ := getAttribute(node.Attr, "data-original-content-type"); strings.HasPrefix(contentType, "video/") {
		msgType = event.MsgVideo
	}
	title, _ := getAttribute(node.Attr, "alt")
	if title == "" {
		parts := strings.SplitN(src, "?", 2)
		title = path.Base(parts[0])
	}
	zhp.attachments = append(zhp.attachments, Attachment{
		URL:      zhp.makeAbsoluteURL(src),
		MsgType:  msgType,
		FileName: title,
	})
	parent := node.Parent
	parent.RemoveChild(node)
	// Zulip puts a line break before the image, which would leave a dangling newline in the caption
	for last := parent.LastChild; last != nil; last = parent.LastChild {
		if (last.Type == html.TextNode && strings.TrimSpace(last.Data) == "") ||
			(last.Type == html.ElementNode && last.DataAtom == atom.Br) {
			parent.RemoveChild(last)
		} else {
			break
		}
	}
	if last := parent.LastChild; last != nil && last.Type == html.TextNode {
		last.Data = strings.TrimRight(last.Data, " \t\n")
	}
	if parent.FirstChild == nil && parent.DataAtom == atom.P && parent.Parent != nil {
		parent.Parent.RemoveChild(parent)
	}
	return true, nil
}

func (zhp *zulipHTMLParser) makeAbsoluteURL(href string) string {
	if strings.HasPrefix(href, "/") && !strings.HasPrefix(href, "//") {
		return zhp.baseURL + href
	} else if strings.HasPrefix(href, "#") {
		return zhp.baseURL + "/" + href
	}
	return href
}

func (zhp *zulipHTMLParser) processInlineAudio(node *html.Node) (bool, error) {
	href, ok := getAttribute(node.Attr, "src")
	if !ok {
		return false, nil
	}
	title, _ := getAttribute(node.Attr, "title")
	zhp.attachments = append(zhp.attachments, Attachment{
		URL:      zhp.makeAbsoluteURL(href),
		MsgType:  event.MsgAudio,
		FileName: title,
	})
	node.Parent.RemoveChild(node)
	return true, nil
}

// processSpoilerBlock converts a Zulip spoiler (a collapsible section) into a <details> element with the header
// as the <summary>. Clients that don't support <details> show the header and content directly.
func (zhp *zulipHTMLParser) processSpoilerBlock(node *html.Node) (bool, error) {
	header := findChild(node, atom.Div, "spoiler-header")
	content := findChild(node, atom.Div, "spoiler-content")
	if header == nil || content == nil {
		return false, nil
	}
	// The header is usually a single paragraph, but <summary> should only contain inline content
	headerContent := header
	if para := findChild(header, atom.P, ""); para != nil {
		headerContent = para
	}
	summary := &html.Node{Type: html.ElementNode, DataAtom: atom.Summary, Data: "summary"}
	for child := headerContent.FirstChild; child != nil; child = headerContent.FirstChild {
		headerContent.RemoveChild(child)
		summary.AppendChild(child)
	}
	if strings.TrimSpace(nodeText(summary)) == "" {
		for child := summary.FirstChild; child != nil; child = summary.FirstChild {
			summary.RemoveChild(child)
		}
		summary.AppendChild(&html.Node{Type: html.TextNode, Data: "Spoiler"})
	}
	for child := node.FirstChild; child != nil; child = node.FirstChild {
		node.RemoveChild(child)
	}
	node.DataAtom = atom.Details
	node.Data = "details"
	node.Attr = nil
	node.AppendChild(summary)
	for child := content.FirstChild; child != nil; child = content.FirstChild {
		content.RemoveChild(child)
		node.AppendChild(child)
	}
	// Not marking as handled, so that the contents of the spoiler get processed too
	return false, nil
}

func (zhp *zulipHTMLParser) processCustomEmoji(node *html.Node) (bool, error) {
	return false, nil
}

func (zhp *zulipHTMLParser) processEmoji(node *html.Node) (bool, error) {
	classes := getClass(node.Attr)
	for _, c := range classes {
		if strings.HasPrefix(c, "emoji-") {
			*node = rebuildNode(node, &html.Node{
				Type: html.TextNode,
				Data: zulipemoji.UnifiedToUnicode(c),
			})
			return true, nil
		}
	}
	return false, nil
}

func (zhp *zulipHTMLParser) processKatex(node *html.Node) (bool, error) {
	annotation := findChild(node, atom.Annotation, "")
	if annotation == nil {
		return false, nil
	}
	*node = rebuildNode(node, zhp.mathNode(nodeText(annotation), true))
	return true, nil
}

// processInlineKatex replaces inline KaTeX output (which includes both MathML and a huge HTML rendering)
// with the Matrix math format, using the LaTeX source as the fallback text.
func (zhp *zulipHTMLParser) processInlineKatex(node *html.Node) (bool, error) {
	annotation := findChild(node, atom.Annotation, "")
	if annotation == nil {
		return false, nil
	}
	*node = rebuildNode(node, zhp.mathNode(nodeText(annotation), false))
	return true, nil
}

// mathNode creates the Matrix HTML for a math expression: an image if rendering is enabled,
// or the standard data-mx-maths element with the LaTeX source as the fallback.
func (zhp *zulipHTMLParser) mathNode(latexSource string, display bool) *html.Node {
	delimiter := "$"
	tag, tagName := atom.Span, "span"
	if display {
		delimiter = "$$"
		tag, tagName = atom.Div, "div"
	}
	renderAsImage := zhp.opts.RenderMath != nil && (display || zhp.opts.MathMode != MathModeAttachments)
	if renderAsImage {
		if img := zhp.opts.RenderMath(latexSource, display); img != nil {
			if display && zhp.opts.MathMode == MathModeAttachments {
				zhp.attachments = append(zhp.attachments, Attachment{
					MsgType:  event.MsgImage,
					FileName: "math.png",
					Math:     img,
					TeX:      latexSource,
				})
				return &html.Node{Type: html.ElementNode, DataAtom: tag, Data: tagName}
			}
			return &html.Node{
				Type:     html.ElementNode,
				DataAtom: tag,
				Data:     tagName,
				FirstChild: &html.Node{
					Type:     html.ElementNode,
					DataAtom: atom.Img,
					Data:     "img",
					Attr: []html.Attribute{
						{Key: "src", Val: string(img.URL)},
						{Key: "alt", Val: delimiter + latexSource + delimiter},
						{Key: "title", Val: latexSource},
						{Key: "width", Val: strconv.Itoa(img.Width)},
						{Key: "height", Val: strconv.Itoa(img.Height)},
					},
				},
			}
		}
	}
	return &html.Node{
		Type:     html.ElementNode,
		DataAtom: tag,
		Data:     tagName,
		Attr:     []html.Attribute{{Key: "data-mx-maths", Val: latexSource}},
		FirstChild: &html.Node{
			Type:     html.ElementNode,
			DataAtom: atom.Code,
			Data:     "code",
			FirstChild: &html.Node{
				Type: html.TextNode,
				Data: latexSource,
			},
		},
	}
}

func (zhp *zulipHTMLParser) processUserMention(node *html.Node) (bool, error) {
	userIDStr, ok := getAttribute(node.Attr, "data-user-id")
	if !ok {
		return false, nil
	}
	if userIDStr == "*" {
		*node = rebuildNode(node, &html.Node{
			Type: html.TextNode,
			Data: "@room",
		})
		zhp.mentions.Room = true
	} else if userID, err := strconv.Atoi(userIDStr); err == nil {
		var ghost *bridgev2.Ghost
		var userLogin *bridgev2.UserLogin
		ghost, err = zhp.br.GetGhostByID(zhp.ctx, zid.MakeUserID(userID))
		if err != nil {
			return false, fmt.Errorf("failed to get ghost of mentioned user %d: %w", userID, err)
		}
		mxid := ghost.Intent.GetMXID()
		userLogin, err = zhp.br.GetExistingUserLoginByID(zhp.ctx, zid.MakeUserLoginID(userID))
		if err != nil {
			return false, fmt.Errorf("failed to get user login of mentioned user %d: %w", userID, err)
		} else if userLogin != nil {
			mxid = userLogin.UserMXID
		}

		if !slices.Contains(getClass(node.Attr), "silent") {
			zhp.mentions.Add(mxid)
		}
		node.Data = "a"
		node.DataAtom = atom.A
		node.Attr = []html.Attribute{{
			Key: "href",
			Val: mxid.URI().MatrixToURL(),
		}}
	} else {
		return false, nil
	}
	return true, nil
}

func (zhp *zulipHTMLParser) processTopicMention(node *html.Node) (bool, error) {
	return false, nil
}

var nearMessageRegex = regexp.MustCompile(`/near/(\d+)$`)

// stripQuoteReply detects the format Zulip uses for "quote and reply":
//
//	<p><span class="user-mention silent" ...>Name</span> <a href=".../near/123">said</a>:</p>
//	<blockquote>...</blockquote>
//
// If the quoted message has been bridged, both elements are removed and the message becomes a native reply.
func (zhp *zulipHTMLParser) stripQuoteReply(body *html.Node) {
	if zhp.opts.ReplyTargetExists == nil {
		return
	}
	first := nextElement(body.FirstChild)
	if first == nil || first.DataAtom != atom.P {
		return
	}
	quote := nextElement(first.NextSibling)
	if quote == nil || quote.DataAtom != atom.Blockquote {
		return
	}
	mention := findChild(first, atom.Span, "user-mention")
	link := findChild(first, atom.A, "")
	if mention == nil || link == nil || !strings.HasSuffix(strings.TrimSpace(nodeText(first)), ":") {
		return
	}
	href, _ := getAttribute(link.Attr, "href")
	match := nearMessageRegex.FindStringSubmatch(href)
	if match == nil {
		return
	}
	messageID, err := strconv.Atoi(match[1])
	if err != nil || !zhp.opts.ReplyTargetExists(messageID) {
		return
	}
	for node := body.FirstChild; node != nil; {
		next := node.NextSibling
		body.RemoveChild(node)
		if node == quote {
			break
		}
		node = next
	}
	// Remove leading whitespace text nodes left after the quote
	for body.FirstChild != nil && body.FirstChild.Type == html.TextNode && strings.TrimSpace(body.FirstChild.Data) == "" {
		body.RemoveChild(body.FirstChild)
	}
}

func nextElement(node *html.Node) *html.Node {
	for ; node != nil; node = node.NextSibling {
		if node.Type == html.ElementNode {
			return node
		}
	}
	return nil
}
