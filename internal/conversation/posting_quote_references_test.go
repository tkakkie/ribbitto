package conversation_test

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/tkakkie/ribbitto/internal/conversation"
	"github.com/tkakkie/ribbitto/internal/kernel"
	"github.com/tkakkie/ribbitto/internal/org"
	platform "github.com/tkakkie/ribbitto/internal/platform/postgres"
	"github.com/tkakkie/ribbitto/internal/realtime"
)

type quoteRefsReads struct {
	conversation.ReadStore
	found []conversation.Message
	err   error
}

func (r quoteRefsReads) GetMessages(_ context.Context, _, _ kernel.ID, _ []kernel.ID) ([]conversation.Message, error) {
	return r.found, r.err
}

func TestPostingQuoteReferences(t *testing.T) {
	orgID, decoyOrgID := kernel.ID{1}, kernel.ID{9}
	senderID, authorID := kernel.ID{2}, kernel.ID{0xc1}
	channelID := kernel.ID{3}
	originalID, secondID := kernel.ID{4}, kernel.ID{5}
	orgMemberID, decoyMemberID := kernel.ID{0xa1}, kernel.ID{0xb1}

	// Member.OrganizationID differs from Organization.ID so a lookup or replay
	// scoped to the member's field crosses into the decoy organisation.
	m := org.Membership{
		Organization: org.Organization{ID: orgID},
		Member:       org.Member{ID: senderID, OrganizationID: decoyOrgID},
	}
	ref := func(id kernel.ID) string { return "<@" + realtime.FormatPayloadID(id) + ">" }
	refBody := "hi " + ref(orgMemberID)
	decoyBody := "hi " + ref(decoyMemberID)
	copyOne := conversation.Message{ID: originalID, OrganizationID: orgID, ChannelID: channelID, MemberID: authorID, Format: conversation.FormatChatV1, Body: refBody}
	copyTwo := copyOne
	copyTwo.ID = secondID
	readFailure := errors.New("quote read failed")
	dirFailure := errors.New("directory failed")

	for _, tt := range []struct {
		name                            string
		format                          conversation.Format
		body                            string
		customReads, nilQuotes, nilDirs bool
		found                           []conversation.Message
		readErr, dirErr, wantErr        error
		wantText, wantError             string
		wantLookups                     [][]kernel.ID
	}{
		{name: "chat_v1 member of organisation", format: conversation.FormatChatV1, body: refBody, wantText: "hi @member", wantLookups: [][]kernel.ID{{orgMemberID}}},
		{name: "chat_v1 member of decoy organisation", format: conversation.FormatChatV1, body: decoyBody, wantText: "hi (former member)", wantLookups: [][]kernel.ID{{decoyMemberID}}},
		{name: "plain_text with reference token", format: conversation.FormatPlainText, body: refBody, wantText: refBody},
		{name: "chat_v1 without references", format: conversation.FormatChatV1, body: "just **words**", wantText: "just words"},
		{name: "snapshot read returns zero rows", format: conversation.FormatChatV1, body: refBody, customReads: true, wantText: "hi (former member)"},
		{name: "snapshot read returns two rows", format: conversation.FormatChatV1, body: refBody, customReads: true, found: []conversation.Message{copyOne, copyTwo}, wantText: "hi (former member)"},
		{name: "nil member directory without references", format: conversation.FormatChatV1, body: "just **words**", nilDirs: true, wantText: "just words"},
		{name: "snapshot read failure", format: conversation.FormatChatV1, body: refBody, customReads: true, readErr: readFailure, wantErr: readFailure, wantError: "reading quoted message: quote read failed"},
		{name: "member lookup failure", format: conversation.FormatChatV1, body: refBody, dirErr: dirFailure, wantLookups: [][]kernel.ID{{orgMemberID}}, wantErr: dirFailure, wantError: "reading quoted message: directory failed"},
		{name: "nil quotes reader", format: conversation.FormatChatV1, body: refBody, nilQuotes: true, wantError: "posting has no reader to check quotes"},
		{name: "nil member directory with reference", format: conversation.FormatChatV1, body: refBody, nilDirs: true, wantError: "reading quoted message: posting has no member directory to resolve a quote"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			s := &quoteRetryStore{
				keyedStore: &keyedStore{state: keyedState{seq: map[kernel.ID]int64{}, keyed: map[keyScope]conversation.Message{}}},
				original:   conversation.Message{ID: originalID, OrganizationID: orgID, ChannelID: channelID, MemberID: authorID, Format: tt.format, Body: tt.body},
			}
			d := &referenceDirectory{t: t, err: tt.dirErr, members: map[kernel.ID][]kernel.ID{orgID: {orgMemberID}, decoyOrgID: {decoyMemberID}}}
			var members conversation.MemberDirectoryIn
			if !tt.nilDirs {
				members = func(platform.Snapshot) conversation.MemberDirectory { return d }
			}
			var quotes conversation.ReadStoreIn
			if !tt.nilQuotes {
				quotes = func(platform.Snapshot) conversation.ReadStore {
					if tt.customReads {
						return quoteRefsReads{found: tt.found, err: tt.readErr}
					}
					return quoteRetryReads{store: s}
				}
			}
			p := conversation.NewPagePosting(s,
				func(platform.Tx) conversation.Writer { return s },
				func(platform.Tx) conversation.EventSequence { return s },
				func(platform.Tx) conversation.EventAppender { return s },
				nil, d, members, quotes, nil)
			key := conversation.NewIdempotencyKey()
			send := conversation.Send{Body: "reply", Key: &key, Quote: &originalID, UnknownMember: "(former member)"}
			sent, err := p.Send(t.Context(), m, channelID, send)
			if !reflect.DeepEqual(d.lookups, tt.wantLookups) {
				t.Fatalf("lookups = %v, want %v", d.lookups, tt.wantLookups)
			}
			if tt.wantError != "" {
				if err == nil || err.Error() != tt.wantError {
					t.Fatalf("error = %v, want %q", err, tt.wantError)
				}
				if tt.wantErr != nil && !errors.Is(err, tt.wantErr) {
					t.Fatalf("error %v does not wrap %v", err, tt.wantErr)
				}
				if errors.Is(err, conversation.ErrInvalidBody) {
					t.Fatalf("error %v must not wrap ErrInvalidBody", err)
				}
				if sent != (conversation.Sent{}) || s.state.inserted != 0 {
					t.Fatalf("sent = %+v, inserted = %d; want zero Sent and 0 inserted", sent, s.state.inserted)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if sent.Message.Body != "reply" || sent.Repeat || sent.Message.Quote == nil ||
				sent.Message.Quote.Text != tt.wantText ||
				sent.Message.Quote.MessageID == nil || *sent.Message.Quote.MessageID != originalID ||
				sent.Message.Quote.MemberID != authorID {
				t.Fatalf("sent = %+v, quote = %+v; want reply, repeat=false, quote text %q message %x member %x",
					sent, sent.Message.Quote, tt.wantText, originalID, authorID)
			}
			again, err := p.Send(t.Context(), m, channelID, send)
			if err != nil || !again.Repeat || again.Message.ID != sent.Message.ID {
				t.Fatalf("replay = %+v, %v; want repeat of %x", again, err, sent.Message.ID)
			}
		})
	}
}
