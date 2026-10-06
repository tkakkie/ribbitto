package postgres_test

import (
	"errors"
	"math"
	"reflect"
	"testing"

	"github.com/tkakkie/ribbitto/internal/conversation"
	"github.com/tkakkie/ribbitto/internal/conversation/conversationtest"
	"github.com/tkakkie/ribbitto/internal/conversation/internal/postgres"
	"github.com/tkakkie/ribbitto/internal/kernel"
	platform "github.com/tkakkie/ribbitto/internal/platform/postgres"
	"github.com/tkakkie/ribbitto/internal/platform/postgres/pgtest"
	"github.com/tkakkie/ribbitto/internal/platform/postgres/pgxbridge"
)

var readStoreIn conversation.ReadStoreIn = func(snapshot platform.Snapshot) conversation.ReadStore { return postgres.ReadStoreIn(snapshot) }

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
// channel, newest first and within the limit.
func TestReadStoreIn(t *testing.T) {
	t.Parallel()
	pool := pgtest.New(t)
	ctx, f := t.Context(), newFixtures(t, pool)
	// acme's general also has a named topic, and acme a second channel.
	randomChannel := conversationtest.Channel(t, pool, f.acme, "random", false)
	random, randomTopic := randomChannel.ID, randomChannel.DefaultTopicID
	beta := conversationtest.Topic(t, pool, f.acme, f.general, "beta").ID
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
	err := platform.InSnapshot(ctx, pool, func(snapshot platform.Snapshot) error {
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
			name           string
			organizationID kernel.ID
			ids            []kernel.ID
			want           []kernel.ID
		}{
			{"newest first, in scope only", f.acme, []kernel.ID{m[1], m[4], m[5], m[3], {0xee}}, []kernel.ID{m[3], m[1]}},
			{"read as another organisation", f.globex, []kernel.ID{m[1]}, []kernel.ID{}},
			{"missing IDs", f.acme, []kernel.ID{{0xee}}, []kernel.ID{}},
			{"empty input", f.acme, nil, []kernel.ID{}},
		} {
			batch, err := store.GetMessages(ctx, tc.organizationID, f.general, tc.ids)
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

// The channel and topic reads bind to the runner's snapshot. Each case
// differs from an in-scope read in one scope only (organisation or channel),
// so each predicate is checked on its own.
func TestReadStoreChannelsAndTopics(t *testing.T) {
	t.Parallel()
	pool := pgtest.New(t)
	ctx, f := t.Context(), newFixtures(t, pool)
	// acme's channels are created out of name order, and general's named
	// topics out of name order and with mixed case.
	randomChannel := conversationtest.Channel(t, pool, f.acme, "random", false)
	alphaChannel := conversationtest.Channel(t, pool, f.acme, "alpha", false)
	random, randomTopic, alpha := randomChannel.ID, randomChannel.DefaultTopicID, alphaChannel.ID
	var beta, gamma, alphaNamed kernel.ID
	for _, topic := range []struct {
		name string
		id   *kernel.ID
	}{{"beta", &beta}, {"Gamma", &gamma}, {"Alpha", &alphaNamed}} {
		*topic.id = conversationtest.Topic(t, pool, f.acme, f.general, topic.name).ID
	}
	err := platform.InSnapshot(ctx, pool, func(snapshot platform.Snapshot) error {
		store := readStoreIn(snapshot)
		ids := func(read string, got []kernel.ID, err error, want ...kernel.ID) {
			t.Helper()
			if want == nil {
				want = []kernel.ID{}
			}
			if err != nil || !reflect.DeepEqual(got, want) {
				t.Errorf("%s = %x, %v; want %x", read, got, err, want)
			}
		}
		channels, err := store.ListChannels(ctx, f.acme)
		ids("ListChannels", channelIDs(channels), err, alpha, f.general, random)
		for _, tc := range []struct {
			name                      string
			organizationID, channelID kernel.ID
			limit                     int
			want                      []kernel.ID
		}{
			{"default first, then by name ignoring case", f.acme, f.general, 50, []kernel.ID{f.generalTopic, alphaNamed, beta, gamma}},
			{"limit", f.acme, f.general, 2, []kernel.ID{f.generalTopic, alphaNamed}},
			{"another channel", f.acme, random, 50, []kernel.ID{randomTopic}},
			{"read as another organisation", f.globex, f.general, 50, nil},
		} {
			topics, err := store.ListTopics(ctx, tc.organizationID, tc.channelID, tc.limit)
			ids("ListTopics, "+tc.name, topicIDs(topics), err, tc.want...)
		}
		for _, limit := range []int{0, -1, math.MaxInt32 + 1} {
			if topics, err := store.ListTopics(ctx, f.acme, f.general, limit); err == nil {
				t.Errorf("ListTopics(limit %d) = %+v, want an error", limit, topics)
			}
		}
		for _, tc := range []struct {
			name                      string
			organizationID, channelID kernel.ID
			ids                       []kernel.ID
			want                      []kernel.ID
		}{
			{"in scope only", f.acme, f.general, []kernel.ID{beta, f.generalTopic, randomTopic, f.foreignTopic, {0xee}}, []kernel.ID{f.generalTopic, beta}},
			{"topic of another channel", f.acme, f.general, []kernel.ID{randomTopic}, nil},
			{"read as another organisation", f.globex, f.general, []kernel.ID{beta}, nil},
			{"missing IDs", f.acme, f.general, []kernel.ID{{0xee}}, nil},
			{"empty input", f.acme, f.general, nil, nil},
		} {
			found, err := store.LookupTopics(ctx, tc.organizationID, tc.channelID, tc.ids)
			// The map's keys, in a fixed order, each holding its own topic.
			got := []kernel.ID{}
			for _, id := range []kernel.ID{f.generalTopic, beta, randomTopic, f.foreignTopic} {
				if topic, ok := found[id]; ok && topic.ID == id {
					got = append(got, id)
				}
			}
			if len(found) != len(got) {
				t.Errorf("LookupTopics, %s returned unrequested topics: %+v", tc.name, found)
			}
			ids("LookupTopics, "+tc.name, got, err, tc.want...)
		}
		if got, err := store.GetChannel(ctx, f.acme, f.general); err != nil || got.ID != f.general || got.DefaultTopicID != f.generalTopic {
			t.Errorf("GetChannel = %+v, %v; want general", got, err)
		}
		if got, err := store.GetTopic(ctx, f.acme, f.general, beta); err != nil || got.ID != beta || got.Name != "beta" || got.ChannelID != f.general {
			t.Errorf("GetTopic = %+v, %v; want beta", got, err)
		}
		for _, tc := range []struct {
			name string
			read func() error
			want error
		}{
			{"channel read as another organisation", func() error { _, err := store.GetChannel(ctx, f.globex, f.general); return err }, conversation.ErrChannelNotFound},
			{"topic of another channel", func() error { _, err := store.GetTopic(ctx, f.acme, random, beta); return err }, conversation.ErrTopicNotFound},
			{"topic read as another organisation", func() error { _, err := store.GetTopic(ctx, f.globex, f.general, beta); return err }, conversation.ErrTopicNotFound},
		} {
			if err := tc.read(); !errors.Is(err, tc.want) {
				t.Errorf("%s: %v, want %v", tc.name, err, tc.want)
			}
		}
		return nil
	})
	requireNoError(t, err)
}

func channelIDs(channels []conversation.Channel) []kernel.ID {
	ids := []kernel.ID{}
	for _, c := range channels {
		ids = append(ids, c.ID)
	}
	return ids
}

func topicIDs(topics []conversation.Topic) []kernel.ID {
	ids := []kernel.ID{}
	for _, topic := range topics {
		ids = append(ids, topic.ID)
	}
	return ids
}
