package postgres_test

import (
	"errors"
	"slices"
	"testing"

	"github.com/tkakkie/ribbitto/internal/app/authz"
	appchannel "github.com/tkakkie/ribbitto/internal/app/channel"
	"github.com/tkakkie/ribbitto/internal/app/message"
	"github.com/tkakkie/ribbitto/internal/domain"
	"github.com/tkakkie/ribbitto/internal/infra/postgres"
	"github.com/tkakkie/ribbitto/internal/infra/postgres/pgtest"
)

func TestPostMessage(t *testing.T) {
	pool := pgtest.New(t)
	ctx := t.Context()
	// Two organisations, each with one member and a default channel.
	memberships := map[string]authz.Membership{}
	channels := map[string]domain.ID{}
	for _, slug := range []string{"acme", "globex"} {
		var org, account, member domain.ID
		requireAccountSchema(t, pool.QueryRow(ctx, "INSERT INTO organization (slug, name) VALUES ($1, $1) RETURNING id", slug).Scan(&org))
		requireAccountSchema(t, pool.QueryRow(ctx, "INSERT INTO account (email, display_name, password_hash) VALUES ($1 || '@example.org', $1, '$argon2id$x') RETURNING id", slug).Scan(&account))
		requireAccountSchema(t, pool.QueryRow(ctx, "INSERT INTO member (organization_id, account_id, role, joined_event_seq, handle) VALUES ($1, $2, 'owner', 1, 'owner') RETURNING id", org, account).Scan(&member))
		requireAccountSchema(t, pool.QueryRow(ctx, "UPDATE organization SET event_seq = 1 WHERE id = $1 RETURNING id", org).Scan(&org))
		ch, err := postgres.NewChannelStore(pool).CreateChannel(ctx, org, appchannel.DefaultName, true)
		requireAccountSchema(t, err)
		memberships[slug] = authz.Membership{Organization: domain.Organization{ID: org, Slug: slug}, Member: domain.Member{ID: member, OrganizationID: org}}
		channels[slug] = ch.ID
	}
	service := message.New(postgres.NewPostingStore(pool))
	state := func(slug string) (seq int64, messages int) {
		t.Helper()
		requireAccountSchema(t, pool.QueryRow(ctx, "SELECT o.event_seq, (SELECT count(*) FROM message m WHERE m.organization_id = o.id) FROM organization o WHERE slug = $1", slug).Scan(&seq, &messages))
		return seq, messages
	}

	posted, err := service.Post(ctx, memberships["acme"], channels["acme"], " hello\r\n ")
	if err != nil || posted.Body != "hello" || posted.EventSeq != 2 || posted.MemberID != memberships["acme"].Member.ID {
		t.Fatalf("post: %+v, %v", posted, err)
	}

	// A member of globex cannot post into acme's channel, even knowing its
	// id, and the failed attempt takes no sequence from either organisation.
	if _, err := service.Post(ctx, memberships["globex"], channels["acme"], "intruder"); !errors.Is(err, appchannel.ErrNotFound) {
		t.Fatalf("cross-organisation post: %v", err)
	}
	// A failed insert (a body the database refuses) rolls the sequence back.
	if _, err := postgres.NewPostingStore(pool).Post(ctx, memberships["acme"].Organization.ID, channels["acme"], memberships["acme"].Member.ID, " untrimmed"); err == nil {
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
