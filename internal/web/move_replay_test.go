package web

import (
	"bytes"
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"reflect"
	"slices"
	"strconv"
	"testing"

	"github.com/tkakkie/ribbitto/internal/app/authz"
	"github.com/tkakkie/ribbitto/internal/app/message"
	"github.com/tkakkie/ribbitto/internal/app/topic"
	"github.com/tkakkie/ribbitto/internal/domain"
	"github.com/tkakkie/ribbitto/internal/infra/postgres"
	"github.com/tkakkie/ribbitto/internal/infra/postgres/pgtest"
	"github.com/tkakkie/ribbitto/internal/realtime"
	"github.com/tkakkie/ribbitto/internal/web/view"
	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

type finiteMoveLog struct {
	realtime.EventReader
	through int64
}

func (r finiteMoveLog) EventsAfter(ctx context.Context, org domain.ID, after int64, limit int) ([]domain.Event, error) {
	if after == r.through {
		return nil, io.EOF
	}
	return r.EventReader.EventsAfter(ctx, org, after, limit)
}

type moveDeliveries struct{ events []realtime.Outgoing }

func (s *moveDeliveries) Send(_ context.Context, out realtime.Outgoing) error {
	s.events = append(s.events, out)
	return nil
}
func (*moveDeliveries) Heartbeat(context.Context) error { return nil }

// Apply the feed's stable-ID replacement contract to server-rendered items.
// Ignore announcement metadata, which belongs only to live posting payloads.
func applyFeed(t *testing.T, feed []string, out realtime.Outgoing) []string {
	t.Helper()
	doc, err := html.Parse(bytes.NewReader(out.Data))
	if err != nil {
		t.Fatal(err)
	}
	for item := range doc.Descendants() {
		if item.DataAtom != atom.Li {
			continue
		}
		id := attr(item, "id")
		item.Attr = slices.DeleteFunc(item.Attr, func(a html.Attribute) bool { return a.Key == "data-announcement" })
		var rendered bytes.Buffer
		if err := html.Render(&rendered, item); err != nil {
			t.Fatal(err)
		}
		index := slices.IndexFunc(feed, func(s string) bool {
			doc, err := html.Parse(bytes.NewBufferString(s))
			if err != nil {
				t.Fatal(err)
			}
			return attr(find(doc, atom.Li), "id") == id
		})
		if index >= 0 {
			feed[index] = rendered.String()
		} else if out.Name != "messages-moved" {
			feed = append(feed, rendered.String())
		}
	}
	return feed
}

// Model topic DOM swaps over the actual payload, preserving full item markup.
func applyTopic(t *testing.T, items []string, out realtime.Outgoing, selected domain.ID, oldest int64) []string {
	t.Helper()
	doc, err := html.Parse(bytes.NewReader(out.Data))
	if err != nil {
		t.Fatal(err)
	}
	for node := range doc.Descendants() {
		if node.DataAtom != atom.Li {
			continue
		}
		id := attr(node, "id")
		if out.Name == "messages-moved" && attr(find(doc, atom.Ul), "data-from-topic") == fmt.Sprintf("%x-%x-%x-%x-%x", selected[:4], selected[4:6], selected[6:8], selected[8:10], selected[10:]) {
			items = slices.DeleteFunc(items, func(s string) bool { return itemAttribute(t, s, "id") == id })
			continue
		}
		seq, err := strconv.ParseInt(attr(node, "data-event-seq"), 10, 64)
		if err != nil {
			t.Fatal(err)
		}
		if seq >= oldest {
			var rendered bytes.Buffer
			if err := html.Render(&rendered, node); err != nil {
				t.Fatal(err)
			}
			items = applyFeed(t, items, realtime.Outgoing{Name: "message", Data: rendered.Bytes()})
		}
	}
	slices.SortFunc(items, func(a, b string) int {
		seqA, _ := strconv.ParseInt(itemAttribute(t, a, "data-event-seq"), 10, 64)
		seqB, _ := strconv.ParseInt(itemAttribute(t, b, "data-event-seq"), 10, 64)
		return cmp.Compare(seqA, seqB)
	})
	return items
}

// Model the stream script's request-scoped retention and post-history replay.
type topicMovePage struct {
	selected domain.ID
	oldest   int64
	items    []string
	loading  bool
	moves    []realtime.Outgoing
}

func (p *topicMovePage) deliver(t *testing.T, out realtime.Outgoing) {
	t.Helper()
	if p.loading && out.Name == "messages-moved" {
		p.moves = append(p.moves, out)
	}
	p.items = applyTopic(t, p.items, out, p.selected, p.oldest)
}

func (p *topicMovePage) loadOlder(t *testing.T, selected domain.Topic, older message.ChannelPage) {
	t.Helper()
	p.items = append(renderedPage(t, older.Entries), p.items...)
	p.oldest = topicPageBoundary(t, selected, older, p.oldest)
	for _, out := range p.moves {
		p.items = applyTopic(t, p.items, out, p.selected, p.oldest)
	}
	p.loading = false
	p.moves = nil
}

func itemAttribute(t *testing.T, item, key string) string {
	t.Helper()
	doc, err := html.Parse(bytes.NewBufferString(item))
	if err != nil {
		t.Fatal(err)
	}
	return attr(find(doc, atom.Li), key)
}

func renderedPage(t *testing.T, entries []message.Entry) []string {
	t.Helper()
	var items []string
	for _, entry := range entries {
		var rendered bytes.Buffer
		if err := view.MessageItem(viewMessage("acme", entry)).Render(t.Context(), &rendered); err != nil {
			t.Fatal(err)
		}
		items = applyFeed(t, items, realtime.Outgoing{Name: "message", Data: rendered.Bytes()})
	}
	return items
}

func TestMoveReplayCorrectsWarmPostingRender(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	pool := pgtest.New(t)
	f := pgtest.OrganizationWithOwner(t, pool, "acme", "general")
	m := authz.Membership{Organization: domain.Organization{ID: f.OrganizationID, Slug: "acme"}, Member: domain.Member{ID: f.MemberID}}
	reader := postgres.MessageReader{Pool: pool}
	renderer := messageRenderer{messages: reader, membership: m, renders: newRenderCache(ctx)}
	log := postgres.NewEventReader(pool)
	var ids []domain.ID
	var source domain.ID
	for range 2 {
		posted, err := postgres.NewPostingStore(pool).Post(ctx, f.OrganizationID, f.Channel.ID, f.MemberID, "selected body")
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, posted.ID)
		source = posted.TopicID
	}
	events, err := log.EventsAfter(ctx, f.OrganizationID, 1, 100)
	if err != nil {
		t.Fatal(err)
	}
	var warm []realtime.Outgoing
	for _, event := range events {
		out, err := renderer.Render(ctx, realtime.Subscription{}, event)
		if err != nil {
			t.Fatal(err)
		}
		warm = append(warm, out)
	}
	destination, through, err := postgres.NewBranchStore(pool).Branch(ctx, f.OrganizationID, f.Channel.ID, f.MemberID,
		topic.Branch{From: source, Messages: ids, NewName: "New label"}, func(domain.Topic) string { return "branch notice" })
	if err != nil {
		t.Fatal(err)
	}
	stream := realtime.Stream{Hub: realtime.NewHub(), Events: finiteMoveLog{log, through}, Renderer: renderer, Authorizer: authz.New(postgres.NewAuthzStore(pool)), BatchSize: 1}
	sub := realtime.Subscription{Organization: f.OrganizationID, OrganizationSlug: "acme", Account: f.AccountID, Channel: f.Channel.ID}
	delivered := &moveDeliveries{}
	cursor, err := stream.Run(ctx, sub, 1, delivered)
	if cursor != through || !errors.Is(err, io.EOF) || len(delivered.events) != 4 {
		t.Fatalf("replay: cursor %d, events %d, error %v", cursor, len(delivered.events), err)
	}
	if !bytes.Equal(delivered.events[0].Data, warm[0].Data) || !bytes.Equal(delivered.events[1].Data, warm[1].Data) {
		t.Fatal("posting cache was not warm")
	}
	if delivered.events[2].Name != "messages-moved" || delivered.events[3].Name != "message" {
		t.Fatal("move must precede notice")
	}
	page, err := reader.Page(ctx, m, f.Channel.ID, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	var want []string
	for _, entry := range page.Entries {
		var rendered bytes.Buffer
		if err := view.MessageItem(viewMessage("acme", entry)).Render(ctx, &rendered); err != nil {
			t.Fatal(err)
		}
		want = applyFeed(t, want, realtime.Outgoing{Name: "message", Data: rendered.Bytes()})
	}
	var feed []string
	for range 2 { // A duplicate replay must remain identical, including checkbox sources.
		for _, out := range delivered.events {
			feed = applyFeed(t, feed, out)
		}
		if !reflect.DeepEqual(feed, want) {
			t.Fatal("replay differs from reload", feed, want)
		}
	}
	if got := applyFeed(t, nil, delivered.events[2]); len(got) != 0 {
		t.Fatal("move inserted unloaded history")
	}
	// A different organisation's member receives neither correction nor notice,
	// even though this connection hits renders warmed by the authorized member.
	foreign := pgtest.OrganizationWithOwner(t, pool, "globex", "general")
	for _, tt := range []struct {
		name         string
		org, channel domain.ID
		ids          []domain.ID
	}{
		{"foreign organisation", foreign.OrganizationID, f.Channel.ID, ids},
		{"foreign channel", f.OrganizationID, foreign.Channel.ID, ids},
		{"incomplete batch", f.OrganizationID, f.Channel.ID, append(slices.Clone(ids), domain.ID{})},
	} {
		t.Run(tt.name, func(t *testing.T) {
			membership := m
			membership.Organization.ID = tt.org
			entries, err := reader.Many(ctx, membership, tt.channel, tt.ids)
			if !errors.Is(err, message.ErrNotFound) || len(entries) != 0 {
				t.Fatalf("batch escaped scope or returned partial data: %+v, %v", entries, err)
			}
		})
	}
	for _, selected := range []domain.ID{source, destination.ID} {
		sub.Topic = &selected
		got := &moveDeliveries{}
		cursor, err := stream.Run(ctx, sub, 1, got)
		if cursor != through || !errors.Is(err, io.EOF) {
			t.Fatalf("topic replay: %d, %v", cursor, err)
		}
		page, err := reader.Page(ctx, m, f.Channel.ID, &selected, nil)
		if err != nil {
			t.Fatal(err)
		}
		var items []string
		for range 2 {
			for _, out := range got.events {
				items = applyTopic(t, items, out, selected, 0)
			}
			if !reflect.DeepEqual(items, renderedPage(t, page.Entries)) {
				t.Fatal("topic replay differs from reload")
			}
		}
	}
	// Denial is checked on topic streams even when all their renders are warm.
	sub.Account = foreign.AccountID
	for _, selected := range []*domain.ID{nil, &source, &destination.ID} {
		sub.Topic = selected
		denied := &moveDeliveries{}
		cursor, err = stream.Run(ctx, sub, 1, denied)
		if cursor != through || !errors.Is(err, io.EOF) || len(denied.events) != 0 {
			t.Fatalf("denied replay: %d, %v, %+v", cursor, err, denied.events)
		}
	}
}

func topicPageBoundary(t *testing.T, destination domain.Topic, page message.ChannelPage, before int64) int64 {
	t.Helper()
	model := view.ChannelPage{Organization: domain.Organization{Name: "Acme", Slug: "acme"}, Topic: &destination, Older: page.Older, Before: before}
	for _, entry := range page.Entries {
		model.Messages = append(model.Messages, viewMessage("acme", entry))
	}
	var markup bytes.Buffer
	if err := view.Channel("", model).Render(t.Context(), &markup); err != nil {
		t.Fatal(err)
	}
	doc, err := html.Parse(&markup)
	if err != nil {
		t.Fatal(err)
	}
	controls, err := htmxMatches(doc, "#load-older")
	if err != nil || len(controls) != 1 {
		t.Fatalf("history control: %v, %v", controls, err)
	}
	boundary, err := strconv.ParseInt(attr(controls[0], "data-oldest-seq"), 10, 64)
	if err != nil {
		t.Fatal(err)
	}
	if attr(find(doc, atom.Ol), "data-topic") == "" {
		t.Fatal("topic page omitted its move routing")
	}
	return boundary
}

func TestOlderMoveThenLoadOlder(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name  string
		count int
		older bool
	}{
		{"empty", 3, false},
		{"fully loaded", 6, false},
		{"partially loaded", message.PageSize + 4, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			ctx := t.Context()
			pool := pgtest.New(t)
			f := pgtest.OrganizationWithOwner(t, pool, "acme", "general")
			m := authz.Membership{Organization: domain.Organization{ID: f.OrganizationID, Slug: "acme"}, Member: domain.Member{ID: f.MemberID}}
			destination, err := postgres.NewTopicStore(pool).CreateTopic(ctx, f.OrganizationID, f.Channel.ID, "Destination")
			if err != nil {
				t.Fatal(err)
			}
			reader := postgres.MessageReader{Pool: pool}
			var moved []domain.ID
			var source domain.ID
			for i := range tt.count {
				selected := &destination.ID
				if i == 0 || i == tt.count/2 || i == tt.count-1 {
					selected = nil // Before, within and after the destination's messages.
				}
				posted, err := postgres.NewPostingStore(pool).PostToTopic(ctx, f.OrganizationID, f.Channel.ID, f.MemberID, selected, "body")
				if err != nil {
					t.Fatal(err)
				}
				if selected == nil {
					moved = append(moved, posted.ID)
					source = posted.TopicID
				}
			}
			page, err := reader.Page(ctx, m, f.Channel.ID, &destination.ID, nil)
			if err != nil || page.Older != tt.older {
				t.Fatalf("destination: %+v, %v", page, err)
			}
			boundary := topicPageBoundary(t, destination, page, 0)
			items := renderedPage(t, page.Entries)
			_, through, err := postgres.NewBranchStore(pool).Branch(ctx, f.OrganizationID, f.Channel.ID, f.MemberID,
				topic.Branch{From: source, To: &destination.ID, Messages: moved}, func(domain.Topic) string { return "notice" })
			if err != nil {
				t.Fatal(err)
			}
			renderer := messageRenderer{messages: reader, membership: m, renders: newRenderCache(ctx)}
			stream := realtime.Stream{Hub: realtime.NewHub(), Events: finiteMoveLog{postgres.NewEventReader(pool), through}, Renderer: renderer, Authorizer: authz.New(postgres.NewAuthzStore(pool)), BatchSize: 1}
			delivered := &moveDeliveries{}
			_, err = stream.Run(ctx, realtime.Subscription{Organization: f.OrganizationID, OrganizationSlug: "acme", Account: f.AccountID, Channel: f.Channel.ID, Topic: &destination.ID}, *page.EventCursor, delivered)
			if !errors.Is(err, io.EOF) || len(delivered.events) != 1 {
				t.Fatalf("destination move: %v, %+v", err, delivered.events)
			}
			for range 2 {
				items = applyTopic(t, items, delivered.events[0], destination.ID, boundary)
			}
			if tt.older {
				if len(items) != message.PageSize+2 {
					t.Fatal("move must insert only the two items within the loaded range")
				}
				older, err := reader.Page(ctx, m, f.Channel.ID, &destination.ID, &boundary)
				if err != nil || older.Older {
					t.Fatalf("older destination: %+v, %v", older, err)
				}
				items = append(renderedPage(t, older.Entries), items...)
				// Load older replaces the control out of band, admitting all history.
				boundary = topicPageBoundary(t, destination, older, boundary)
				if boundary != 0 {
					t.Fatalf("exhausted history boundary = %d, want 0", boundary)
				}
				items = applyTopic(t, items, delivered.events[0], destination.ID, boundary)
			}
			feed, err := reader.Page(ctx, m, f.Channel.ID, nil, &through)
			if err != nil {
				t.Fatal(err)
			}
			before := feed.Entries[0].EventSeq
			earliest, err := reader.Page(ctx, m, f.Channel.ID, nil, &before)
			if err != nil {
				t.Fatal(err)
			}
			want := renderedPage(t, append(earliest.Entries, feed.Entries...))
			if !reflect.DeepEqual(items, want) {
				t.Fatal("move and loaded history differ from reload")
			}
		})
	}
}

