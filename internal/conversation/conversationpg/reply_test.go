package conversationpg_test

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
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
)

func TestReplyPostingAndMoves(t *testing.T) {
	t.Parallel()
	pool := pgtest.New(t)
	f := mentionFixture(t, pool)
	m := membership(f.OrganizationID, f.MemberID)
	m.Member.OrganizationID, m.Member.JoinedEventSeq = f.OrganizationID, 1
	bob := orgtest.Member(t, pool, f.OrganizationID, identitytest.Account(t, pool, "bob@example.org", "Bob"), org.RoleMember, "bob", 1)
	topic := conversationtest.Topic(t, pool, f.OrganizationID, f.Channel.ID, "target")
	p := mentionPosting(pool)
	original, err := p.PostToTopic(t.Context(), membership(f.OrganizationID, bob), f.Channel.ID, &topic.ID, "**answer me**")
	requireNoError(t, err)
	quote, err := p.Post(t.Context(), m, f.Channel.ID, "quote me")
	requireNoError(t, err)
	key := conversation.NewIdempotencyKey()
	reader := conversationpg.NewReader(pool, compose.LookupMembers, compose.LookupAccounts, compose.EventCursor, compose.PageCountsIn)
	viewer := membership(f.OrganizationID, bob)
	viewer.Member.OrganizationID, viewer.Member.JoinedEventSeq = f.OrganizationID, 1
	pageBefore, err := reader.Page(t.Context(), viewer, f.Channel.ID, nil, nil)
	requireNoError(t, err)
	send := conversation.Send{Body: "answer", ReplyTo: &original.ID, Quote: &quote.ID, Key: &key}
	first, err := p.Send(t.Context(), m, f.Channel.ID, send)
	requireNoError(t, err)
	want := &conversation.Reply{MessageID: original.ID, MemberID: bob}
	if !reflect.DeepEqual(first.Message.Reply, want) || first.Topic != topic.ID || first.Message.EventSeq <= original.EventSeq || *first.Message.Quote.MessageID != quote.ID {
		t.Fatalf("reply author, topic, ordering or independent quote: %+v", first)
	}
	pageAfter, err := reader.Page(t.Context(), viewer, f.Channel.ID, nil, nil)
	requireNoError(t, err)
	if pageAfter.ChannelCounts[f.Channel.ID] != pageBefore.ChannelCounts[f.Channel.ID]+1 {
		t.Fatal("reply added an unread count beyond its own message")
	}
	before := mentionState(t, pool, f.OrganizationID)
	for _, tc := range []struct {
		name         string
		reply, quote *kernel.ID
		want         error
	}{
		{"unchanged", &original.ID, &quote.ID, nil},
		{"reply changed", &quote.ID, &quote.ID, conversation.ErrPostKeyReused},
		{"quote changed", &original.ID, &original.ID, conversation.ErrPostKeyReused},
		{"both changed", &quote.ID, &original.ID, conversation.ErrPostKeyReused},
		{"reply removed", nil, &quote.ID, conversation.ErrPostKeyReused},
	} {
		send.ReplyTo, send.Quote = tc.reply, tc.quote
		again, err := p.Send(t.Context(), m, f.Channel.ID, send)
		if !errors.Is(err, tc.want) || !again.Repeat || again.Message.ID != first.Message.ID || mentionState(t, pool, f.OrganizationID) != before {
			t.Fatalf("%s: %+v, %v", tc.name, again, err)
		}
	}
	explicit, err := p.Send(t.Context(), m, f.Channel.ID, conversation.Send{Body: "elsewhere", ReplyTo: &original.ID, Topic: &f.Channel.DefaultTopicID})
	requireNoError(t, err)
	if explicit.Message.TopicID != f.Channel.DefaultTopicID {
		t.Fatal("explicit topic ignored")
	}
	foreign := conversationtest.OrganizationWithOwner(t, pool, "foreign", "general")
	other, err := p.Post(t.Context(), membership(foreign.OrganizationID, foreign.MemberID), foreign.Channel.ID, "foreign")
	requireNoError(t, err)
	channel := conversationtest.Channel(t, pool, f.OrganizationID, "other", false)
	outside, err := p.Post(t.Context(), m, channel.ID, "other channel")
	requireNoError(t, err)
	before = mentionState(t, pool, f.OrganizationID)
	for _, target := range []kernel.ID{other.ID, outside.ID, {0xee}} {
		_, err := p.Send(t.Context(), m, f.Channel.ID, conversation.Send{Body: "refused", ReplyTo: &target})
		if !errors.Is(err, conversation.ErrMessageNotFound) || mentionState(t, pool, f.OrganizationID) != before {
			t.Fatalf("out-of-scope reply wrote state: %v", err)
		}
	}
	brancher := conversationpg.NewBrancher(pool, compose.PostingSequence, compose.PostingEvents, compose.BranchReads, nil)
	for i, message := range []conversation.Message{original, first.Message} {
		_, err := brancher.Branch(t.Context(), m, f.Channel.ID, conversation.Branch{Messages: []kernel.ID{message.ID}, From: topic.ID, NewName: []string{"original moved", "reply moved"}[i]}, func(conversation.Topic) string { return "moved" })
		requireNoError(t, err)
	}
	pins := conversationpg.NewPinning(pool, compose.PostingSequence, compose.PostingEvents, nil)
	requireNoError(t, pins.Pin(t.Context(), m, f.Channel.ID, first.Message.ID))
	requireNoError(t, pins.Unpin(t.Context(), m, f.Channel.ID, first.Message.ID))
	before = mentionState(t, pool, f.OrganizationID)
	entry, err := reader.One(t.Context(), m, f.Channel.ID, first.Message.EventSeq)
	requireNoError(t, err)
	if !reflect.DeepEqual(entry.Reply, want) || entry.ReplyPreview.Text("@gone") != "answer me" || mentionState(t, pool, f.OrganizationID) != before {
		t.Fatalf("move, pin or read changed reply/read state: %+v", entry)
	}
	key = conversation.NewIdempotencyKey()
	send = conversation.Send{Body: "@owner answer", ReplyTo: &original.ID, Key: &key}
	_, err = p.Send(t.Context(), m, f.Channel.ID, send)
	requireNoError(t, err)
	again, err := p.Send(t.Context(), m, f.Channel.ID, send)
	if err != nil || !again.Repeat || again.Topic != again.Message.TopicID {
		t.Fatalf("typed-handle retry: %+v, %v", again, err)
	}
}

