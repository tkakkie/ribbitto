package conversation

import (
	"strings"

	"github.com/tkakkie/ribbitto/internal/kernel"
	"github.com/tkakkie/ribbitto/internal/org"
)

// TextSegment is one piece of a chat_v1 body's text extraction: literal
// text, or a member reference that the caller resolves when it uses the
// text, so no name is stored and a rename never leaves stale text.
type TextSegment struct {
	Text string
	// Reference marks a segment that names Member; its Text is empty.
	Reference bool
	Member    kernel.ID
}

// ExtractChatV1 returns a parsed chat_v1 body's text under decision 39:
// delimiters, escapes, fences and language tags are gone, code keeps its
// content exactly, an autolink is its URL, line breaks inside a block stay
// and each block boundary is one line break. Adjacent text is one segment.
func ExtractChatV1(blocks []BodyBlock) []TextSegment {
	var x textExtractor
	for i, block := range blocks {
		if i > 0 {
			x.text.WriteByte('\n')
		}
		if block.Kind == BlockCode {
			x.text.WriteString(strings.Join(block.Code, "\n"))
			continue
		}
		for j, line := range block.Lines {
			if j > 0 {
				x.text.WriteByte('\n')
			}
			x.inlines(line)
		}
	}
	x.flush()
	return x.segments
}

type textExtractor struct {
	segments []TextSegment
	text     strings.Builder
}

func (x *textExtractor) inlines(nodes []BodyInline) {
	for _, node := range nodes {
		switch node.Kind {
		case InlineBold, InlineItalic:
			x.inlines(node.Children)
		case InlineReference:
			x.flush()
			x.segments = append(x.segments, TextSegment{Reference: true, Member: node.Member})
		default:
			// Text, a code span's content and a link's URL are all kept as they are.
			x.text.WriteString(node.Text)
		}
	}
}

func (x *textExtractor) flush() {
	if x.text.Len() > 0 {
		x.segments = append(x.segments, TextSegment{Text: x.text.String()})
		x.text.Reset()
	}
}

// Text returns the message as plain text, one extraction per format
// (decision 38): a plain_text body unchanged, and a chat_v1 body's
// extraction with each reference resolved through References to its
// "@handle", or to placeholder when the organisation's directory did not
// return the member. A reference never shows its UUID, while a UUID typed as
// text or inside code stays as typed.
func (e Entry) Text(placeholder string) string {
	if e.Format != FormatChatV1 {
		return e.Body
	}
	blocks := e.Blocks
	if blocks == nil {
		blocks = ParseChatV1(e.Body)
	}
	var text strings.Builder
	for _, segment := range ExtractChatV1(blocks) {
		switch member, ok := e.References[segment.Member]; {
		case !segment.Reference:
			text.WriteString(segment.Text)
		case ok:
			text.WriteString("@" + member.Handle)
		default:
			text.WriteString(placeholder)
		}
	}
	return text.String()
}

// resolveReferences returns the current names of the members blocks
// reference that the organisation's directory returned; a missing one stays
// unresolved, so a foreign member's names cannot reach the text.
func resolveReferences(blocks []BodyBlock, members map[kernel.ID]org.DirectoryEntry, names map[kernel.ID]string) map[kernel.ID]Reference {
	references := map[kernel.ID]Reference{}
	for _, id := range appendReferences(nil, blocks) {
		if member, ok := members[id]; ok {
			references[id] = Reference{DisplayName: names[member.AccountID], Handle: member.Handle}
		}
	}
	return references
}
