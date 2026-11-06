package postgres_test

import (
	"context"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/tkakkie/ribbitto/internal/compose"
	"github.com/tkakkie/ribbitto/internal/conversation"
	"github.com/tkakkie/ribbitto/internal/conversation/conversationpg"
	"github.com/tkakkie/ribbitto/internal/conversation/conversationtest"
	"github.com/tkakkie/ribbitto/internal/conversation/internal/postgres"
	"github.com/tkakkie/ribbitto/internal/identity/identitytest"
	"github.com/tkakkie/ribbitto/internal/kernel"
	"github.com/tkakkie/ribbitto/internal/org"
	"github.com/tkakkie/ribbitto/internal/org/orgtest"
	platform "github.com/tkakkie/ribbitto/internal/platform/postgres"
	"github.com/tkakkie/ribbitto/internal/platform/postgres/pgtest"
	"github.com/tkakkie/ribbitto/internal/platform/postgres/pgxbridge"
	"github.com/tkakkie/ribbitto/internal/unread"
)

type unreadListFixture struct {
	pool     *pgxpool.Pool
	member   org.Membership
	channels []kernel.ID
	labels   map[string]kernel.ID
}

func unreadListExec(t *testing.T, pool *pgxpool.Pool, sql string, args ...any) {
	t.Helper()
	_, err := pool.Exec(t.Context(), sql, args...)
	requireNoError(t, err)
}