type unavailableReplyTargets struct{ conversation.ReadStore }

func (unavailableReplyTargets) GetMessages(context.Context, kernel.ID, kernel.ID, []kernel.ID) ([]conversation.Message, error) {
	return nil, nil
}

func TestReplyPreviewStatementCount(t *testing.T) {
	t.Parallel()
	pool := pgtest.New(t)
	f := mentionFixture(t, pool)
	m := membership(f.OrganizationID, f.MemberID)
	p := mentionPosting(pool)
	original, err := p.Post(t.Context(), m, f.Channel.ID, "**hello** "+mentionToken(f.MemberID)+" "+strings.Repeat("界", 210))
	requireNoError(t, err)
	counter := platform.NewQueryCounter()
	config := pool.Config()
	config.ConnConfig.Tracer = counter
	traced, err := pgxpool.NewWithConfig(t.Context(), config)
	requireNoError(t, err)
	t.Cleanup(traced.Close)
	reader := conversationpg.NewReader(traced, compose.LookupMembers, compose.LookupAccounts, compose.EventCursor, nil)
	var baseline int64
	var reply conversation.Message
	posted := 0
	for _, count := range []int{1, 50} {
		for ; posted < count; posted++ {
			sent, err := p.Send(t.Context(), m, f.Channel.ID, conversation.Send{Body: "answer", ReplyTo: &original.ID})
			requireNoError(t, err)
			reply = sent.Message
		}
		before := counter.Counts()
		page, err := reader.Page(t.Context(), m, f.Channel.ID, nil, nil)
		requireNoError(t, err)
		delta := counter.Counts().Queries - before.Queries
		if baseline == 0 {
			baseline = delta
		}
		if delta != baseline {
			t.Fatalf("%d replies: %d queries, want %d", count, delta, baseline)
		}
		for _, entry := range page.Entries {
			if entry.Reply == nil {
				continue
			}
			preview := entry.ReplyPreview
			text := preview.Text("@gone")
			if !preview.Available || preview.Author.Handle != "owner" || len([]rune(text)) != 200 || !strings.HasPrefix(text, "hello @owner ") || strings.Contains(text, "**") {
				t.Fatalf("preview is not current, resolved, bounded text: %+v %q", preview, text)
			}
		}
		t.Logf("%d replies: %d page queries", count, delta)
	}
	_, err = pool.Exec(t.Context(), "UPDATE message SET body='current plain text', format='plain_text' WHERE organization_id=$1 AND id=$2", f.OrganizationID, original.ID)
	requireNoError(t, err)
	entry, err := reader.One(t.Context(), m, f.Channel.ID, reply.EventSeq)
	requireNoError(t, err)
	if entry.ReplyPreview.Text("@gone") != "current plain text" {
		t.Fatal("preview retained a snapshot of the old body")
	}
	runner := conversationpg.NewSnapshotRunnerForTest(pool)
	unavailable := conversation.NewReader(runner, runner, func(s platform.Snapshot) conversation.ReadStore {
		return unavailableReplyTargets{postgres.ReadStoreIn(s)}
	}, compose.LookupMembers, compose.LookupAccounts, compose.EventCursor, nil)
	entry, err = unavailable.One(t.Context(), m, f.Channel.ID, reply.EventSeq)
	requireNoError(t, err)
	if entry.ReplyPreview == nil || entry.ReplyPreview.Available || entry.ReplyPreview.Text("@gone") != "" {
		t.Fatal("missing reply target did not return a neutral state")
	}
}
