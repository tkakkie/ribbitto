package conversationpg_test

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/tkakkie/ribbitto/internal/compose"
	"github.com/tkakkie/ribbitto/internal/conversation"
	"github.com/tkakkie/ribbitto/internal/conversation/conversationpg"
	"github.com/tkakkie/ribbitto/internal/conversation/conversationtest"
	"github.com/tkakkie/ribbitto/internal/conversation/internal/postgres"
	"github.com/tkakkie/ribbitto/internal/identity/identitytest"
	"github.com/tkakkie/ribbitto/internal/kernel"
	"github.com/tkakkie/ribbitto/internal/org"
	"github.com/tkakkie/ribbitto/internal/org/orgtest"
	platform "github.com/tkakkie/ribbitto/internal/platform/postgres"
	"github.com/tkakkie/ribbitto/internal/platform/postgres/pgtest"
	"github.com/tkakkie/ribbitto/internal/realtime"
)

// staleReads finds a message of another channel, as a read made before a
// move would.
type staleReads struct {
	conversation.ReadStore
	channel kernel.ID
}

func (r staleReads) GetMessages(ctx context.Context, organizationID, _ kernel.ID, ids []kernel.ID) ([]conversation.Message, error) {
	return r.ReadStore.GetMessages(ctx, organizationID, r.channel, ids)
}

