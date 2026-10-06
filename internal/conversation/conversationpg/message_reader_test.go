package conversationpg_test

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/tkakkie/ribbitto/internal/conversation"
	"github.com/tkakkie/ribbitto/internal/conversation/conversationpg"
	"github.com/tkakkie/ribbitto/internal/domain"
	"github.com/tkakkie/ribbitto/internal/infra/postgres/pgtest"
	"github.com/tkakkie/ribbitto/internal/org"
)

func TestReaderOne(t *testing.T) {
	t.Parallel()
	pool := pgtest.New(t)
	ctx := t.Context()
	local := pgtest.OrganizationWithOwner(t, pool, "acme", conversation.DefaultChannelName)
	foreign := pgtest.OrganizationWithOwner(t, pool, "globex", conversation.DefaultChannelName)
	otherChannel := pgtest.Channel(t, pool, local.OrganizationID, "other", false)
	membership := org.Membership{Organization: org.Organization{ID: local.OrganizationID}, Member: org.Member{ID: local.MemberID}}
	foreignMembership := org.Membership{Organization: org.Organization{ID: foreign.OrganizationID}, Member: org.Member{ID: foreign.MemberID}}
	service := conversationpg.NewPosting(pool, eventSequence, appendEvents, nil)
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
	reader := conversationpg.NewReader(pool, lookupMembers, lookupAccounts, eventCursor)
	for _, tt := range []struct {
		name       string
		membership org.Membership
		channel    domain.ID
		seq        int64
		want       conversation.Entry
		wantErr    error
	}{
		{"hydrated", membership, local.Channel.ID, posted.EventSeq, conversation.Entry{Message: posted, DisplayName: "Current Name", Handle: "current-handle", DefaultTopic: true}, nil},
		{"missing", membership, local.Channel.ID, posted.EventSeq + 100, conversation.Entry{}, conversation.ErrMessageNotFound},
		{"wrong channel", membership, otherChannel.ID, posted.EventSeq, conversation.Entry{}, conversation.ErrMessageNotFound},
		{"foreign message", membership, foreign.Channel.ID, foreignPost.EventSeq, conversation.Entry{}, conversation.ErrMessageNotFound},
		{"foreign member", foreignMembership, local.Channel.ID, posted.EventSeq, conversation.Entry{}, conversation.ErrMessageNotFound},
		{"foreign hydrated", foreignMembership, foreign.Channel.ID, foreignPost.EventSeq, conversation.Entry{Message: foreignPost, DisplayName: "globex", Handle: "owner", DefaultTopic: true}, nil},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got, err := reader.One(ctx, tt.membership, tt.channel, tt.seq)
			if !errors.Is(err, tt.wantErr) || got != tt.want {
				t.Fatalf("entry = %+v, error = %v; want %+v, %v", got, err, tt.want, tt.wantErr)
			}
		})
	}
}

