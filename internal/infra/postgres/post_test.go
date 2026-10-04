package postgres_test

import (
	"errors"
	"slices"
	"testing"

	"github.com/tkakkie/ribbitto/internal/app/message"
	"github.com/tkakkie/ribbitto/internal/app/topic"
	"github.com/tkakkie/ribbitto/internal/conversation"
	"github.com/tkakkie/ribbitto/internal/domain"
	"github.com/tkakkie/ribbitto/internal/infra/postgres"
	"github.com/tkakkie/ribbitto/internal/infra/postgres/pgtest"
	"github.com/tkakkie/ribbitto/internal/org"
)

func TestPostMessage(t *testing.T) {
	t.Parallel()
	pool := pgtest.New(t)
	ctx := t.Context()
	// Two organisations, each with one member and a default channel.
	memberships := map[string]org.Membership{}
	channels := map[string]domain.ID{}
	for _, slug := range []string{"acme", "globex"} {
		fixture := pgtest.OrganizationWithOwner(t, pool, slug, conversation.DefaultChannelName)
		// These fixture memberships predate logging, as on an upgraded database.
		_, err := pool.Exec(ctx, "UPDATE organization SET event_log_boundary_seq = event_seq WHERE id = $1", fixture.OrganizationID)
		requireNoError(t, err)
		memberships[slug] = org.Membership{Organization: org.Organization{ID: fixture.OrganizationID, Slug: slug}, Member: org.Member{ID: fixture.MemberID, OrganizationID: fixture.OrganizationID}}
		channels[slug] = fixture.Channel.ID
	}
	service := message.New(postgres.NewPostingStore(pool, eventSequence, appendEvents))
	state := func(slug string) (seq int64, messages int) {
		t.Helper()
		requireNoError(t, pool.QueryRow(ctx, "SELECT o.event_seq, (SELECT count(*) FROM message m WHERE m.organization_id = o.id) FROM organization o WHERE slug = $1", slug).Scan(&seq, &messages))
		assertEventLog(t, pool, memberships[slug].Organization.ID, seq)
		return seq, messages
	}

	posted, err := service.Post(ctx, memberships["acme"], channels["acme"], " hello\r\n ")
	if err != nil || posted.Body != "hello" || posted.EventSeq != 2 || posted.MemberID != memberships["acme"].Member.ID {
		t.Fatalf("post: %+v, %v", posted, err)
	}

	// A member of globex cannot post into acme's channel, even knowing its
	// id, and the failed attempt takes no sequence from either organisation.
	if _, err := service.Post(ctx, memberships["globex"], channels["acme"], "intruder"); !errors.Is(err, conversation.ErrChannelNotFound) {
		t.Fatalf("cross-organisation post: %v", err)
	}
	// A failed insert (a body the database refuses) rolls the sequence back.
	if _, err := postgres.NewPostingStore(pool, eventSequence, appendEvents).Post(ctx, memberships["acme"].Organization.ID, channels["acme"], memberships["acme"].Member.ID, " untrimmed"); err == nil {
		t.Fatal("the database accepted an untrimmed body")
	}
	if seq, n := state("acme"); seq != 2 || n != 1 {
		t.Fatalf("acme after failures: event_seq=%d messages=%d", seq, n)
	}
	if seq, n := state("globex"); seq != 1 || n != 0 {
		t.Fatalf("globex after failures: event_seq=%d messages=%d", seq, n)
	}

	// Concurrent posts, with no other writer, get consecutive sequence values
	// after the counter's value before them.
	const posts = 20
	before, _ := state("acme")
	results := make(chan int64, posts)
	errs := make(chan error, posts)
	for range posts {
		go func() {
			p, err := service.Post(ctx, memberships["acme"], channels["acme"], "concurrent")
			errs <- err
			results <- p.EventSeq
		}()
	}
	var got []int64
	for range posts {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
		got = append(got, <-results)
	}
	slices.Sort(got)
	for i, seq := range got {
		if seq != before+int64(i)+1 {
			t.Fatalf("sequences %v are not consecutive after %d", got, before)
		}
	}
	if seq, n := state("acme"); seq != before+posts || n != posts+1 {
		t.Fatalf("after concurrent posts: event_seq=%d messages=%d", seq, n)
	}
}

// An organisation ID that does not exist (the RESTRICT foreign keys keep a
// used organisation from being deleted) makes org's sequence answer
// org.ErrNotFound, which posting and branching return unchanged, writing
// nothing, even with another organisation's real channel, topic and member.
func TestPostingAndBranchingIntoUnknownOrganization(t *testing.T) {
	t.Parallel()
	pool := pgtest.New(t)
	ctx := t.Context()
	acme := pgtest.OrganizationWithOwner(t, pool, "acme", conversation.DefaultChannelName)
	posting, branching := postgres.NewPostingStore(pool, eventSequence, appendEvents), postgres.NewBranchStore(pool, eventSequence, appendEvents)
	posted, err := posting.Post(ctx, acme.OrganizationID, acme.Channel.ID, acme.MemberID, "kept")
	requireNoError(t, err)
	written := func() (counts [4]int64) {
		t.Helper()
		requireNoError(t, pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM message), (SELECT count(*) FROM topic),
			(SELECT count(*) FROM event_log), (SELECT sum(event_seq) FROM organization)`).Scan(&counts[0], &counts[1], &counts[2], &counts[3]))
		return counts
	}
	before := written()
	unknown := domain.ID{0xee}
	topicID := acme.Channel.DefaultTopicID
	for name, run := range map[string]func() error{
		"post": func() error {
			_, err := posting.Post(ctx, unknown, acme.Channel.ID, acme.MemberID, "lost")
			return err
		},
		"post to a topic": func() error {
			_, err := posting.PostToTopic(ctx, unknown, acme.Channel.ID, acme.MemberID, &topicID, "lost")
			return err
		},
		"branch": func() error {
			_, _, err := branching.Branch(ctx, unknown, acme.Channel.ID, acme.MemberID,
				topic.Branch{Messages: []domain.ID{posted.ID}, From: topicID, NewName: "lost"}, func(domain.Topic) string { return "lost" })
			return err
		},
	} {
		if err := run(); !errors.Is(err, org.ErrNotFound) {
			t.Fatalf("%s into an unknown organisation: %v, want org.ErrNotFound", name, err)
		}
		if after := written(); after != before {
			t.Fatalf("%s into an unknown organisation wrote: %v, was %v", name, after, before)
		}
	}
}
