package conversation_test

import (
	"context"
	"slices"
	"testing"

	"github.com/tkakkie/ribbitto/internal/conversation"
	"github.com/tkakkie/ribbitto/internal/kernel"
	"github.com/tkakkie/ribbitto/internal/org"
	platform "github.com/tkakkie/ribbitto/internal/platform/postgres"
	"github.com/tkakkie/ribbitto/internal/realtime"
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

type returnedMentionWriter struct {
	*postingFake
	ids []kernel.ID
}

func (w *returnedMentionWriter) InsertMessage(ctx context.Context, organizationID, channelID, topicID, memberID kernel.ID, body string, seq int64, key *conversation.IdempotencyKey, proof conversation.TypedBodyProof, quote *conversation.Quote, reply *conversation.Reply) (conversation.Message, error) {
	posted, err := w.postingFake.InsertMessage(ctx, organizationID, channelID, topicID, memberID, body, seq, key, proof, quote, reply)
	// Make the returned values distinguishable from the request and reserved
	// sequence, so reparsing or using the input sequence fails the assertion.
	posted.ID, posted.EventSeq, posted.Body = kernel.ID{0x55}, 99, "different returned body"
	return posted, err
}

func (w *returnedMentionWriter) InsertMentions(_ context.Context, organizationID, messageID kernel.ID, seq int64, ids []kernel.ID) error {
	w.scope(organizationID, kernel.ID{3})
	if messageID != (kernel.ID{0x55}) || seq != 99 || !slices.Equal(ids, w.ids) {
		w.t.Fatalf("mention arguments: message=%v seq=%d ids=%v", messageID, seq, ids)
	}
	return w.step("mentions")
}

func (w *returnedMentionWriter) Append(_ context.Context, organizationID kernel.ID, seq int64, _ realtime.EventKind, _ *kernel.ID, data []byte) error {
	w.scope(organizationID, kernel.ID{3})
	posted, err := conversation.DecodePosted(data)
	if err != nil || posted.MessageID != (kernel.ID{0x55}) || seq != 7 {
		w.t.Fatalf("event=%+v sequence=%d, %v", posted, seq, err)
	}
	return w.step("append")
}

func TestPostingReusesValidatedMentionsAndReturnedSequence(t *testing.T) {
	f := &postingFake{t: t, topic: kernel.ID{6}}
	id := kernel.ID{0xa1}
	w := &returnedMentionWriter{postingFake: f, ids: []kernel.ID{id}}
	d := &referenceDirectory{t: t, members: map[kernel.ID][]kernel.ID{{1}: {id}}}
	p := conversation.NewPagePosting(f, func(platform.Tx) conversation.Writer { return w },
		func(platform.Tx) conversation.EventSequence { return f },
		func(platform.Tx) conversation.EventAppender { return w },
		nil, d, func(platform.Snapshot) conversation.MemberDirectory { return d }, nil, nil)
	m := org.Membership{Organization: org.Organization{ID: kernel.ID{1}}, Member: org.Member{ID: kernel.ID{2}}}
	_, err := p.Post(t.Context(), m, kernel.ID{3}, "<@a1000000-0000-0000-0000-000000000000>")
	if err != nil || len(d.lookups) != 1 || !slices.Equal(f.calls, []string{"begin", "sequence", "default", "insert", "mentions", "append", "commit"}) {
		t.Fatalf("post=%v calls=%v lookups=%v", err, f.calls, d.lookups)
	}
}
