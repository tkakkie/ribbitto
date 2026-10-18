package conversation

import (
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/tkakkie/ribbitto/internal/kernel"
	"github.com/tkakkie/ribbitto/internal/realtime"
)

// BlockKind says which chat_v1 block a BodyBlock is.
type BlockKind int

// The chat_v1 blocks (decision 39).
const (
	BlockText BlockKind = iota
	BlockQuote
	BlockCode
)

// InlineKind says which chat_v1 inline node a BodyInline is.
type InlineKind int

// The chat_v1 inline nodes (decision 39).
const (
	InlineText InlineKind = iota
	InlineBold
	InlineItalic
	InlineCode
	InlineLink
	InlineReference
)

// BodyBlock is one block of a parsed chat_v1 body.
type BodyBlock struct {
	Kind BlockKind
	// Lines are a text or quote block's lines, each as inline nodes; a blank line has none.
	Lines [][]BodyInline
	// Code holds a code block's content lines, literal; Language is its tag, or empty.
	Code     []string
	Language string
}

// BodyInline is one inline node of a parsed chat_v1 body.
type BodyInline struct {
	Kind InlineKind
	// Text is literal text, a code span's content or a link's URL.
	Text string
	// Children are a bold or italic node's content.
	Children []BodyInline
	// Member is a reference's member, not yet resolved or checked.
	Member kernel.ID
}

// ParseChatV1 parses a body that ValidateMessageBody returned under
// chat_v1's grammar (decision 39). Anything outside the grammar is literal
// text, so every body parses. It runs in time linear in the body's length.
func ParseChatV1(body string) []BodyBlock {
	return parseChatV1(body, nil)
}

// textRun locates unescaped plain text in the source, before extraction.
// line is the content's start, after any block quote marker.
type textRun struct{ start, end, line int }

func parseChatV1(body string, runs *[]textRun) []BodyBlock {
	lines := strings.Split(body, "\n")
	offsets := make([]int, len(lines)+1)
	for i, line := range lines {
		offsets[i+1] = offsets[i] + len(line) + 1
	}
	// closing[i] is the first line from i on that is exactly a fence, so an
	// opening line without one costs a lookup instead of a scan of the rest.
	closing := make([]int, len(lines)+1)
	closing[len(lines)] = len(lines)
	for i := len(lines) - 1; i >= 0; i-- {
		closing[i] = closing[i+1]
		if lines[i] == "```" {
			closing[i] = i
		}
	}
	var blocks []BodyBlock
	for i := 0; i < len(lines); i++ {
		if language, ok := fenceOpening(lines[i]); ok && closing[i+1] < len(lines) {
			end := closing[i+1]
			blocks = append(blocks, BodyBlock{Kind: BlockCode, Code: lines[i+1 : end], Language: language})
			i = end
			continue
		}
		kind, content := BlockText, lines[i]
		if rest, ok := strings.CutPrefix(content, ">"); ok {
			kind, content = BlockQuote, strings.TrimPrefix(rest, " ")
		}
		if n := len(blocks); n == 0 || blocks[n-1].Kind != kind {
			blocks = append(blocks, BodyBlock{Kind: kind})
		}
		last := &blocks[len(blocks)-1]
		last.Lines = append(last.Lines, parseInline(content, offsets[i]+len(lines[i])-len(content), runs))
	}
	return blocks
}

// EscapeChatV1 returns text that chat_v1 reads back as exactly text, all of
// it literal: every ASCII punctuation character gets a backslash, so no
// delimiter, code span, member reference, autolink, quote or fence can start.
// It is for text the server writes itself, such as branching's notice, whose
// topic name must never become markup or a reference. ASCII bytes never occur
// inside a multi-byte UTF-8 sequence, so a byte loop is safe.
func EscapeChatV1(text string) string {
	var escaped strings.Builder
	for i := 0; i < len(text); i++ {
		if isASCIIPunctuation(text[i]) {
			escaped.WriteByte('\\')
		}
		escaped.WriteByte(text[i])
	}
	return escaped.String()
}

