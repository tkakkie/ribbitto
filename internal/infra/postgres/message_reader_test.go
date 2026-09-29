package postgres_test

import (
	"fmt"
	"slices"
	"testing"

	"github.com/tkakkie/ribbitto/internal/app/authz"
	appchannel "github.com/tkakkie/ribbitto/internal/app/channel"
	"github.com/tkakkie/ribbitto/internal/app/message"
	"github.com/tkakkie/ribbitto/internal/domain"
	"github.com/tkakkie/ribbitto/internal/infra/postgres"
	"github.com/tkakkie/ribbitto/internal/infra/postgres/pgtest"
)

func TestMessagePaging(t *testing.T) {
	pool := pgtest.New(t)
	ctx := t.Context()
	memberships := map[string]authz.Membership{}
	for _, slug := range []string{"acme", "globex"} {
		org := pgtest.Organization(t, ctx, pool, slug, slug, 1)
		account := pgtest.Account(t, ctx, pool, slug+"@example.org", slug)
		member := pgtest.Member(t, ctx, pool, org, account, domain.RoleOwner, "owner", 1)
		memberships[slug] = authz.Membership{Organization: domain.Organization{ID: org, Slug: slug}, Member: domain.Member{ID: member, OrganizationID: org}}
	}
	acme, globex := memberships["acme"], memberships["globex"]
	channels := map[string]domain.ID{}
	for i, name := range []string{"empty", "exact", "partial", "noise"} {
		ch := pgtest.Channel(t, ctx, pool, acme.Organization.ID, name, i == 0)
		channels[name] = ch.ID
	}
	foreign := pgtest.Channel(t, ctx, pool, globex.Organization.ID, appchannel.DefaultName, true)

	// Interleave posts so that every channel's event_seq values have gaps
	// filled by another channel's, and globex reuses acme's numbers.
	service := message.New(postgres.NewPostingStore(pool))
	sizes := map[string]int{"exact": 2 * message.PageSize, "partial": message.PageSize + 1}
	posted := map[string][]string{}
	var foreignSeqs []int64
	for i := range 2 * message.PageSize {
		for _, name := range []string{"exact", "partial"} {
			if i < sizes[name] {
				body := fmt.Sprintf("%s %d", name, i)
				_, err := service.Post(ctx, acme, channels[name], body)
				requireNoError(t, err)
				posted[name] = append(posted[name], body)
			}
		}
		if i%10 == 0 {
			_, err := service.Post(ctx, acme, channels["noise"], "noise")
			requireNoError(t, err)
			m, err := service.Post(ctx, globex, foreign.ID, "foreign")
			requireNoError(t, err)
			foreignSeqs = append(foreignSeqs, m.EventSeq)
		}
	}

	reader := postgres.MessageReader{Pool: pool}
	for _, name := range []string{"empty", "exact", "partial"} {
		t.Run(name, func(t *testing.T) {
			var got []string
			var before *int64
			pages := 0
			for {
				page, err := reader.Before(ctx, acme, channels[name], before)
				if err != nil {
					t.Fatal(err)
				}
				pages++
				var bodies []string
				for _, e := range page.Entries {
					if e.ChannelID != channels[name] || e.OrganizationID != acme.Organization.ID || e.Handle != "owner" || e.DisplayName != "acme" {
						t.Fatalf("entry from the wrong scope or without its author: %+v", e)
					}
					bodies = append(bodies, e.Body)
				}
				got = append(bodies, got...)
				if !page.Older {
					break
				}
				if len(page.Entries) != message.PageSize || pages > 10 {
					t.Fatalf("page %d has %d entries and claims older ones", pages, len(page.Entries))
				}
				before = &page.Entries[0].EventSeq
			}
			if want := (sizes[name] + message.PageSize - 1) / message.PageSize; pages != max(want, 1) {
				t.Fatalf("%d pages for %d messages", pages, sizes[name])
			}
			if !slices.Equal(got, posted[name]) {
				t.Fatalf("paging returned %d messages, not every message once in order: %q", len(got), got)
			}
		})
	}

	// before is only an upper bound. A value taken from another channel or
	// organisation still reads the URL channel within the member's
	// organisation, and a foreign membership cannot read acme's channel.
	noise, err := reader.Before(ctx, acme, channels["noise"], nil)
	requireNoError(t, err)
	for _, before := range []int64{noise.Entries[len(noise.Entries)-1].EventSeq, foreignSeqs[len(foreignSeqs)-1]} {
		page, err := reader.Before(ctx, acme, channels["exact"], &before)
		if err != nil || len(page.Entries) == 0 {
			t.Fatalf("before=%d: %d entries, %v", before, len(page.Entries), err)
		}
		for _, e := range page.Entries {
			if e.ChannelID != channels["exact"] || e.EventSeq >= before {
				t.Fatalf("before=%d returned %+v", before, e.Message)
			}
		}
	}
	if page, err := reader.Before(ctx, globex, channels["exact"], nil); err != nil || len(page.Entries) != 0 || page.Older {
		t.Fatalf("globex read acme's channel: %+v, %v", page, err)
	}
}
