package conversation_test

import (
	"slices"
	"strings"
	"testing"

	"github.com/tkakkie/ribbitto/internal/conversation"
	"github.com/tkakkie/ribbitto/internal/kernel"
	"github.com/tkakkie/ribbitto/internal/realtime"
)

// otherUUID is a member the directory did not return: unknown, or in
// another organisation.
const otherUUID = "0190a0b1-2c3d-7e4f-8a9b-0c1d2e3f4a5c"

// textCases fix chat_v1 body → Entry.Text, with memberUUID resolved to
// @tomoya and otherUUID unresolved. The first rows are decision 39's table.
var textCases = []struct{ name, in, want string }{
	{"bold and italic", "**Deploy** _today_", "Deploy today"},
	{"code span", "use `a*b` here", "use a*b here"},
	{"escaped delimiters", `\*\*not bold\*\*`, "**not bold**"},
	{"quote then text", "> quoted line\nreply", "quoted line\nreply"},
	{"fence with tag", "```go\nx := 1\n```", "x := 1"},
	{"link trailing dot", "see https://example.com/a.", "see https://example.com/a."},
	{"bold link", "**see https://example.com/x**", "see https://example.com/x"},
	{"crossing", "**a _b** c_", "**a b** c"},
	{"resolved reference", "hi <@" + memberUUID + ">", "hi @tomoya"},
	{"unresolved reference", "hi <@" + otherUUID + ">!", "hi @?!"},
	{"code keeps white space", "` a  b\t`\n```\n  x\n\n\ty  \n```", " a  b\t\n  x\n\n\ty  "},
	{"block boundaries", "a\n\n> q\n> r\n```\nc\n```\n```\n```\nb", "a\n\nq\nr\nc\n\nb"},
	{"references in bold and quote", "> **<@" + memberUUID + "> _<@" + otherUUID + ">_**", "@tomoya @?"},
	// Literal UUIDs stay as typed: in text, escaped, malformed, and in code.
	{"UUID as text", "id " + otherUUID + " and <" + memberUUID + ">", "id " + otherUUID + " and <" + memberUUID + ">"},
	{"escaped reference", `\<@` + otherUUID + ">", "<@" + otherUUID + ">"},
	{"malformed reference", "<@" + strings.ToUpper(otherUUID) + ">", "<@" + strings.ToUpper(otherUUID) + ">"},
	{"reference in code span", "`<@" + otherUUID + ">`", "<@" + otherUUID + ">"},
	{"reference in fenced code", "```\n<@" + otherUUID + ">\n```", "<@" + otherUUID + ">"},
}

func TestEntryText(t *testing.T) {
	member, err := realtime.ParsePayloadID(memberUUID)
	if err != nil {
		t.Fatal(err)
	}
	references := map[kernel.ID]conversation.Reference{member: {DisplayName: "Tomoya", Handle: "tomoya"}}
	for _, tc := range textCases {
		t.Run(tc.name, func(t *testing.T) {
			// Blocks as the reader fills them, and Body alone, give the same text.
			parsed := conversation.Entry{Message: conversation.Message{Format: conversation.FormatChatV1}, Blocks: conversation.ParseChatV1(tc.in), References: references}
			unparsed := conversation.Entry{Message: conversation.Message{Format: conversation.FormatChatV1, Body: tc.in}, References: references}
			for _, entry := range []conversation.Entry{parsed, unparsed} {
				if got := entry.Text("@?"); got != tc.want {
					t.Fatalf("Text(%q) = %q, want %q", tc.in, got, tc.want)
				}
			}
		})
	}
}

// checkExtraction is the fuzz test's extraction check: the reference
// segments are exactly the tree's reference nodes in order, and Text writes
// each as @handle when resolved and as the placeholder when not, never its
// UUID. Body text cannot hold NUL, so the sentinels count only resolutions.
func checkExtraction(t *testing.T, body string, blocks []conversation.BodyBlock) {
	var nodes []kernel.ID
	var walk func([]conversation.BodyInline)
	walk = func(inlines []conversation.BodyInline) {
		for _, node := range inlines {
			if node.Kind == conversation.InlineReference {
				nodes = append(nodes, node.Member)
			}
			walk(node.Children)
		}
	}
	for _, block := range blocks {
		for _, line := range block.Lines {
			walk(line)
		}
	}
	var segments []kernel.ID
	var literal strings.Builder
	for _, segment := range conversation.ExtractChatV1(blocks) {
		if segment.Reference != (segment.Text == "") {
			t.Fatalf("%q: segment %+v", body, segment)
		}
		if segment.Reference {
			segments = append(segments, segment.Member)
		}
		literal.WriteString(segment.Text + "\x00")
	}
	if !slices.Equal(segments, nodes) {
		t.Fatalf("%q: reference segments %v, nodes %v", body, segments, nodes)
	}
	references, resolved := map[kernel.ID]conversation.Reference{}, 0
	for _, id := range nodes {
		if id[15]%2 == 0 {
			references[id] = conversation.Reference{Handle: "\x00h"}
			resolved++
		}
	}
	text := conversation.Entry{Message: conversation.Message{Format: conversation.FormatChatV1, Body: body}, Blocks: blocks, References: references}.Text("\x00p")
	if strings.Count(text, "@\x00h") != resolved || strings.Count(text, "\x00p") != len(nodes)-resolved {
		t.Fatalf("%q: %d resolved of %d references in %q", body, resolved, len(nodes), text)
	}
	for _, id := range nodes {
		if uuid := realtime.FormatPayloadID(id); strings.Count(text, uuid) != strings.Count(literal.String(), uuid) {
			t.Fatalf("%q: a reference shows its UUID in %q", body, text)
		}
	}
}
