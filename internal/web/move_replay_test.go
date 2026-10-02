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

func TestOlderMoveThenLoadOlder(t *testing.T) {
	t.Parallel()
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
	for i := range message.PageSize + 4 {
		selected := &destination.ID
		if i == 0 || i == 25 || i == message.PageSize+3 {
			selected = nil // Below the boundary, inside it and above the newest item.
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
	if err != nil || !page.Older {
		t.Fatalf("partial destination: %+v, %v", page, err)
	}
	var markup bytes.Buffer
	if err := view.Channel("", view.ChannelPage{Topic: &destination, Messages: []view.Message{viewMessage("acme", page.Entries[0])}, Older: true}).Render(ctx, &markup); err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(markup.Bytes(), []byte(fmt.Sprintf(`data-oldest-seq="%d"`, page.Entries[0].EventSeq))) || !bytes.Contains(markup.Bytes(), []byte(`data-topic="`)) {
		t.Fatal("topic page omitted its move routing or history boundary")
	}
	boundary := page.Entries[0].EventSeq
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
	if len(items) != message.PageSize+2 {
		t.Fatal("move must insert only the two items within the loaded range")
	}
	older, err := reader.Page(ctx, m, f.Channel.ID, &destination.ID, &boundary)
	if err != nil || older.Older {
		t.Fatalf("older destination: %+v, %v", older, err)
	}
	items = append(renderedPage(t, older.Entries), items...)
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
		t.Fatal("move then Load older duplicated, lost or misordered history")
	}
}
