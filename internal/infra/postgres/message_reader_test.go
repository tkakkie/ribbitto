package postgres_test

import (
	"errors"
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

func TestMessageOne(t *testing.T) {
	t.Parallel()
	pool := pgtest.New(t)
	ctx := t.Context()
	local := pgtest.OrganizationWithOwner(t, pool, "acme", appchannel.DefaultName)
	foreign := pgtest.OrganizationWithOwner(t, pool, "globex", appchannel.DefaultName)
	otherChannel := pgtest.Channel(t, pool, local.OrganizationID, "other", false)
	membership := authz.Membership{Organization: domain.Organization{ID: local.OrganizationID}, Member: domain.Member{ID: local.MemberID}}
	foreignMembership := authz.Membership{Organization: domain.Organization{ID: foreign.OrganizationID}, Member: domain.Member{ID: foreign.MemberID}}
	service := message.New(postgres.NewPostingStore(pool))
	posted, err := service.Post(ctx, membership, local.Channel.ID, "local body")
	requireNoError(t, err)
	foreignPost, err := service.Post(ctx, foreignMembership, foreign.Channel.ID, "foreign body")
	requireNoError(t, err)
	if posted.EventSeq != foreignPost.EventSeq {
		t.Fatal("fixture must reuse the same sequence across organisations")
	}
	// Names are current directory values, not values captured when posting.
	_, err = pool.Exec(ctx, `UPDATE account SET display_name = 'Current Name' WHERE id = $1`, local.AccountID)
	requireNoError(t, err)
	_, err = pool.Exec(ctx, `UPDATE member SET handle = 'current-handle' WHERE organization_id = $1 AND id = $2`, local.OrganizationID, local.MemberID)
	requireNoError(t, err)
	reader := postgres.MessageReader{Pool: pool}
	for _, tt := range []struct {
		name       string
		membership authz.Membership
		channel    domain.ID
		seq        int64
		want       message.Entry
		wantErr    error
	}{
		{"hydrated", membership, local.Channel.ID, posted.EventSeq, message.Entry{Message: posted, DisplayName: "Current Name", Handle: "current-handle"}, nil},
		{"missing", membership, local.Channel.ID, posted.EventSeq + 100, message.Entry{}, message.ErrNotFound},
		{"wrong channel", membership, otherChannel.ID, posted.EventSeq, message.Entry{}, message.ErrNotFound},
		{"foreign message", membership, foreign.Channel.ID, foreignPost.EventSeq, message.Entry{}, message.ErrNotFound},
		{"foreign member", foreignMembership, local.Channel.ID, posted.EventSeq, message.Entry{}, message.ErrNotFound},
		{"foreign hydrated", foreignMembership, foreign.Channel.ID, foreignPost.EventSeq, message.Entry{Message: foreignPost, DisplayName: "globex", Handle: "owner"}, nil},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got, err := reader.One(ctx, tt.membership, tt.channel, tt.seq)
			if !errors.Is(err, tt.wantErr) || got != tt.want {
				t.Fatalf("entry = %+v, error = %v; want %+v, %v", got, err, tt.want, tt.wantErr)
			}
		})
	}
}

func TestMessagePaging(t *testing.T) {
	t.Parallel()
	pool := pgtest.New(t)
	ctx := t.Context()
	memberships := map[string]authz.Membership{}
	defaults := map[string]domain.Channel{}
	for _, slug := range []string{"acme", "globex"} {
		name := appchannel.DefaultName
		if slug == "acme" {
			name = "empty"
		}
		fixture := pgtest.OrganizationWithOwner(t, pool, slug, name)
		memberships[slug] = authz.Membership{Organization: domain.Organization{ID: fixture.OrganizationID, Slug: slug}, Member: domain.Member{ID: fixture.MemberID, OrganizationID: fixture.OrganizationID}}
		defaults[slug] = fixture.Channel
	}
	acme, globex := memberships["acme"], memberships["globex"]
	channels := map[string]domain.ID{"empty": defaults["acme"].ID}
	for _, name := range []string{"exact", "partial", "noise"} {
		ch := pgtest.Channel(t, pool, acme.Organization.ID, name, false)
		channels[name] = ch.ID
	}
	foreign := defaults["globex"]

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
