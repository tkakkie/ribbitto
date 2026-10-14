package realtime_test

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/tkakkie/ribbitto/internal/conversation/conversationpg"
	"github.com/tkakkie/ribbitto/internal/conversation/conversationtest"
	"github.com/tkakkie/ribbitto/internal/identity/identitytest"
	"github.com/tkakkie/ribbitto/internal/kernel"
	"github.com/tkakkie/ribbitto/internal/org"
	"github.com/tkakkie/ribbitto/internal/org/orgtest"
	platform "github.com/tkakkie/ribbitto/internal/platform/postgres"
	"github.com/tkakkie/ribbitto/internal/platform/postgres/pgtest"
	"github.com/tkakkie/ribbitto/internal/unread"
	"github.com/tkakkie/ribbitto/internal/unread/unreadpg"
)

type sidebarOracleCount struct {
	raw, capped int64
	badge       string
}

func sidebarOracleDisplay(n int64) sidebarOracleCount {
	badge := ""
	if n >= 100 {
		badge = "99+"
	} else if n > 0 {
		badge = strconv.FormatInt(n, 10)
	}
	return sidebarOracleCount{n, min(n, 100), badge}
}

type sidebarOracleEntry struct {
	id    kernel.ID
	name  string
	count sidebarOracleCount
}

// Selection has zero or one entry, independent of whether it is in the list.
// These slices model identity/order/counts, not HTML or coverage generations.
type sidebarOracleModel struct {
	organization, member, current kernel.ID
	channels, topics, selection   []sidebarOracleEntry
}

func sidebarOracleCompare(got, want sidebarOracleModel) error {
	if got.organization != want.organization || got.member != want.member || got.current != want.current ||
		!slices.Equal(got.channels, want.channels) || !slices.Equal(got.topics, want.topics) || !slices.Equal(got.selection, want.selection) {
		return fmt.Errorf("sidebar mismatch: got %+v; want %+v", got, want)
	}
	return nil
}