func unreadListSmall(t *testing.T) unreadListFixture {
	t.Helper()
	pool := pgtest.New(t)
	f := conversationtest.OrganizationWithOwner(t, pool, "unread-list", "general")
	member := org.Membership{Organization: org.Organization{ID: f.OrganizationID}, Member: org.Member{ID: f.MemberID, JoinedEventSeq: 1}}
	writer := orgtest.Member(t, pool, f.OrganizationID, identitytest.Account(t, pool, "writer@example.org", "Writer"), org.RoleMember, "writer", 1)
	labels := map[string]kernel.ID{"bulk-99+": f.Channel.DefaultTopicID}
	channels := []kernel.ID{f.Channel.ID}
	topic := func(channel kernel.ID, name string) kernel.ID {
		id := conversationtest.Topic(t, pool, f.OrganizationID, channel, name).ID
		labels[name] = id
		return id
	}
	message := func(channel, id kernel.ID, seq int64, moved any, own bool) kernel.ID {
		author := writer
		if own {
			author = f.MemberID
		}
		var result kernel.ID
		requireNoError(t, pool.QueryRow(t.Context(), `INSERT INTO message (organization_id,channel_id,topic_id,member_id,body,event_seq,moved_event_seq) VALUES ($1,$2,$3,$4,'fixture',$5,$6) RETURNING id`, f.OrganizationID, channel, id, author, seq, moved).Scan(&result))
		return result
	}
	read := func(channel kernel.ID, lo, hi int64) {
		unreadListExec(t, pool, `INSERT INTO channel_read VALUES ($1,$2,$3) ON CONFLICT DO NOTHING`, f.OrganizationID, channel, f.MemberID)
		unreadListExec(t, pool, `INSERT INTO read_range VALUES ($1,$2,$3,$4,$5)`, f.OrganizationID, channel, f.MemberID, lo, hi)
	}
	floor := func(channel, id kernel.ID, value int64) {
		unreadListExec(t, pool, `INSERT INTO channel_read VALUES ($1,$2,$3) ON CONFLICT DO NOTHING`, f.OrganizationID, channel, f.MemberID)
		unreadListExec(t, pool, `INSERT INTO topic_read_floor (organization_id,channel_id,member_id,topic_id,floor_seq) VALUES ($1,$2,$3,$4,$5)`, f.OrganizationID, channel, f.MemberID, id, value)
	}
	for i := int64(10); i < 115; i++ {
		message(f.Channel.ID, f.Channel.DefaultTopicID, i, nil, false)
	}
	message(f.Channel.ID, topic(f.Channel.ID, "after-channel-100"), 500, nil, false)
	message(f.Channel.ID, topic(f.Channel.ID, "own-post"), 600, nil, true)
	// Posting unions its own sequence into the read set; author filtering would
	// hide broken state and would disagree with the existing counter's contract.
	read(f.Channel.ID, 0, 2)
	read(f.Channel.ID, 600, 601)
	for i := 0; i < 60; i++ {
		id := topic(f.Channel.ID, fmt.Sprintf("topic-%02d", i))
		message(f.Channel.ID, id, int64(1000+i), nil, false)
	}
	resolved := topic(f.Channel.ID, "zz-resolved-beyond-50")
	conclusion := message(f.Channel.ID, resolved, 1100, nil, false)
	unreadListExec(t, pool, `UPDATE topic SET conclusion_message_id=$2 WHERE id=$1`, resolved, conclusion)
	// The newest read message, rather than the newest unread, controls rank.
	message(f.Channel.ID, labels["topic-00"], 1150, nil, true)
	read(f.Channel.ID, 1150, 1151)

	moves := conversationtest.Channel(t, pool, f.OrganizationID, "moves", false)
	channels = append(channels, moves.ID)
	read(moves.ID, 0, 2)
	source := topic(moves.ID, "moved-out")
	destination := topic(moves.ID, "moved-first")
	movedID := message(moves.ID, source, 1200, nil, false)
	unreadListExec(t, pool, `UPDATE message SET topic_id=$2,moved_event_seq=3000 WHERE id=$1`, movedID, destination)
	message(moves.ID, destination, 1202, int64(3001), false)
	read(moves.ID, 1202, 1203)
	message(moves.ID, destination, 1204, int64(1900), false) // a hole below an explicit floor
	message(moves.ID, destination, 2100, nil, false)
	floor(moves.ID, destination, 2000)
	many := topic(moves.ID, "uncapped-moved-first")
	// The smallest event sequence is the last by move sequence, outside the
	// first 100 moved candidates. A capped first-unread lookup must fail here.
	for i := int64(1300); i < 1402; i++ {
		message(moves.ID, many, i, 5000-i, false)
	}
	floor(moves.ID, many, 2000)

	fragmented := conversationtest.Channel(t, pool, f.OrganizationID, "fragmented", false)
	channels = append(channels, fragmented.ID)
	read(fragmented.ID, 0, 2)
	readOnly := topic(fragmented.ID, "positive-range-read")
	for i := int64(0); i < 110; i++ {
		seq := 6000 + 2*i
		id := fragmented.DefaultTopicID
		if i == 109 {
			id = readOnly
		}
		message(fragmented.ID, id, seq, nil, false)
		read(fragmented.ID, seq, seq+1)
		message(fragmented.ID, fragmented.DefaultTopicID, seq+1, nil, false)
	}
	message(fragmented.ID, topic(fragmented.ID, "after-range-101"), 6220, nil, false)
	joined := conversationtest.Channel(t, pool, f.OrganizationID, "no-read-rows", false)
	channels = append(channels, joined.ID)
	labels["join-prefix"] = joined.DefaultTopicID
	message(joined.ID, joined.DefaultTopicID, 1, nil, false)
	message(joined.ID, joined.DefaultTopicID, 7000, nil, false)
	// All channels are public today. Withholding this ID models an access
	// resolver's unreadable channel: the candidate must only use supplied IDs.
	excluded := conversationtest.Channel(t, pool, f.OrganizationID, "withheld", false)
	message(excluded.ID, excluded.DefaultTopicID, 8000, nil, false)
	foreign := conversationtest.OrganizationWithOwner(t, pool, "foreign-list", "foreign")
	unreadListExec(t, pool, `INSERT INTO message (organization_id,channel_id,topic_id,member_id,body,event_seq) VALUES ($1,$2,$3,$4,'foreign',9000)`, foreign.OrganizationID, foreign.Channel.ID, foreign.Channel.DefaultTopicID, foreign.MemberID)
	return unreadListFixture{pool, member, channels, labels}
}

