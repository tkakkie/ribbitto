package web

import (
	"bytes"
	"context"
	"errors"
	"io"
	"reflect"
	"slices"
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
	_, through, err := postgres.NewBranchStore(pool).Branch(ctx, f.OrganizationID, f.Channel.ID, f.MemberID,
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
	sub.Account = foreign.AccountID
	denied := &moveDeliveries{}
	cursor, err = stream.Run(ctx, sub, 1, denied)
	if cursor != through || !errors.Is(err, io.EOF) || len(denied.events) != 0 {
		t.Fatalf("denied replay: %d, %v, %+v", cursor, err, denied.events)
	}
}