func fenceOpening(line string) (string, bool) {
	language, ok := strings.CutPrefix(line, "```")
	if !ok || len(language) > 20 {
		return "", false
	}
	for _, c := range []byte(language) {
		if !isASCIIAlphanumeric(c) && !strings.ContainsRune("+-#.", rune(c)) {
			return "", false
		}
	}
	return language, true
}

// inlineFrame is the line itself (the bottom of the stack) or an open delimiter.
type inlineFrame struct {
	kind  InlineKind
	delim string
	start int
	nodes []BodyInline
}

type inlineParser struct {
	line    string
	stack   []inlineFrame
	text    strings.Builder
	literal bool
}

func parseInline(line string, offset int, runs *[]textRun) []BodyInline {
	p := inlineParser{line: line, stack: make([]inlineFrame, 1, 3)}
	var plain []bool
	if runs != nil {
		plain = make([]bool, len(line))
	}
	for i := 0; i < len(line); {
		p.literal = false
		next := p.step(i)
		if runs != nil && p.literal {
			for j := i; j < next; j++ {
				plain[j] = true
			}
		}
		i = next
	}
	p.flush()
	// Delimiters still open at the end of the line are literal.
	for top := len(p.stack) - 1; top > 0; top-- {
		frame, parent := p.stack[top], &p.stack[top-1]
		if runs != nil {
			for j := frame.start; j < frame.start+len(frame.delim); j++ {
				plain[j] = true
			}
		}
		parent.nodes = append(append(parent.nodes, BodyInline{Kind: InlineText, Text: frame.delim}), frame.nodes...)
	}
	for i := 0; i < len(plain); i++ {
		if !plain[i] {
			continue
		}
		start := i
		for i < len(plain) && plain[i] {
			i++
		}
		*runs = append(*runs, textRun{offset + start, offset + i, offset})
	}
	return mergeText(p.stack[0].nodes)
}

// step reads the construct at line[i], trying the rules in decision 39's
// order, and returns where the next one starts.
func (p *inlineParser) step(i int) int {
	line := p.line
	switch c := line[i]; {
	case c == '\\' && i+1 < len(line) && isASCIIPunctuation(line[i+1]):
		p.text.WriteByte(line[i+1])
		return i + 2
	case c == '`':
		// An empty pair is not "text between" backticks, so the first is literal.
		if end := strings.IndexByte(line[i+1:], '`'); end > 0 {
			p.add(BodyInline{Kind: InlineCode, Text: line[i+1 : i+1+end]})
			return i + end + 2
		}
	case c == '<':
		if id, ok := memberReference(line[i:]); ok {
			p.add(BodyInline{Kind: InlineReference, Member: id})
			return i + len("<@") + 36 + len(">")
		}
	case c == 'h':
		if url := autolink(line, i); url != "" {
			p.add(BodyInline{Kind: InlineLink, Text: url})
			return i + len(url)
		}
	case c == '*' && strings.HasPrefix(line[i:], "**"):
		p.delimiter(i, InlineBold, "**")
		return i + 2
	case c == '_':
		p.delimiter(i, InlineItalic, "_")
		return i + 1
	}
	_, size := utf8.DecodeRuneInString(line[i:])
	p.literal = true
	p.text.WriteString(line[i : i+size])
	return i + size
}

func (p *inlineParser) delimiter(i int, kind InlineKind, delim string) {
	before, _ := utf8.DecodeLastRuneInString(p.line[:i])
	after, _ := utf8.DecodeRuneInString(p.line[i+len(delim):])
	hasBefore, hasAfter := i > 0, i+len(delim) < len(p.line)
	canOpen := hasAfter && !unicode.IsSpace(after)
	canClose := hasBefore && !unicode.IsSpace(before)
	if kind == InlineItalic {
		canOpen = canOpen && (!hasBefore || !isWordRune(before))
		canClose = canClose && (!hasAfter || !isWordRune(after))
	}
	top := len(p.stack) - 1
	switch {
	case canClose && top > 0 && p.stack[top].kind == kind:
		p.flush()
		frame := p.stack[top]
		p.stack = p.stack[:top]
		p.add(BodyInline{Kind: kind, Children: mergeText(frame.nodes)})
	case canOpen && top < 2: // two open delimiters at most
		p.flush()
		p.stack = append(p.stack, inlineFrame{kind: kind, delim: delim, start: i})
	default:
		// Includes a closer matching a deeper delimiter (crossing).
		p.literal = true
		p.text.WriteString(delim)
	}
}