// Posting a quote copies the original's own text in the posting
// transaction: a plain_text body as stored, a chat_v1 extraction resolved
// at quote time, never the original's quote. An unknown original, another
// organisation's and another channel's are one error that writes nothing
// and takes no event_seq, as is an extraction with nothing left.
func TestPostingQuoteAgainstPostgreSQL(t *testing.T) {
	t.Parallel()
	pool := pgtest.New(t)
	ctx := t.Context()
	acme := conversationtest.OrganizationWithOwner(t, pool, "acme", "general")
	globex := conversationtest.OrganizationWithOwner(t, pool, "globex", "general")
	random := conversationtest.Channel(t, pool, acme.OrganizationID, "random", false)
	bob := orgtest.Member(t, pool, acme.OrganizationID, identitytest.Account(t, pool, "bob@example.org", "Bob"), org.RoleMember, "bob", 1)
	owner, bobM := membership(acme.OrganizationID, acme.MemberID), membership(acme.OrganizationID, bob)
	posting := conversationpg.NewPagePosting(pool, compose.PostingSequence, compose.PostingEvents, nil, compose.LookupMembers, nil)
	ref := func(id kernel.ID) string { return "<@" + realtime.FormatPayloadID(id) + ">" }
	post := func(m org.Membership, channel kernel.ID, body string) conversation.Message {
		t.Helper()
		posted, err := posting.Post(ctx, m, channel, body)
		requireNoError(t, err)
		return posted
	}
	// Posting stores only checked chat_v1, so these originals are rewritten
	// as an older or bad row could hold them.
	stored := func(body string, format conversation.Format) conversation.Message {
		t.Helper()
		posted := post(bobM, acme.Channel.ID, "fixture")
		_, err := pool.Exec(ctx, "UPDATE message SET body = $2, format = $3 WHERE id = $1", posted.ID, body, string(format))
		requireNoError(t, err)
		return posted
	}
	send := func(p *conversation.Posting, id kernel.ID, key *conversation.IdempotencyKey) (conversation.Sent, error) {
		return p.Send(ctx, owner, acme.Channel.ID, conversation.Send{Body: "reply", Quote: &id, Key: key, UnknownMember: "@gone"})
	}

	rich := post(bobM, acme.Channel.ID, "**Deploy** "+ref(bob)+"\n> soon `a*b`\n```go\nx := 1\n```")
	var quoting conversation.Message
	for _, tc := range []struct {
		name     string
		original conversation.Message
		text     string
	}{
		{"plain_text as stored", stored("**not bold** "+ref(bob), conversation.FormatPlainText), "**not bold** " + ref(bob)},
		{"chat_v1 resolved at quote time", rich, "Deploy @bob\nsoon a*b\nx := 1"},
		{"unresolved reference", stored("hi "+ref(kernel.ID{0xee}), conversation.FormatChatV1), "hi @gone"},
		{"trimmed at both ends", post(bobM, acme.Channel.ID, "` padded `"), "padded"},
	} {
		sent, err := send(posting, tc.original.ID, nil)
		want := &conversation.Quote{MessageID: &tc.original.ID, MemberID: bob, Text: tc.text}
		if err != nil || !reflect.DeepEqual(sent.Message.Quote, want) {
			t.Fatalf("%s: %+v, %v; want %+v", tc.name, sent.Message.Quote, err, want)
		}
		quoting = sent.Message
	}
	// One level: quoting a quote copies only that message's own body.
	if sent, err := send(posting, quoting.ID, nil); err != nil || sent.Message.Quote.Text != "reply" || sent.Message.Quote.MemberID != acme.MemberID {
		t.Fatalf("quote of a quote: %+v, %v", sent.Message.Quote, err)
	}
	// A repeated key answers the first message only with the same quote.
	key := conversation.IdempotencyKey{0x42}
	first, err := send(posting, rich.ID, &key)
	requireNoError(t, err)
	if again, err := send(posting, rich.ID, &key); err != nil || !again.Repeat || again.Message.ID != first.Message.ID {
		t.Fatalf("repeat: %+v, %v", again, err)
	}
	if _, err := send(posting, quoting.ID, &key); !errors.Is(err, conversation.ErrPostKeyReused) {
		t.Fatalf("repeat with another quote: %v", err)
	}
	if _, err := posting.Send(ctx, owner, acme.Channel.ID, conversation.Send{Body: "reply", Key: &key}); !errors.Is(err, conversation.ErrPostKeyReused) {
		t.Fatalf("repeat without the quote: %v", err)
	}

	state := func() (seq int64, messages int) {
		t.Helper()
		requireNoError(t, pool.QueryRow(ctx, "SELECT event_seq, (SELECT count(*) FROM message) FROM organization WHERE id = $1", acme.OrganizationID).Scan(&seq, &messages))
		return seq, messages
	}
	empty := post(bobM, acme.Channel.ID, ">")
	other := post(bobM, random.ID, "secret")
	far := post(membership(globex.OrganizationID, globex.MemberID), globex.Channel.ID, "secret")
	// The scoped read in the transaction decides, not the snapshot before it.
	moved := conversation.NewPagePosting(conversationpg.NewTxRunnerForTest(pool),
		func(tx platform.Tx) conversation.Writer { return postgres.WriterIn(tx) }, compose.PostingSequence, compose.PostingEvents, nil,
		conversationpg.NewSnapshotRunnerForTest(pool), compose.LookupMembers,
		func(s platform.Snapshot) conversation.ReadStore {
			return staleReads{ReadStore: postgres.ReadStoreIn(s), channel: random.ID}
		}, nil)
	before, count := state()
	for _, tc := range []struct {
		name    string
		posting *conversation.Posting
		id      kernel.ID
		want    error
	}{
		{"unknown", posting, kernel.ID{0xee}, conversation.ErrQuoteNotFound},
		{"another organisation's", posting, far.ID, conversation.ErrQuoteNotFound},
		{"another channel's", posting, other.ID, conversation.ErrQuoteNotFound},
		{"another channel's after the snapshot", moved, other.ID, conversation.ErrQuoteNotFound},
		{"nothing left to quote", posting, empty.ID, conversation.ErrInvalidBody},
	} {
		_, err := send(tc.posting, tc.id, nil)
		if !errors.Is(err, tc.want) || errors.Is(err, conversation.ErrInvalidBody) != (tc.want == conversation.ErrInvalidBody) {
			t.Errorf("%s: %v, want %v", tc.name, err, tc.want)
		}
		if seq, messages := state(); seq != before || messages != count {
			t.Errorf("%s wrote: seq %d, %d messages; was %d, %d", tc.name, seq, messages, before, count)
		}
	}
}