func TestReaderPaging(t *testing.T) {
	t.Parallel()
	pool := pgtest.New(t)
	ctx := t.Context()
	memberships := map[string]org.Membership{}
	defaults := map[string]conversation.Channel{}
	for _, slug := range []string{"acme", "globex"} {
		name := conversation.DefaultChannelName
		if slug == "acme" {
			name = "empty"
		}
		fixture := pgtest.OrganizationWithOwner(t, pool, slug, name)
		memberships[slug] = org.Membership{Organization: org.Organization{ID: fixture.OrganizationID, Slug: slug}, Member: org.Member{ID: fixture.MemberID, OrganizationID: fixture.OrganizationID}}
		defaults[slug] = conversation.Channel(fixture.Channel)
	}
	acme, globex := memberships["acme"], memberships["globex"]
	channels := map[string]domain.ID{"empty": defaults["acme"].ID}
	wantChannels := []conversation.Channel{defaults["acme"]}
	for _, name := range []string{"exact", "partial", "noise"} {
		ch := pgtest.Channel(t, pool, acme.Organization.ID, name, false)
		channels[name] = ch.ID
		wantChannels = append(wantChannels, conversation.Channel(ch))
	}
	slices.SortFunc(wantChannels, func(a, b conversation.Channel) int { return strings.Compare(a.Name, b.Name) })
	foreign := defaults["globex"]

	// Interleave posts so that every channel's event_seq values have gaps
	// filled by another channel's, and globex reuses acme's numbers.
	service := conversationpg.NewPosting(pool, eventSequence, appendEvents, nil)
	sizes := map[string]int{"exact": 2 * conversation.PageSize, "partial": conversation.PageSize + 1}
	posted := map[string][]string{}
	var foreignSeqs []int64
	for i := range 2 * conversation.PageSize {
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

	for _, name := range []string{"exact", "partial"} {
		var topicID domain.ID
		requireNoError(t, pool.QueryRow(ctx, "INSERT INTO topic (organization_id, channel_id, name, is_default) VALUES ($1, $2, $3, false) RETURNING id", acme.Organization.ID, channels[name], name).Scan(&topicID))
		_, err := pool.Exec(ctx, "UPDATE message SET topic_id = $3 WHERE organization_id = $1 AND channel_id = $2 AND event_seq % 2 = 0", acme.Organization.ID, channels[name], topicID)
		requireNoError(t, err)
	}
	queries, topicQueries := 0, 0
	config := pool.Config()
	config.ConnConfig.Tracer = queryHook(func(_ context.Context, sql string) {
		if strings.HasPrefix(sql, "-- name:") {
			queries++
		}
		if strings.HasPrefix(sql, "-- name: LookupTopics ") {
			topicQueries++
		}
	})
	reading, err := pgxpool.NewWithConfig(ctx, config)
	requireNoError(t, err)
	t.Cleanup(reading.Close)
	reader := conversationpg.NewReader(reading, lookupMembers, lookupAccounts, eventCursor)
	for _, name := range []string{"empty", "exact", "partial"} {
		t.Run(name, func(t *testing.T) {
			var got []string
			var before *int64
			pages := 0
			for {
				queries, topicQueries = 0, 0
				page, err := reader.Page(ctx, acme, channels[name], nil, before)
				if err != nil {
					t.Fatal(err)
				}
				if !slices.Equal(page.Channels, wantChannels) {
					t.Fatalf("sidebar channels = %+v, want only acme's channels %+v (globex has %+v)", page.Channels, wantChannels, foreign)
				}
				if (page.EventCursor == nil) != (before != nil) {
					t.Fatalf("cursor presence disagrees with history bound: %+v", page)
				}
				wantQueries := 7 // including the sidebar's bounded topic list (#303)
				if before == nil {
					wantQueries++
				}
				if queries != wantQueries || topicQueries != 1 {
					t.Fatalf("page queries = %d (%d topic), want %d (1 topic)", queries, topicQueries, wantQueries)
				}
				pages++
				var bodies []string
				for _, e := range page.Entries {
					if e.ChannelID != channels[name] || e.OrganizationID != acme.Organization.ID || e.Handle != "owner" || e.DisplayName != "acme" {
						t.Fatalf("entry from the wrong scope or without its author: %+v", e)
					}
					if e.EventSeq%2 == 0 {
						if e.TopicName != name || e.DefaultTopic {
							t.Fatalf("named label: %+v", e)
						}
					} else if !e.DefaultTopic || e.TopicName != "" {
						t.Fatalf("default label: %+v", e)
					}
					bodies = append(bodies, e.Body)
				}
				got = append(bodies, got...)
				if !page.Older {
					break
				}
				if len(page.Entries) != conversation.PageSize || pages > 10 {
					t.Fatalf("page %d has %d entries and claims older ones", pages, len(page.Entries))
				}
				before = &page.Entries[0].EventSeq
			}
			if want := (sizes[name] + conversation.PageSize - 1) / conversation.PageSize; pages != max(want, 1) {
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
	noise, err := reader.Page(ctx, acme, channels["noise"], nil, nil)
	requireNoError(t, err)
	for _, before := range []int64{noise.Entries[len(noise.Entries)-1].EventSeq, foreignSeqs[len(foreignSeqs)-1]} {
		page, err := reader.Page(ctx, acme, channels["exact"], nil, &before)
		if err != nil || len(page.Entries) == 0 {
			t.Fatalf("before=%d: %d entries, %v", before, len(page.Entries), err)
		}
		for _, e := range page.Entries {
			if e.ChannelID != channels["exact"] || e.EventSeq >= before {
				t.Fatalf("before=%d returned %+v", before, e.Message)
			}
		}
	}
	if page, err := reader.Page(ctx, globex, channels["exact"], nil, nil); !errors.Is(err, conversation.ErrChannelNotFound) || len(page.Entries) != 0 || page.Older {
		t.Fatalf("globex read acme's channel: %+v, %v", page, err)
	}
}

// queryHook interrupts a real read before a selected statement, without adding
// test hooks to the production adapter or relying on scheduler timing.
type queryHook func(context.Context, string)

func (hook queryHook) TraceQueryStart(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	hook(ctx, data.SQL)
	return ctx
}
func (queryHook) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}

func TestChannelPageSnapshot(t *testing.T) {
	t.Parallel()
	for _, statement := range []string{"GetChannel", "ListChannels", "ListMessagesBefore", "LookupMembers", "LookupDisplayNames", "LookupTopics", "GetEventSeq"} {
		t.Run(statement, func(t *testing.T) {
			pool := pgtest.New(t)
			ctx := t.Context()
			fixture := pgtest.OrganizationWithOwner(t, pool, "acme", "general")
			m := org.Membership{Organization: org.Organization{ID: fixture.OrganizationID}, Member: org.Member{ID: fixture.MemberID}}
			posting := conversationpg.NewPosting(pool, eventSequence, appendEvents, nil)
			initial, err := posting.Post(ctx, m, fixture.Channel.ID, "initial")
			requireNoError(t, err)
			var concurrent conversation.Message
			var began bool
			config := pool.Config()
			config.ConnConfig.Tracer = queryHook(func(ctx context.Context, sql string) {
				if sql == "begin isolation level repeatable read read only" {
					began = true
				}
				if !strings.HasPrefix(sql, "-- name: "+statement+" ") || concurrent.EventSeq != 0 {
					return
				}
				// The writer uses another connection and commits before this
				// snapshot's next statement, including its final cursor read.
				concurrent, err = posting.Post(ctx, m, fixture.Channel.ID, "concurrent")
				requireNoError(t, err)
				_, err = pool.Exec(ctx, "UPDATE channel SET name = 'renamed' WHERE organization_id = $1 AND id = $2", fixture.OrganizationID, fixture.Channel.ID)
				requireNoError(t, err)
				_, err = pool.Exec(ctx, "UPDATE member SET handle = 'renamed' WHERE organization_id = $1 AND id = $2", fixture.OrganizationID, fixture.MemberID)
				requireNoError(t, err)
				_, err = pool.Exec(ctx, "UPDATE account SET display_name = 'renamed' WHERE id = $1", fixture.AccountID)
				requireNoError(t, err)
			})
			reading, err := pgxpool.NewWithConfig(ctx, config)
			requireNoError(t, err)
			t.Cleanup(reading.Close)
			page, err := (conversationpg.NewReader(reading, lookupMembers, lookupAccounts, eventCursor)).Page(ctx, m, fixture.Channel.ID, nil, nil)
			requireNoError(t, err)
			if !began || concurrent.EventSeq == 0 || page.EventCursor == nil {
				t.Fatalf("missing transaction, concurrent commit or cursor: %+v", page)
			}
			visible := slices.ContainsFunc(page.Entries, func(e conversation.Entry) bool { return e.ID == concurrent.ID })
			if !visible && concurrent.EventSeq <= *page.EventCursor {
				t.Fatal("concurrent message is neither on the page nor after its cursor")
			}
			cursor, count, name, handle, display := initial.EventSeq, 1, "general", "owner", "acme"
			if statement == "GetChannel" {
				cursor, count, name, handle, display = concurrent.EventSeq, 2, "renamed", "renamed", "renamed"
			}
			if *page.EventCursor != cursor || len(page.Entries) != count || page.Current.Name != name || len(page.Channels) != 1 || page.Channels[0].Name != name {
				t.Fatalf("page mixed snapshots: %+v, cursor %d", page, *page.EventCursor)
			}
			for _, e := range page.Entries {
				if e.Handle != handle || e.DisplayName != display {
					t.Fatalf("author mixed snapshots: %+v", e)
				}
			}
		})
	}
}
