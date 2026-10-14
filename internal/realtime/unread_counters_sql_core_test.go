package realtime_test

import (
	"fmt"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/tkakkie/ribbitto/internal/conversation"
	"github.com/tkakkie/ribbitto/internal/conversation/conversationpg"
	"github.com/tkakkie/ribbitto/internal/conversation/conversationtest"
	"github.com/tkakkie/ribbitto/internal/kernel"
	"github.com/tkakkie/ribbitto/internal/org"
	platform "github.com/tkakkie/ribbitto/internal/platform/postgres"
	"github.com/tkakkie/ribbitto/internal/unread"
	"github.com/tkakkie/ribbitto/internal/unread/unreadpg"
)

func TestUnreadCountersSQLCore(t *testing.T) {
	h := newCounterMatrix(t)
	owner, writer := h.members[0], h.join(t, "writer")
	var topic kernel.ID
	for i := 0; i < 50; i++ {
		topic = conversationtest.Topic(t, h.pool, owner.Organization.ID, h.channel, fmt.Sprintf("Topic%02d", i)).ID
	}
	foreign := conversationtest.OrganizationWithOwner(t, h.pool, "foreign", "general")
	outsider := org.Membership{Organization: org.Organization{ID: foreign.OrganizationID}, Member: org.Member{ID: foreign.MemberID, JoinedEventSeq: 1}}
	views := []counterView{{owner, h.channel, topic}, {writer, h.channel, topic}, {outsider, foreign.Channel.ID, foreign.Channel.DefaultTopicID}}
	counterSQLTables(t, h.pool)
	config := h.pool.Config()
	config.ConnConfig.Tracer = counterSQLAdapter{}
	pool, err := pgxpool.NewWithConfig(t.Context(), config)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	posting := conversationpg.NewPagePosting(pool, postingSequence, postingEvents, func(tx platform.Tx) conversation.PostReadWriter { return counterSQLPostReads{tx} }, nil)
	reading := unreadpg.NewReading(pool, counterFeedWriter(), counterTopicWriter(nil), nil)
	check := func(name string) {
		t.Helper()
		tx, err := h.pool.BeginTx(t.Context(), pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = tx.Rollback(t.Context()) }()
		for _, v := range views {
			truth := counterLoad(t, tx, v.member.Organization.ID)
			got := counterSQLModel(t, tx, v, truth)
			want, err := sidebarOracleIn(t.Context(), tx, v.member.Organization.ID, v.member.Member.ID, v.channel, &v.selected)
			if err != nil {
				t.Fatal(err)
			}
			if err := sidebarOracleCompare(got, want); err != nil {
				t.Fatalf("%s: SQL counters/fallback differ from oracle: %v", name, err)
			}
		}
	}
	post := func(name string, m org.Membership, channel kernel.ID, target *kernel.ID, cursor *int64) int64 {
		t.Helper()
		ctx, w := counterSQLContext(t.Context(), m, channel)
		var message conversation.Message
		var err error
		if cursor == nil {
			message, err = posting.PostToTopic(ctx, m, channel, target, "older page")
		} else {
			message, err = posting.PostFromPage(ctx, m, channel, target, "latest page", *cursor)
		}
		if err != nil || w.err != nil {
			t.Fatalf("source: %v; counters: %v", err, w.err)
		}
		check(name)
		return message.EventSeq
	}
	read := func(name string, m org.Membership, target *kernel.ID, cursor int64) {
		t.Helper()
		ctx, w := counterSQLContext(t.Context(), m, h.channel)
		scope := unread.Scope{OrganizationID: m.Organization.ID, ChannelID: h.channel, MemberID: m.Member.ID}
		var err error
		if target == nil {
			err = reading.Feed(ctx, scope, m.Member.JoinedEventSeq, cursor)
		} else {
			err = reading.Topic(ctx, unread.TopicScope{Scope: scope, TopicID: *target}, m.Member.JoinedEventSeq, cursor)
		}
		if err != nil || w.err != nil {
			t.Fatalf("source: %v; counters: %v", err, w.err)
		}
		check(name)
	}
	_, key := counterSQLContext(t.Context(), owner, h.channel)
	materialize := func(name string) {
		counterSQLMaterialize(t, h.pool, key)
		check(name)
		var n int
		if err := h.pool.QueryRow(t.Context(), `SELECT count(*) FROM counter_topic_read WHERE organization_id=$1 AND channel_id=$2 AND member_id=$3`, key.organization, key.channel, key.member).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != 51 {
			t.Fatal("materialisation omitted empty or unlisted topics")
		}
	}
	revision := func() int64 {
		var n int64
		if err := h.pool.QueryRow(t.Context(), `SELECT revision FROM counter_bundle WHERE organization_id=$1 AND channel_id=$2 AND member_id=$3`, key.organization, key.channel, key.member).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	check("empty-missing-bundles")
	post("foreign-sentinel", outsider, foreign.Channel.ID, nil, nil)
	materialize("empty-materialisation")
	var seq [6]int64
	for i := range seq {
		var target *kernel.ID
		if i%2 == 1 {
			target = &topic
		}
		seq[i] = post(fmt.Sprintf("fragment-%d", i), writer, h.channel, target, nil)
	}
	read("topic-fragments", owner, &topic, seq[3])
	r := revision()
	read("older-topic-overlap", owner, &topic, seq[1])
	if revision() != r {
		t.Fatal("no-op changed revision")
	}
	post("feed-cursor-post", owner, h.channel, nil, &seq[0])
	read("feed-overlap", owner, nil, seq[2])
	post("cursorless-topic-own-post", owner, h.channel, &topic, nil)
	h.floor(t, owner, topic, seq[3])
	post("topic-cursor-post", owner, h.channel, &topic, &seq[5])
	var latest int64
	for i := 0; i < 101; i++ {
		latest = post(fmt.Sprintf("cap-%d", i), writer, h.channel, nil, nil)
	}
	read("missing-bundle-topic-read", writer, &topic, latest)
	// Exhaustive materialisation must retain read counts above the display cap.
	_, writerKey := counterSQLContext(t.Context(), writer, h.channel)
	counterSQLMaterialize(t, h.pool, writerKey)
	check("uncapped-materialisation")
	read("cap-down-one", owner, nil, seq[4])
	read("all-base-read", owner, &h.base, latest)
	r = revision()
	read("floor-only", owner, &topic, latest)
	if revision() != r+1 {
		t.Fatal("floor-only write did not advance revision")
	}
	r = revision()
	read("topic-repeat", owner, &topic, latest)
	read("feed-repeat", owner, nil, latest)
	if revision() != r {
		t.Fatal("repeated reads changed revision")
	}
	if _, err := h.pool.Exec(t.Context(), `UPDATE counter_bundle SET valid=false,n=9999 WHERE organization_id=$1 AND channel_id=$2 AND member_id=$3`, key.organization, key.channel, key.member); err != nil {
		t.Fatal(err)
	}
	check("invalid-stale-bundle-fallback")
	post("post-while-invalid", owner, h.channel, nil, nil)
	newest := post("unread-while-invalid", writer, h.channel, nil, nil)
	read("read-while-invalid", owner, nil, newest)
	materialize("rebuild-invalid")
}