func (p *inlineParser) add(node BodyInline) {
	p.flush()
	top := &p.stack[len(p.stack)-1]
	top.nodes = append(top.nodes, node)
}

func (p *inlineParser) flush() {
	if p.text.Len() > 0 {
		top := &p.stack[len(p.stack)-1]
		top.nodes = append(top.nodes, BodyInline{Kind: InlineText, Text: p.text.String()})
		p.text.Reset()
	}
}

// mergeText joins adjacent text nodes, which literal delimiters leave, so a
// tree has one shape however its text was split.
func mergeText(nodes []BodyInline) []BodyInline {
	merged := nodes[:0]
	var text strings.Builder
	for i, node := range nodes {
		if node.Kind != InlineText {
			merged = append(merged, node)
			continue
		}
		text.WriteString(node.Text)
		if i+1 == len(nodes) || nodes[i+1].Kind != InlineText {
			merged = append(merged, BodyInline{Kind: InlineText, Text: text.String()})
			text.Reset()
		}
	}
	return merged
}

func memberReference(s string) (kernel.ID, bool) {
	if len(s) < 39 || !strings.HasPrefix(s, "<@") || s[38] != '>' || strings.ContainsFunc(s[2:38], unicode.IsUpper) {
		return kernel.ID{}, false
	}
	// The canonical layout is the one realtime payloads use; only its
	// lowercase form is a reference here.
	id, err := realtime.ParsePayloadID(s[2:38])
	return id, err == nil
}

// autolink returns the link starting at line[i], or "" if none does.
func autolink(line string, i int) string {
	rest := line[i:]
	scheme := "https://"
	if !strings.HasPrefix(rest, scheme) {
		if scheme = "http://"; !strings.HasPrefix(rest, scheme) {
			return ""
		}
	}
	if before, _ := utf8.DecodeLastRuneInString(line[:i]); i > 0 && !unicode.IsSpace(before) && before != '(' {
		return ""
	}
	// The host follows the authority's last "@" and starts with a letter, a
	// digit or "[". Checking it before scanning the rest keeps a failed
	// attempt from rescanning the line. No trailing character trimmed below
	// can be that first character, so the trimmed link keeps its host.
	authority := rest[len(scheme):]
	if end := strings.IndexFunc(authority, func(r rune) bool { return strings.ContainsRune("/?#", r) || endsAutolink(r) }); end >= 0 {
		authority = authority[:end]
	}
	host, _ := utf8.DecodeRuneInString(authority[strings.LastIndexByte(authority, '@')+1:])
	if !unicode.IsLetter(host) && !unicode.IsDigit(host) && host != '[' {
		return ""
	}
	end := strings.IndexFunc(rest[len(scheme):], endsAutolink)
	if end < 0 {
		end = len(rest)
	} else {
		end += len(scheme)
	}
	url := rest[:end]
	unbalanced := strings.Count(url, ")") - strings.Count(url, "(")
	for len(url) > len(scheme) {
		last := url[len(url)-1]
		if last == ')' && unbalanced > 0 {
			unbalanced--
		} else if !strings.ContainsRune(".,:;!?'*_", rune(last)) {
			break
		}
		url = url[:len(url)-1]
	}
	return url
}

func endsAutolink(r rune) bool {
	switch r {
	case '<', '>', '"', '\u2066', '\u2067', '\u2068', '\u2069', '\u200e', '\u200f', '\u061c':
		return true
	}
	return unicode.IsSpace(r)
}

// isWordRune reports the characters a "_" must not touch on its outer side,
// for any script: letters, digits and combining marks.
func isWordRune(r rune) bool {
	return unicode.IsLetter(r) || unicode.IsDigit(r) || unicode.IsMark(r)
}

func isASCIIPunctuation(c byte) bool {
	return strings.IndexByte("!\"#$%&'()*+,-./:;<=>?@[\\]^_`{|}~", c) >= 0
}

func isASCIIAlphanumeric(c byte) bool {
	return 'a' <= c && c <= 'z' || 'A' <= c && c <= 'Z' || '0' <= c && c <= '9'
}