// sidebarOracle reads persisted truth independently of Reader.Sidebar and the
// cost count factories. Floors and capped/gap-limited scans are optimizations;
// exhaustive range containment is the read rule even for moved messages.
// Callers gate writes or use sidebarOracleIn at their captured boundary.
func sidebarOracle(ctx context.Context, pool *pgxpool.Pool, organization, member, current kernel.ID, selected *kernel.ID) (sidebarOracleModel, error) {
	tx, err := pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return sidebarOracleModel{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	model, err := sidebarOracleIn(ctx, tx, organization, member, current, selected)
	if err != nil {
		return sidebarOracleModel{}, err
	}
	return model, tx.Commit(ctx)
}

func sidebarOracleIn(ctx context.Context, tx pgx.Tx, organization, member, current kernel.ID, selected *kernel.ID) (sidebarOracleModel, error) {
	model := sidebarOracleModel{organization: organization, member: member, current: current}
	var joined int64
	if err := tx.QueryRow(ctx, `SELECT joined_event_seq FROM member WHERE organization_id=$1 AND id=$2`, organization, member).Scan(&joined); err != nil {
		return model, err
	}
	rows, err := tx.Query(ctx, `
WITH unread_messages AS (
  SELECT m.channel_id, m.topic_id FROM message m
  WHERE m.organization_id=$1 AND m.event_seq > $4
    AND NOT EXISTS (SELECT 1 FROM read_range r
      WHERE r.organization_id=m.organization_id AND r.channel_id=m.channel_id
        AND r.member_id=$2 AND r.lo <= m.event_seq AND m.event_seq < r.hi)
), entries AS (
  SELECT 0 AS kind, c.id, c.name, false AS is_default,
    (SELECT count(*) FROM unread_messages u WHERE u.channel_id=c.id) AS unread
  FROM channel c WHERE c.organization_id=$1
  UNION ALL
  SELECT 1, t.id, coalesce(t.name,''), t.is_default,
    (SELECT count(*) FROM unread_messages u WHERE u.topic_id=t.id)
  FROM topic t WHERE t.organization_id=$1 AND t.channel_id=$3
)
SELECT kind, id, name, unread FROM entries
ORDER BY kind, is_default DESC, CASE WHEN kind=1 THEN lower(name) ELSE name END, id
`, organization, member, current, joined)
	if err != nil {
		return model, err
	}
	defer rows.Close()
	found := false
	for rows.Next() {
		var entry sidebarOracleEntry
		var kind int
		var count int64
		if err := rows.Scan(&kind, &entry.id, &entry.name, &count); err != nil {
			return model, err
		}
		entry.count = sidebarOracleDisplay(count)
		if kind == 0 {
			model.channels = append(model.channels, entry)
			found = found || entry.id == current
		} else {
			if len(model.topics) < 50 {
				model.topics = append(model.topics, entry)
			}
			if selected != nil && entry.id == *selected {
				model.selection = []sidebarOracleEntry{entry}
			}
		}
	}
	if err := rows.Err(); err != nil {
		return model, err
	}
	if !found || (selected != nil && len(model.selection) != 1) {
		return model, fmt.Errorf("oracle selection outside organisation/channel")
	}
	return model, nil
}

func TestSidebarOracle(t *testing.T) {
	pool := pgtest.New(t)
	f := conversationtest.OrganizationWithOwner(t, pool, "oracle", "general")
	account := identitytest.Account(t, pool, "reader@example.org", "Reader")
	member := orgtest.Member(t, pool, f.OrganizationID, account, org.RoleMember, "reader", 1)
	m := org.Membership{Organization: org.Organization{ID: f.OrganizationID}, Member: org.Member{ID: member, JoinedEventSeq: 1}}
	other := conversationtest.Channel(t, pool, f.OrganizationID, "other", false)
	zero := conversationtest.Channel(t, pool, f.OrganizationID, "zero", false)
	expected := sidebarOracleModel{organization: f.OrganizationID, member: member, current: f.Channel.ID,
		channels: []sidebarOracleEntry{{f.Channel.ID, "general", sidebarOracleDisplay(107)}, {other.ID, "other", sidebarOracleDisplay(2)}, {zero.ID, "zero", sidebarOracleDisplay(0)}},
		topics:   []sidebarOracleEntry{{f.Channel.DefaultTopicID, "", sidebarOracleDisplay(104)}}}
	var selected, first kernel.ID
	for i := 1; i <= 50; i++ {
		topic := conversationtest.Topic(t, pool, f.OrganizationID, f.Channel.ID, fmt.Sprintf("Topic%02d", i))
		count := int64(0)
		if i == 1 {
			first, count = topic.ID, 1
		}
		entry := sidebarOracleEntry{topic.ID, topic.Name, sidebarOracleDisplay(count)}
		if i < 50 {
			expected.topics = append(expected.topics, entry)
		} else {
			selected = topic.ID
			entry.count = sidebarOracleDisplay(2)
			expected.selection = []sidebarOracleEntry{entry}
		}
	}
	// Synthetic persisted fixtures, not synthetic event metadata. One pre-join
	// message and one read message leave 104 uncapped default-topic messages.
	if _, err := pool.Exec(t.Context(), `
INSERT INTO message (organization_id,channel_id,topic_id,member_id,body,event_seq)
SELECT $1,$2,$3,$4,'fixture',s FROM generate_series(1,106) s;
`, f.OrganizationID, f.Channel.ID, f.Channel.DefaultTopicID, f.MemberID); err != nil {
		t.Fatal(err)
	}
	for i, ids := range [][2]kernel.ID{{f.Channel.ID, first}, {f.Channel.ID, selected}, {f.Channel.ID, selected}, {other.ID, other.DefaultTopicID}, {other.ID, other.DefaultTopicID}} {
		if _, err := pool.Exec(t.Context(), `INSERT INTO message (organization_id,channel_id,topic_id,member_id,body,event_seq) VALUES ($1,$2,$3,$4,'fixture',$5)`, f.OrganizationID, ids[0], ids[1], f.MemberID, 107+i); err != nil {
			t.Fatal(err)
		}
	}
	before, err := sidebarOracle(t.Context(), pool, f.OrganizationID, member, f.Channel.ID, &selected)
	if err != nil || before.channels[0].count.raw != 108 {
		t.Fatalf("implicit join prefix without ranges: model=%+v error=%v", before, err)
	}
	merge := func(channel kernel.ID, hi int64) {
		t.Helper()
		if err := platform.InTx(t.Context(), pool, func(tx platform.Tx) error {
			return unreadpg.WriterIn(tx).Merge(t.Context(), unread.Scope{OrganizationID: f.OrganizationID, ChannelID: channel, MemberID: member}, 1, unread.Range{Lo: 0, Hi: hi})
		}); err != nil {
			t.Fatal(err)
		}
	}
	merge(f.Channel.ID, 3)
	read := func(selection *kernel.ID) sidebarOracleModel {
		t.Helper()
		got, err := sidebarOracle(t.Context(), pool, f.OrganizationID, member, f.Channel.ID, selection)
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	foreign := conversationtest.OrganizationWithOwner(t, pool, "foreign", "general")
	got := read(&selected)
	if err := sidebarOracleCompare(got, expected); err != nil {
		t.Fatal(err)
	}
	for _, selection := range []*kernel.ID{nil, &first, &selected} {
		model := read(selection)
		reader := conversationpg.NewReader(pool, nil, nil, eventCursor, costPageCountsIn)
		actual, err := reader.Sidebar(t.Context(), m, f.Channel.ID, selection)
		if err != nil {
			t.Fatal(err)
		}
		channels, topics := make(map[kernel.ID]int64), make(map[kernel.ID]int64)
		for _, entry := range model.channels {
			channels[entry.id] = entry.count.capped
		}
		for _, entry := range append(slices.Clone(model.topics), model.selection...) {
			topics[entry.id] = entry.count.capped
		}
		if len(actual.Channels) != len(model.channels) || len(actual.Topics) != 50 || !maps.Equal(actual.ChannelCounts, channels) || !maps.Equal(actual.TopicCounts, topics) {
			t.Fatal("persisted oracle disagrees with cost count adapter")
		}
	}
	for _, selection := range []kernel.ID{other.DefaultTopicID, foreign.Channel.DefaultTopicID} {
		if _, err := sidebarOracle(t.Context(), pool, f.OrganizationID, member, f.Channel.ID, &selection); err == nil {
			t.Fatal("oracle accepted foreign selection")
		}
	}
	for _, tt := range []struct {
		name   string
		change func(*sidebarOracleModel)
	}{
		{"overcount", func(m *sidebarOracleModel) { m.topics[1].count = sidebarOracleDisplay(2) }},
		{"undercount", func(m *sidebarOracleModel) { m.topics[1].count = sidebarOracleDisplay(0) }},
		{"other-channel", func(m *sidebarOracleModel) { m.channels[1].count = sidebarOracleDisplay(3) }},
		{"uncapped", func(m *sidebarOracleModel) { m.channels[0].count.raw++ }},
		{"display", func(m *sidebarOracleModel) { m.channels[0].count.capped-- }},
		{"zero-badge", func(m *sidebarOracleModel) { m.channels[2].count.badge = "1" }},
		{"selection", func(m *sidebarOracleModel) { m.selection = nil }},
		{"selection-count", func(m *sidebarOracleModel) { m.selection[0].count = sidebarOracleDisplay(1) }},
		{"missing-entry", func(m *sidebarOracleModel) { m.topics = m.topics[:49] }},
		{"extra-entry", func(m *sidebarOracleModel) { m.topics = append(m.topics, m.selection[0]) }},
		{"order", func(m *sidebarOracleModel) { m.topics[1], m.topics[2] = m.topics[2], m.topics[1] }},
		{"member", func(m *sidebarOracleModel) { m.member = f.MemberID }},
		{"organisation", func(m *sidebarOracleModel) { m.organization = foreign.OrganizationID }},
	} {
		t.Run(tt.name, func(t *testing.T) {
			bad := got
			bad.channels, bad.topics, bad.selection = slices.Clone(got.channels), slices.Clone(got.topics), slices.Clone(got.selection)
			tt.change(&bad)
			if sidebarOracleCompare(bad, got) == nil {
				t.Fatalf("exact oracle accepted %s", tt.name)
			}
		})
	}
	t.Run("read-only", func(t *testing.T) {
		merge(other.ID, 112)
		after := read(&selected)
		if sidebarOracleCompare(got, after) == nil {
			t.Fatal("exact oracle accepted missed read-only change")
		}
		expected.channels[1].count = sidebarOracleDisplay(0)
		if err := sidebarOracleCompare(after, expected); err != nil {
			t.Fatal(err)
		}
	})
}

func TestSidebarOracleDisplay(t *testing.T) {
	for _, want := range []sidebarOracleCount{{0, 0, ""}, {1, 1, "1"}, {99, 99, "99"}, {100, 100, "99+"}, {101, 100, "99+"}} {
		if got := sidebarOracleDisplay(want.raw); got != want {
			t.Fatalf("display = %+v, want %+v", got, want)
		}
	}
}