// TestUnreadListCorrectness is Stage 0: small fixtures in the normal database
// suite. Both candidates, every page and the existing selected-topic oracle
// run in the same repeatable-read snapshot; a mismatch blocks timing.
// With a disposable RIBBITTO_TEST_DATABASE_URL and RIBBITTO_REQUIRE_DB=1, run:
//
//	go test -count=1 -run '^TestUnreadList' -v ./internal/conversation/internal/postgres
func TestUnreadListCorrectness(t *testing.T) {
	f := unreadListSmall(t)
	pool, trace := unreadListTracedPool(t, f.pool)
	requireNoError(t, platform.InSnapshot(t.Context(), pool, func(s platform.Snapshot) error {
		want := unreadListOracle(t, s, f)
		if len(want) <= 50 {
			t.Fatal("fixture must exercise multiple pages")
		}
		for _, name := range []string{"after-channel-100", "zz-resolved-beyond-50", "after-range-101", "moved-first", "uncapped-moved-first", "join-prefix", "bulk-99+"} {
			if !slices.ContainsFunc(want, func(r unreadListRow) bool { return r.Topic == f.labels[name] }) {
				t.Fatalf("missing fixture %s", name)
			}
		}
		for _, form := range []string{"V0", "V1"} {
			got := unreadListAll(t, s, f, form, "", trace)
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("%s differs from oracle: got=%+v want=%+v", form, got, want)
			}
			// Real counts-only sidebar path, including a topic beyond its 50.
			topics, err := postgres.ReadStoreIn(s).ListTopics(t.Context(), f.member.Organization.ID, f.channels[0], 50)
			requireNoError(t, err)
			ids := make([]kernel.ID, len(topics))
			for i, topic := range topics {
				ids[i] = topic.ID
			}
			selected := f.labels["zz-resolved-beyond-50"]
			counts, err := compose.PageCountsIn(s).(conversation.SidebarCounter).ReadCounts(t.Context(), f.member, f.channels[0], f.channels, ids, &selected)
			requireNoError(t, err)
			if len(topics) != 50 || slices.Contains(ids, selected) {
				t.Fatal("resolved fixture must sort beyond sidebar")
			}
			for _, row := range got {
				if count, ok := counts.TopicCounts[row.Topic]; ok && count != row.Count {
					t.Fatalf("sidebar badge mismatch: %+v", row)
				}
				if row.Topic == f.labels["bulk-99+"] && row.Count != 100 {
					t.Fatal("99+ count must cap at 100")
				}
			}
		}
		for _, mutation := range []string{"drop-moved-branch", "drop-positive-ranges", "ignore-explicit-floor", "capped-first-unread", "sidebar-50", "channel-first-100", "ignore-join-prefix", "inclusive-keyset"} {
			for _, form := range []string{"V0", "V1"} {
				got := unreadListAll(t, s, f, form, mutation, trace)
				if reflect.DeepEqual(got, want) {
					t.Fatalf("%s survived mutation %s", form, mutation)
				}
				var changed []string
				for name, id := range f.labels {
					expected := slices.IndexFunc(want, func(r unreadListRow) bool { return r.Topic == id })
					actual := slices.IndexFunc(got, func(r unreadListRow) bool { return r.Topic == id })
					if expected < 0 && actual >= 0 || expected >= 0 && (actual < 0 || got[actual] != want[expected]) {
						changed = append(changed, name)
					}
				}
				if mutation == "inclusive-keyset" {
					changed = append(changed, "duplicate-page-boundary")
				}
				slices.Sort(changed)
				t.Logf("mutation=%s form=%s rejected; fixtures=%v", mutation, form, changed)
			}
		}
		return nil
	}))
}

func unreadListOracle(t *testing.T, s platform.Snapshot, f unreadListFixture) []unreadListRow {
	t.Helper()
	var want []unreadListRow
	for _, channel := range f.channels {
		topics, err := postgres.ReadStoreIn(s).ListTopics(t.Context(), f.member.Organization.ID, channel, 1000)
		requireNoError(t, err)
		for _, topic := range topics {
			scope := unread.Scope{OrganizationID: f.member.Organization.ID, ChannelID: channel, MemberID: f.member.Member.ID}
			counts, first, err := compose.NewTopicCounts().ReadWithFirstUnread(t.Context(), s, scope, f.member.Member.JoinedEventSeq, []kernel.ID{topic.ID}, &topic.ID, true)
			requireNoError(t, err)
			if counts[topic.ID] == 0 {
				continue
			}
			row := unreadListRow{Channel: channel, Topic: topic.ID, Name: topic.Name, Default: topic.IsDefault, Resolved: topic.ConclusionMessageID != (kernel.ID{}), Count: counts[topic.ID], First: first}
			requireNoError(t, pgxbridge.Snapshot(s).QueryRow(t.Context(), `SELECT max(event_seq),(SELECT id FROM message WHERE organization_id=$1 AND event_seq=$3) FROM message WHERE organization_id=$1 AND topic_id=$2`, f.member.Organization.ID, topic.ID, first).Scan(&row.Last, &row.FirstID))
			want = append(want, row)
		}
	}
	slices.SortFunc(want, func(a, b unreadListRow) int {
		if a.Last > b.Last {
			return -1
		}
		if a.Last < b.Last {
			return 1
		}
		return -strings.Compare(fmt.Sprintf("%x", a.Topic), fmt.Sprintf("%x", b.Topic))
	})
	return want
}

