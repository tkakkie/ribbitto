package conversation_test

import (
	"slices"
	"testing"

	"github.com/tkakkie/ribbitto/internal/conversation"
	"github.com/tkakkie/ribbitto/internal/kernel"
)

func TestExtractMentions(t *testing.T) {
	a, b := kernel.ID{1}, kernel.ID{2}
	refA, refB := "<@01000000-0000-0000-0000-000000000000>", "<@02000000-0000-0000-0000-000000000000>"
	for _, tt := range []struct {
		name, body string
		format     conversation.Format
		want       []kernel.ID
	}{
		{"distinct in source order", refB + " **" + refA + "** " + refB, conversation.FormatChatV1, []kernel.ID{b, a}},
		{"inline code", "`" + refA + "`", conversation.FormatChatV1, nil},
		{"fence", "```\n" + refA + "\n```", conversation.FormatChatV1, nil},
		{"escape", "\\" + refA, conversation.FormatChatV1, nil},
		{"plain text", refA + " @owner", conversation.FormatPlainText, nil},
		{"unknown format", refA, conversation.Format("future"), nil},
		{"block quote", "> " + refA, conversation.FormatChatV1, []kernel.ID{a}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := conversation.ExtractMentions(tt.format, tt.body); !slices.Equal(got, tt.want) {
				t.Fatalf("mentions=%v, want %v", got, tt.want)
			}
		})
	}
}
