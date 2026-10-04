package postgres_test

import (
	"errors"
	"reflect"
	"testing"

	"github.com/tkakkie/ribbitto/internal/conversation"
	"github.com/tkakkie/ribbitto/internal/conversation/conversationpg"
	"github.com/tkakkie/ribbitto/internal/kernel"
	platform "github.com/tkakkie/ribbitto/internal/platform/postgres"
	"github.com/tkakkie/ribbitto/internal/platform/postgres/pgtest"
	"github.com/tkakkie/ribbitto/internal/platform/postgres/pgxbridge"
)

var readStoreIn conversation.ReadStoreIn = conversationpg.ReadStoreIn

// messageIDs lists a read's message IDs in order; never nil, so an empty
// read compares equal to an empty want.
func messageIDs(messages []conversation.Message) []kernel.ID {
	ids := []kernel.ID{}
	for _, m := range messages {
		ids = append(ids, m.ID)
	}
	return ids
}

// The runner's transaction is read-only and repeatable-read, and returns fn's
// error as is. The message reads bind to it, scoped to the organisation and
// channel, in the legacy queries' order and limits.
func TestReadStoreIn(t *testing.T) {
	t.Parallel()
	pool := pgtest.New(t)
	ctx, f := t.Context(), newFixtures(t, pool)
	// acme's general also has a named topic, and acme a second channel.
	var random, randomTopic, beta kernel.ID
	fixture(t, pool, channelSQL, []any{f.acme, "random"}, &random, &randomTopic)
	fixture(t, pool, "INSERT INTO topic (organization_id, channel_id, name) VALUES ($1, $2, 'beta') RETURNING id", []any{f.acme, f.general}, &beta)
	// m[seq] is the message at that event_seq: 1–3 in general, 4 in random,
	// 5 in globex's channel.
	var m [6]kernel.ID
	for i, row := range []struct{ organizationID, channelID, topicID, memberID kernel.ID }{
		{f.acme, f.general, f.generalTopic, f.alice}, {f.acme, f.general, beta, f.alice}, {f.acme, f.general, f.generalTopic, f.alice},
		{f.acme, random, randomTopic, f.alice}, {f.globex, f.foreign, f.foreignTopic, f.bob},
	} {
		fixture(t, pool, "INSERT INTO message (organization_id, channel_id, topic_id, member_id, body, event_seq) VALUES ($1, $2, $3, $4, 'hello', $5) RETURNING id",
			[]any{row.organizationID, row.channelID, row.topicID, row.memberID, i + 1}, &m[i+1])
	}
	before := func(seq int64) *int64 { return &seq }
	stop := errors.New("fn failed")
	err := conversationpg.NewSnapshotRunner(pool).InSnapshot(ctx, func(snapshot platform.Snapshot) error {
		var isolation, readOnly string
		requireNoError(t, pgxbridge.Snapshot(snapshot).QueryRow(ctx, "SELECT current_setting('transaction_isolation'), current_setting('transaction_read_only')").Scan(&isolation, &readOnly))
		if isolation != "repeatable read" || readOnly != "on" {
			t.Fatalf("snapshot is %q, read only %q; want repeatable read, on", isolation, readOnly)
		}
		store := readStoreIn(snapshot)
		for _, tc := range []struct {
			name                      string
			organizationID, channelID kernel.ID
			topicID                   *kernel.ID
			before                    *int64
			limit                     int32
			want                      []kernel.ID
		}{
			{"latest page, every topic", f.acme, f.general, nil, nil, 50, []kernel.ID{m[3], m[2], m[1]}},
			{"before is exclusive", f.acme, f.general, nil, before(3), 50, []kernel.ID{m[2], m[1]}},
			{"limit", f.acme, f.general, nil, nil, 2, []kernel.ID{m[3], m[2]}},
			{"named topic", f.acme, f.general, &beta, nil, 50, []kernel.ID{m[2]}},
			{"default topic", f.acme, f.general, &f.generalTopic, nil, 50, []kernel.ID{m[3], m[1]}},
			{"topic of another channel", f.acme, f.general, &randomTopic, nil, 50, []kernel.ID{}},
			{"another channel", f.acme, random, nil, nil, 50, []kernel.ID{m[4]}},
			{"another organisation's channel", f.acme, f.foreign, nil, nil, 50, []kernel.ID{}},
			{"read as another organisation", f.globex, f.general, nil, nil, 50, []kernel.ID{}},
		} {
			history, err := store.ListMessagesBefore(ctx, tc.organizationID, tc.channelID, tc.topicID, tc.before, tc.limit)
			if got := messageIDs(history); err != nil || !reflect.DeepEqual(got, tc.want) {
				t.Errorf("ListMessagesBefore, %s = %x, %v; want %x", tc.name, got, err, tc.want)
			}
		}
		for _, tc := range []struct {
			name string
			ids  []kernel.ID
			want []kernel.ID
		}{
			{"newest first, in scope only", []kernel.ID{m[1], m[4], m[5], m[3], {0xee}}, []kernel.ID{m[3], m[1]}},
			{"missing IDs", []kernel.ID{{0xee}}, []kernel.ID{}},
			{"empty input", nil, []kernel.ID{}},
		} {
			batch, err := store.GetMessages(ctx, f.acme, f.general, tc.ids)
			if got := messageIDs(batch); err != nil || !reflect.DeepEqual(got, tc.want) {
				t.Errorf("GetMessages, %s = %x, %v; want %x", tc.name, got, err, tc.want)
			}
		}
		got, err := store.GetMessage(ctx, f.acme, f.general, 2)
		if err != nil || got.ID != m[2] || got.OrganizationID != f.acme || got.ChannelID != f.general || got.TopicID != beta ||
			got.MemberID != f.alice || got.Body != "hello" || got.EventSeq != 2 || got.CreatedAt.IsZero() {
			t.Fatalf("GetMessage = %+v, %v; want message 2", got, err)
		}
		for _, tc := range []struct {
			name                      string
			organizationID, channelID kernel.ID
			eventSeq                  int64
		}{
			{"message of another channel", f.acme, f.general, 4},
			{"event_seq of another organisation", f.acme, f.general, 5},
			{"read as another organisation", f.globex, f.general, 2},
			{"missing event_seq", f.acme, f.general, 6},
		} {
			if got, err := store.GetMessage(ctx, tc.organizationID, tc.channelID, tc.eventSeq); !errors.Is(err, conversation.ErrMessageNotFound) {
				t.Errorf("GetMessage, %s = %+v, %v; want ErrMessageNotFound", tc.name, got, err)
			}
		}
		return stop
	})
	if err != stop {
		t.Fatalf("InSnapshot = %v, want fn's error as is", err)
	}
}