func unreadListAll(t *testing.T, s platform.Snapshot, f unreadListFixture, form, mutation string, trace *unreadListTrace) []unreadListRow {
	t.Helper()
	var got []unreadListRow
	var after *unreadListPosition
	// Bounded even for a broken keyset; duplicates must be observable, never hang.
	for range 10 {
		before := trace.Counts().Queries
		rows, err := unreadListPage(t.Context(), s, f.member, append(slices.Clone(f.channels), f.channels[0]), after, form, mutation)
		requireNoError(t, err)
		added := int64(1)
		if form == "V1" {
			added = 2
		}
		if delta := trace.Counts().Queries - before; delta != added {
			t.Fatalf("%s added statements=%d, want %d", form, delta, added)
		}
		n := min(50, len(rows))
		got = append(got, rows[:n]...)
		if len(rows) <= 50 {
			return got
		}
		last := rows[n-1]
		after = &unreadListPosition{last.Last, last.Topic}
	}
	t.Fatal("keyset did not terminate")
	return nil
}

type unreadListTrace struct {
	*platform.QueryCounter
	names []string
}

func (p *unreadListTrace) TraceQueryStart(ctx context.Context, conn *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	p.QueryCounter.TraceQueryStart(ctx, conn, data)
	name := strings.Fields(data.SQL)[0]
	if fields := strings.Fields(data.SQL); len(fields) > 2 && fields[1] == "name:" {
		name = fields[2]
	}
	p.names = append(p.names, name)
	return ctx
}

func unreadListTracedPool(t *testing.T, pool *pgxpool.Pool) (*pgxpool.Pool, *unreadListTrace) {
	t.Helper()
	trace := &unreadListTrace{QueryCounter: platform.NewQueryCounter()}
	config := pool.Config()
	config.ConnConfig.Tracer = trace
	config.ConnConfig.RuntimeParams["statement_timeout"] = "30000ms"
	config.AfterConnect = func(_ context.Context, conn *pgx.Conn) error {
		// pgx's default map has the multirange, but not its array codec.
		element, _ := conn.TypeMap().TypeForOID(pgtype.Int8multirangeOID)
		conn.TypeMap().RegisterType(&pgtype.Type{Name: "_int8multirange", OID: pgtype.Int8multirangeArrayOID, Codec: &pgtype.ArrayCodec{ElementType: element}})
		return nil
	}
	traced, err := pgxpool.NewWithConfig(t.Context(), config)
	requireNoError(t, err)
	t.Cleanup(traced.Close)
	return traced, trace
}

type unreadListPageCounter struct {
	conversation.PageCounter
	snapshot platform.Snapshot
	form     string
}

func (c unreadListPageCounter) ReadCounts(ctx context.Context, m org.Membership, channel kernel.ID, channels, topics []kernel.ID, selected *kernel.ID) (conversation.PageCounts, error) {
	counts, err := c.PageCounter.(conversation.SidebarCounter).ReadCounts(ctx, m, channel, channels, topics, selected)
	if err == nil {
		_, err = unreadListPage(ctx, c.snapshot, m, channels, nil, c.form, "")
	}
	return counts, err
}

func TestUnreadListPageStatements(t *testing.T) {
	f := unreadListSmall(t)
	resolved := f.labels["zz-resolved-beyond-50"]
	for _, form := range []string{"V0", "V1"} {
		for _, selected := range []*kernel.ID{nil, &resolved} {
			pool, trace := unreadListTracedPool(t, f.pool)
			factory := func(s platform.Snapshot) conversation.PageCounter {
				return unreadListPageCounter{compose.PageCountsIn(s), s, form}
			}
			_, err := conversationpg.NewReader(pool, nil, nil, compose.EventCursor, factory).Sidebar(t.Context(), f.member, f.channels[0], selected)
			requireNoError(t, err)
			added := int64(1)
			if form == "V1" {
				added = 2
			}
			want := int64(9) + added
			if selected != nil {
				want++
			}
			q := trace.Counts()
			if q.Queries != want || q.Begins != 1 || q.Commits != 1 || q.Rollbacks != 0 {
				t.Fatalf("%s selected=%t: %+v names=%v", form, selected != nil, q, trace.names)
			}
			expected := []string{"begin", "SET", "GetChannel", "ListChannels", "ListTopics", "GetEventSeq", "FirstChannelReadRanges", "CountChannelUnread", "ReadTopicState", "CountTopicUnread", "UnreadTopicPage" + form, "commit"}
			if selected != nil {
				expected = append(expected, "GetTopic")
			}
			if form == "V1" {
				expected = append(expected, "OrgReadState")
			}
			names := slices.Clone(trace.names)
			slices.Sort(names)
			slices.Sort(expected)
			if !slices.Equal(names, expected) {
				t.Fatalf("unexpected statements: %v want %v", names, expected)
			}
			t.Logf("%s selected=%t unread-path=%d whole-page=%d (+ BEGIN/COMMIT); names=%v", form, selected != nil, 4+added, q.Queries, trace.names)
		}
	}
}