func TestMoveCrossesLoadOlder(t *testing.T) {
	t.Parallel()
	for _, count := range []int{message.PageSize + 4, 2*message.PageSize + 4} {
		for _, stale := range []bool{true, false} {
			t.Run(fmt.Sprintf("count=%d/stale=%t", count, stale), func(t *testing.T) {
				ctx := t.Context()
				pool := pgtest.New(t)
				f := pgtest.OrganizationWithOwner(t, pool, "acme", "general")
				m := authz.Membership{Organization: domain.Organization{ID: f.OrganizationID, Slug: "acme"}, Member: domain.Member{ID: f.MemberID}}
				destination, err := postgres.NewTopicStore(pool).CreateTopic(ctx, f.OrganizationID, f.Channel.ID, "Destination")
				if err != nil {
					t.Fatal(err)
				}
				reader := postgres.MessageReader{Pool: pool}
				var source domain.Topic
				var moved []domain.ID
				for i := range count {
					for _, selected := range []*domain.ID{nil, &destination.ID} {
						posted, err := postgres.NewPostingStore(pool).PostToTopic(ctx, f.OrganizationID, f.Channel.ID, f.MemberID, selected, "body")
						if err != nil {
							t.Fatal(err)
						}
						if selected == nil {
							source.ID = posted.TopicID
							// One below the next page, one in it, one already loaded.
							if i == 0 || i == count-message.PageSize-2 || i == count-1 {
								moved = append(moved, posted.ID)
							}
						}
					}
				}
				topics := []domain.Topic{source, destination}
				pages := make([]topicMovePage, len(topics))
				responses := make([]message.ChannelPage, len(topics))
				var cursor int64
				for i, selected := range topics {
					page, err := reader.Page(ctx, m, f.Channel.ID, &selected.ID, nil)
					if err != nil {
						t.Fatal(err)
					}
					cursor = *page.EventCursor
					pages[i] = topicMovePage{selected: selected.ID, oldest: topicPageBoundary(t, selected, page, 0), items: renderedPage(t, page.Entries), loading: true}
					if stale {
						responses[i], err = reader.Page(ctx, m, f.Channel.ID, &selected.ID, &pages[i].oldest)
						if err != nil {
							t.Fatal(err)
						}
					}
				}
				_, through, err := postgres.NewBranchStore(pool).Branch(ctx, f.OrganizationID, f.Channel.ID, f.MemberID,
					topic.Branch{From: source.ID, To: &destination.ID, Messages: moved}, func(domain.Topic) string { return "notice" })
				if err != nil {
					t.Fatal(err)
				}
				renderer := messageRenderer{messages: reader, membership: m, renders: newRenderCache(ctx)}
				stream := realtime.Stream{Hub: realtime.NewHub(), Events: finiteMoveLog{postgres.NewEventReader(pool), through}, Renderer: renderer, Authorizer: authz.New(postgres.NewAuthzStore(pool)), BatchSize: 1}
				for i, selected := range topics {
					t.Run([]string{"source", "destination"}[i], func(t *testing.T) {
						page := &pages[i]
						delivered := &moveDeliveries{}
						_, err := stream.Run(ctx, realtime.Subscription{Organization: f.OrganizationID, OrganizationSlug: "acme", Account: f.AccountID, Channel: f.Channel.ID, Topic: &selected.ID}, cursor, delivered)
						if !errors.Is(err, io.EOF) {
							t.Fatal(err)
						}
						for range 2 { // Reconnect duplicates must not duplicate history items.
							for _, out := range delivered.events {
								page.deliver(t, out)
							}
						}
						if len(page.moves) != 2 {
							t.Fatalf("retained %d moves, want 2", len(page.moves))
						}
						if !stale {
							responses[i], err = reader.Page(ctx, m, f.Channel.ID, &selected.ID, &page.oldest)
							if err != nil {
								t.Fatal(err)
							}
						}
						page.loadOlder(t, selected, responses[i])
						if len(page.moves) != 0 || page.loading {
							t.Fatal("completed history retained moves")
						}
						if (page.oldest == 0) != (count == message.PageSize+4) {
							t.Fatalf("unexpected history bound %d", page.oldest)
						}
						var entries []message.Entry
						var before *int64
						for {
							reloaded, err := reader.Page(ctx, m, f.Channel.ID, &selected.ID, before)
							if err != nil {
								t.Fatal(err)
							}
							entries = append(reloaded.Entries, entries...)
							if !reloaded.Older {
								break
							}
							before = &reloaded.Entries[0].EventSeq
						}
						entries = slices.DeleteFunc(entries, func(e message.Entry) bool { return e.EventSeq < page.oldest })
						if !reflect.DeepEqual(page.items, renderedPage(t, entries)) {
							t.Fatal("move crossing history differs from reload within the new bound")
						}
					})
				}
			})
		}
	}
}
