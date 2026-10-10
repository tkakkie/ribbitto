package conversationpg_test

import (
	"encoding/json"
	"fmt"
	"os"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/tkakkie/ribbitto/internal/conversation/conversationtest"
	"github.com/tkakkie/ribbitto/internal/identity/identitytest"
	"github.com/tkakkie/ribbitto/internal/kernel"
	"github.com/tkakkie/ribbitto/internal/org"
	"github.com/tkakkie/ribbitto/internal/org/orgtest"
	"github.com/tkakkie/ribbitto/internal/platform/postgres/pgtest"
)

// TestUnreadBench measures decision 32, only with RIBBITTO_UNREAD_BENCH=1,
// never in CI. Run with -v; optional RIBBITTO_UNREAD_BENCH_* variables:
//
//	POSTS        comma-separated own-post counts (10,100,1000,10000)
//	MOVES        move batches (100)
//	MOVE_SIZE    read messages per batch (100)
//	RANGES       channel range rows including the prefix (10000; minimum 2)
//	TOPICS       listed topics (50; minimum 2)
//	WARMUP       warm-up repetitions (3)
//	REPEAT       measured repetitions (20)
//
// Normal and stressed states alternate on the same data. Timings include
// SQL round trips and Go range construction, exclude BEGIN/ROLLBACK and
// EXPLAIN, and do not measure commit durability or concurrent writers.
func TestUnreadBench(t *testing.T) {
	if os.Getenv("RIBBITTO_UNREAD_BENCH") != "1" || os.Getenv("CI") != "" {
		t.Skip("set RIBBITTO_UNREAD_BENCH=1 outside CI")
	}
	// Check every possible host before pgtest connects, including fallbacks.
	config, err := pgconn.ParseConfig(os.Getenv("RIBBITTO_TEST_DATABASE_URL"))
	requireNoError(t, err)
	hosts := []string{config.Host}
	for _, fallback := range config.Fallbacks {
		hosts = append(hosts, fallback.Host)
	}
	for _, host := range hosts {
		if host != "localhost" && host != "127.0.0.1" && host != "::1" {
			t.Fatalf("unread benchmark requires a loopback PostgreSQL server, not %q", host)
		}
	}
	posts := unreadBenchInts(t, "POSTS", "10,100,1000,10000")
	moves, batch := unreadBenchInts(t, "MOVES", "100")[0], unreadBenchInts(t, "MOVE_SIZE", "100")[0]
	ranges, topics := unreadBenchInts(t, "RANGES", "10000")[0], unreadBenchInts(t, "TOPICS", "50")[0]
	warmup, repeat := unreadBenchInts(t, "WARMUP", "3")[0], unreadBenchInts(t, "REPEAT", "20")[0]
	if ranges < 2 || topics < 2 {
		t.Fatal("RANGES and TOPICS must be at least 2")
	}
	pool := pgtest.NewEmpty(t)
	pgtest.NewMigrator(t, pool).UpTo(t.Context(), 11)
	f := conversationtest.OrganizationWithOwner(t, pool, "bench", "general")
	account := identitytest.Account(t, pool, "normal@example.org", "Normal")
	normal := orgtest.Member(t, pool, f.OrganizationID, account, org.RoleMember, "normal", 1)
	ids := []kernel.ID{f.Channel.DefaultTopicID}
	for i := 1; i < topics; i++ {
		ids = append(ids, conversationtest.Topic(t, pool, f.OrganizationID, f.Channel.ID, fmt.Sprintf("Topic %d", i)).ID)
	}
	exec := func(t *testing.T, sql string, args ...any) {
		_, err := pool.Exec(t.Context(), sql, args...)
		if err != nil {
			t.Fatal(fmt.Errorf("preparing unread benchmark: %w", err))
		}
	}
	exec(t, `CREATE TABLE channel_read (
 organization_id uuid, channel_id uuid, member_id uuid,
 PRIMARY KEY (organization_id, channel_id, member_id),
 FOREIGN KEY (organization_id, channel_id) REFERENCES channel (organization_id, id),
 FOREIGN KEY (organization_id, member_id) REFERENCES member (organization_id, id));
CREATE TABLE read_range (
 organization_id uuid, channel_id uuid, member_id uuid, lo bigint, hi bigint NOT NULL CHECK (lo < hi),
 PRIMARY KEY (organization_id, channel_id, member_id, lo),
 FOREIGN KEY (organization_id, channel_id, member_id) REFERENCES channel_read);
CREATE TABLE topic_read_floor (
 organization_id uuid, topic_id uuid, member_id uuid, channel_id uuid NOT NULL, floor_seq bigint NOT NULL,
 PRIMARY KEY (organization_id, topic_id, member_id),
 FOREIGN KEY (organization_id, channel_id, topic_id) REFERENCES topic (organization_id, channel_id, id),
 FOREIGN KEY (organization_id, member_id) REFERENCES member (organization_id, id));`)
	var version string
	requireNoError(t, pool.QueryRow(t.Context(), "SELECT version()").Scan(&version))
	t.Logf("%s; client=%s %s/%s CPUs=%d; warmup=%d repeat=%d; single connection, serial execution", version, runtime.Version(), runtime.GOOS, runtime.GOARCH, runtime.NumCPU(), warmup, repeat)
	settings, err := pool.Query(t.Context(), "SELECT name, setting, unit FROM pg_settings WHERE name IN ('shared_buffers','work_mem','effective_cache_size','jit','max_parallel_workers_per_gather','random_page_cost') ORDER BY name")
	requireNoError(t, err)
	defer settings.Close()
	for settings.Next() {
		values, err := settings.Values()
		requireNoError(t, err)
		t.Logf("setting: %v", values)
	}
	requireNoError(t, settings.Err())
	indexes, err := pool.Query(t.Context(), "SELECT indexdef FROM pg_indexes WHERE tablename IN ('message','read_range','topic_read_floor','channel_read') ORDER BY indexname")
	requireNoError(t, err)
	defer indexes.Close()
	for indexes.Next() {
		var definition string
		requireNoError(t, indexes.Scan(&definition))
		t.Log(definition)
	}
	requireNoError(t, indexes.Err())
	cases := []struct {
		name string
		size int
	}{{"moved", moves * batch}, {"fragmented", ranges}}
	for _, n := range posts {
		cases = append(cases, struct {
			name string
			size int
		}{"own-posts", n})
	}
	for _, scenario := range cases {
		t.Run(fmt.Sprintf("%s-%d", scenario.name, scenario.size), func(t *testing.T) {
			n, floor := scenario.size+1, int64(9)
			if scenario.name == "fragmented" {
				n = 2 * (scenario.size - 1)
			}
			cursor := int64(n + 9)
			if scenario.name == "moved" {
				floor = cursor
				cursor += int64(moves)
			}
			exec(t, "TRUNCATE message, read_range, topic_read_floor, channel_read")
			exec(t, `INSERT INTO message (organization_id, channel_id, member_id, topic_id, body, event_seq, moved_event_seq)
 SELECT $1,$2,CASE WHEN i=1 OR ($6 AND i%2=1) THEN $11::uuid ELSE $3 END,CASE WHEN $6 AND i%2=0 THEN ($4::uuid[])[2+(i/2)%($7-1)] ELSE ($4::uuid[])[1] END,
 'benchmark',i+9,CASE WHEN $5 THEN $8::bigint+greatest(i-2,0)/$9+1 END FROM generate_series(1,$10::int) i`,
				f.OrganizationID, f.Channel.ID, f.MemberID, ids, scenario.name == "moved", scenario.name == "fragmented", topics, floor, batch, n, normal)
			exec(t, "UPDATE organization SET event_seq=$2,event_log_boundary_seq=$2 WHERE id=$1", f.OrganizationID, cursor)
			for _, member := range []kernel.ID{normal, f.MemberID} {
				exec(t, "INSERT INTO channel_read VALUES ($1,$2,$3)", f.OrganizationID, f.Channel.ID, member)
				if member == normal {
					exec(t, "INSERT INTO read_range VALUES ($1,$2,$3,0,$4)", f.OrganizationID, f.Channel.ID, member, cursor+1)
				} else {
					exec(t, "INSERT INTO read_range VALUES ($1,$2,$3,0,2)", f.OrganizationID, f.Channel.ID, member)
					if scenario.name == "fragmented" {
						exec(t, "INSERT INTO read_range SELECT $1,$2,$3,i+9,i+10 FROM generate_series(2,$4::int,2) i", f.OrganizationID, f.Channel.ID, member, n)
					} else {
						exec(t, "INSERT INTO read_range VALUES ($1,$2,$3,11,$4)", f.OrganizationID, f.Channel.ID, member, int64(n+10))
					}
				}
				value := floor
				if member == normal {
					value = cursor
				}
				exec(t, "INSERT INTO topic_read_floor SELECT $1,id,$2,$3,$4 FROM unnest($5::uuid[]) id", f.OrganizationID, member, f.Channel.ID, value, ids)
			}
			exec(t, "ANALYZE message; ANALYZE read_range; ANALYZE topic_read_floor; ANALYZE channel_read")
			var rows, bytes, total int64
			requireNoError(t, pool.QueryRow(t.Context(), "SELECT count(*),pg_relation_size('read_range'),pg_total_relation_size('read_range') FROM read_range").Scan(&rows, &bytes, &total))
			t.Logf("ANALYZE ran; messages=%d topics=%d ranges(normal=1, stressed=%d) total_rows=%d table_bytes=%d including_indexes_bytes=%d configured_moves=%d configured_batch=%d cursor=%d", n, topics, map[bool]int{true: ranges, false: 2}[scenario.name == "fragmented"], rows, bytes, total, moves, batch, cursor)
			for _, operation := range []string{"step1", "step2", "step3-load-decode", "step4-topics", "feed-write", "topic-write", "topic-write-per-message"} {
				samples := [2][]time.Duration{}
				for run := -warmup; run <= repeat; run++ {
					for state, member := range []kernel.ID{normal, f.MemberID} {
						tx, err := pool.BeginTx(t.Context(), pgx.TxOptions{IsoLevel: pgx.RepeatableRead})
						requireNoError(t, err)
						func() {
							defer func() { requireNoError(t, tx.Rollback(t.Context())) }()
							u := unreadBenchRun{t: t, tx: tx, key: []any{f.OrganizationID, f.Channel.ID, member}, topics: ids, cursor: cursor}
							// Inputs belong to this snapshot but are outside the measured statement.
							u.load(false)
							u.gaps(false)
							u.explain = run == repeat
							start := time.Now()
							u.measure(operation)
							took := time.Since(start)
							if run >= 0 && run < repeat {
								samples[state] = append(samples[state], took)
							}
							if u.explain {
								t.Logf("%s state=%d returned_rows=%d relation_rows_visited=%g shared_hits=%g shared_reads=%g (EXPLAIN; rows include filter removals and loops)", operation, state, u.rows, u.scanned, u.hits, u.reads)
								switch operation {
								case "feed-write", "topic-write", "topic-write-per-message":
									// Diagnostics must not add work to timings or plan totals.
									var after int64
									err := tx.QueryRow(t.Context(), "SELECT count(*) FROM read_range WHERE organization_id=$1 AND channel_id=$2 AND member_id=$3", u.key...).Scan(&after)
									if err != nil {
										t.Fatal(fmt.Errorf("counting unread benchmark ranges: %w", err))
									}
									t.Logf("%s range_rows_after=%d", operation, after)
								}
							}
						}()
					}
				}
				for state, times := range samples {
					slices.Sort(times)
					t.Logf("%s state=%d (0=normal,1=stressed) median=%s p95=%s repetitions=%d", operation, state, times[(len(times)-1)/2], times[(95*len(times)+99)/100-1], len(times))
				}
			}
		})
	}
}

type unreadBenchRun struct {
	t                    *testing.T
	tx                   pgx.Tx
	key                  []any
	topics               []kernel.ID
	cursor, prefix       int64
	ranges               pgtype.Multirange[pgtype.Range[int64]]
	floors, gapLo, gapHi []int64
	explain              bool
	rows                 int
	plans                map[string]bool
	scanned, hits, reads float64
}

func (u *unreadBenchRun) query(sql string, extra ...any) [][]any {
	u.t.Helper()
	args := append(slices.Clone(u.key), extra...)
	// Type unused scope parameters even in conversation-only statements.
	scope := "WITH benchmark_scope AS (SELECT $1::uuid,$2::uuid,$3::uuid) "
	if strings.HasPrefix(sql, "WITH ") {
		sql = scope[:len(scope)-1] + "," + strings.TrimPrefix(sql, "WITH ")
	} else {
		sql = scope + sql
	}
	if u.explain {
		_, err := u.tx.Exec(u.t.Context(), "SAVEPOINT plan")
		requireNoError(u.t, err)
		var raw []byte
		requireNoError(u.t, u.tx.QueryRow(u.t.Context(), "EXPLAIN (ANALYZE, BUFFERS, FORMAT JSON) "+sql, args...).Scan(&raw))
		if u.plans == nil {
			u.plans = make(map[string]bool)
		}
		if !u.plans[sql] {
			u.t.Logf("plan: %s\n%s", sql, raw)
			u.plans[sql] = true
		}
		var plan []struct{ Plan map[string]any }
		requireNoError(u.t, json.Unmarshal(raw, &plan))
		u.scanned += unreadBenchScanned(plan[0].Plan)
		for name, target := range map[string]*float64{"Shared Hit Blocks": &u.hits, "Shared Read Blocks": &u.reads} {
			if n, ok := plan[0].Plan[name].(float64); ok {
				*target += n
			}
		}
		_, err = u.tx.Exec(u.t.Context(), "ROLLBACK TO SAVEPOINT plan; RELEASE SAVEPOINT plan")
		requireNoError(u.t, err)
	}
	rows, err := u.tx.Query(u.t.Context(), sql, args...)
	if err != nil {
		u.t.Fatal(fmt.Errorf("running unread benchmark statement: %w", err))
	}
	defer rows.Close()
	var out [][]any
	for rows.Next() {
		values, err := rows.Values()
		requireNoError(u.t, err)
		out = append(out, values)
	}
	requireNoError(u.t, rows.Err())
	u.rows += len(out)
	return out
}

func (u *unreadBenchRun) gaps(measured bool) {
	u.explain = measured && u.explain
	rows := u.query(`SELECT r.lo,r.hi FROM unnest(ARRAY[$2::uuid]) c(id) CROSS JOIN LATERAL (
 SELECT lo,hi FROM read_range WHERE organization_id=$1 AND channel_id=c.id AND member_id=$3 ORDER BY lo LIMIT 101) r`)
	u.gapLo, u.gapHi = nil, nil
	for i, row := range rows {
		if i == 100 {
			break
		} // The 101st range bounds the 100th gap.
		hi := int64(9223372036854775807)
		if i+1 < len(rows) {
			hi = rows[i+1][0].(int64)
		}
		u.gapLo = append(u.gapLo, row[1].(int64))
		u.gapHi = append(u.gapHi, hi)
	}
}

func (u *unreadBenchRun) load(measured bool) {
	u.explain = measured && u.explain
	rows := u.query(`WITH prefix AS (SELECT hi FROM read_range WHERE organization_id=$1 AND channel_id=$2 AND member_id=$3 AND lo=0)
 SELECT 0::bigint,lo,hi FROM read_range WHERE organization_id=$1 AND channel_id=$2 AND member_id=$3 AND hi>=(SELECT hi FROM prefix)
 UNION ALL SELECT t.n,coalesce(f.floor_seq,(SELECT hi-1 FROM prefix)),0 FROM unnest($4::uuid[]) WITH ORDINALITY t(id,n)
 LEFT JOIN topic_read_floor f ON f.organization_id=$1 AND f.channel_id=$2 AND f.member_id=$3 AND f.topic_id=t.id`, u.topics)
	u.ranges = nil
	u.floors = make([]int64, len(u.topics))
	u.rows = 0
	for _, row := range rows {
		if row[0].(int64) == 0 {
			lo, hi := row[1].(int64), row[2].(int64)
			if lo == 0 {
				u.prefix = hi
			}
			u.ranges = append(u.ranges, pgtype.Range[int64]{Lower: lo, Upper: hi, LowerType: pgtype.Inclusive, UpperType: pgtype.Exclusive, Valid: true})
		} else {
			u.floors[row[0].(int64)-1] = row[1].(int64)
		}
	}
	u.rows = len(rows)
}

func (u *unreadBenchRun) measure(operation string) {
	u.rows = 0
	const above = `SELECT event_seq FROM message WHERE organization_id=$1 AND topic_id=t.id AND event_seq >= $5 AND event_seq>t.floor AND NOT ($4::int8multirange @> event_seq) ORDER BY event_seq`
	const moved = `SELECT event_seq FROM message WHERE organization_id=$1 AND topic_id=t.id AND event_seq >= $5 AND event_seq<=t.floor AND moved_event_seq>t.floor AND NOT ($4::int8multirange @> event_seq) ORDER BY moved_event_seq`
	candidates := "(" + above + ") UNION ALL (" + moved + ")"
	switch operation {
	case "step1":
		u.gaps(true)
	case "step2":
		u.query(`SELECT c.id,count(m.event_seq),min(m.event_seq) FROM unnest(ARRAY[$2::uuid]) c(id) LEFT JOIN LATERAL (
 SELECT m.event_seq FROM unnest($4::bigint[],$5::bigint[]) g(lo,hi) CROSS JOIN LATERAL (
 SELECT event_seq FROM message WHERE organization_id=$1 AND channel_id=c.id AND event_seq>=g.lo AND event_seq<g.hi ORDER BY event_seq LIMIT 100) m LIMIT 100) m ON true GROUP BY c.id`, u.gapLo, u.gapHi)
	case "step3-load-decode":
		u.load(true)
	case "step4-topics":
		u.query(`SELECT t.id,c.n,CASE WHEN t.id=($6::uuid[])[1] THEN first.seq END FROM unnest($6::uuid[],$7::bigint[]) t(id,floor)
 CROSS JOIN LATERAL (SELECT count(*) n FROM (`+candidates+` LIMIT 100) m) c
 LEFT JOIN LATERAL (SELECT min(event_seq) seq FROM ((`+above+` LIMIT 1) UNION ALL (`+moved+`)) m WHERE t.id=($6::uuid[])[1]) first ON true`, u.ranges, u.prefix, u.topics, u.floors)
	case "feed-write", "topic-write", "topic-write-per-message":
		u.query("SELECT channel_id FROM channel_read WHERE organization_id=$1 AND channel_id=$2 AND member_id=$3 FOR UPDATE")
		if operation == "feed-write" {
			n := u.query("SELECT coalesce(min(event_seq),$4::bigint+1) FROM message WHERE organization_id=$1 AND channel_id=$2 AND event_seq>$4", u.cursor)[0][0].(int64)
			u.merge(0, n)
		} else {
			u.load(true)
			newRanges := `SELECT coalesce((SELECT event_seq+1 FROM message WHERE organization_id=$1 AND channel_id=$2 AND event_seq<m.event_seq ORDER BY event_seq DESC LIMIT 1),0) lo,
 coalesce((SELECT event_seq FROM message WHERE organization_id=$1 AND channel_id=$2 AND event_seq>m.event_seq ORDER BY event_seq LIMIT 1),m.event_seq+1) hi
 FROM unnest(ARRAY[($6::uuid[])[1]],ARRAY[($7::bigint[])[1]]) t(id,floor) CROSS JOIN LATERAL (` + candidates + `) m
 JOIN message shown ON shown.organization_id=$1 AND shown.event_seq=m.event_seq WHERE m.event_seq<=$8 AND (shown.moved_event_seq IS NULL OR shown.moved_event_seq<=$8)`
			if operation == "topic-write" {
				rows := u.query(newRanges, u.ranges, u.prefix, u.topics, u.floors, u.cursor)
				lo, hi := make([]int64, 0, len(rows)), make([]int64, 0, len(rows))
				for _, row := range rows {
					lo, hi = append(lo, row[0].(int64)), append(hi, row[1].(int64))
				}
				u.topicWrite(lo, hi)
			} else {
				rows := u.query(newRanges, u.ranges, u.prefix, u.topics, u.floors, u.cursor)
				for _, row := range rows {
					u.merge(row[0].(int64), row[1].(int64))
				}
				u.query(`INSERT INTO topic_read_floor VALUES ($1,($4::uuid[])[1],$3,$2,$5) ON CONFLICT (organization_id,topic_id,member_id)
 DO UPDATE SET floor_seq=greatest(topic_read_floor.floor_seq,excluded.floor_seq) RETURNING floor_seq`, u.topics, u.cursor)
			}
		}
	}
}

func (u *unreadBenchRun) merge(lo, hi int64) {
	// Disjoint ranges can only reach left through the immediate predecessor.
	u.query(`WITH removed AS (DELETE FROM read_range WHERE organization_id=$1 AND channel_id=$2 AND member_id=$3
 AND lo>=coalesce((SELECT lo FROM read_range WHERE organization_id=$1 AND channel_id=$2 AND member_id=$3 AND lo<=$4 ORDER BY lo DESC LIMIT 1),$4)
 AND lo<=$5 AND hi>=$4 RETURNING lo,hi)
 INSERT INTO read_range SELECT $1,$2,$3,least($4,min(lo)),greatest($5,max(hi)) FROM removed RETURNING lo,hi`, lo, hi)
}

func (u *unreadBenchRun) topicWrite(lo, hi []int64) {
	// Only range values cross from conversation's candidate query to unread.
	// Coalesce additions first so neighbour probes depend on runs, not messages.
	// Use DELETE's returned rows to order replacement inserts after deletion.
	u.query(`WITH additions AS (SELECT range_agg(int8range(lo,hi)) ranges FROM unnest($4::bigint[],$5::bigint[]) candidates(lo,hi)),
 new_ranges AS MATERIALIZED (SELECT lower(r) lo,upper(r) hi FROM additions CROSS JOIN LATERAL unnest(ranges) r),
 touched AS MATERIALIZED (SELECT DISTINCT r.lo FROM new_ranges n CROSS JOIN LATERAL (
 SELECT lo FROM read_range WHERE organization_id=$1 AND channel_id=$2 AND member_id=$3
 AND lo>=coalesce((SELECT lo FROM read_range WHERE organization_id=$1 AND channel_id=$2 AND member_id=$3 AND lo<=n.lo ORDER BY lo DESC LIMIT 1),n.lo)
 AND lo<=n.hi AND hi>=n.lo) r),
 removed AS (DELETE FROM read_range r USING touched t WHERE r.organization_id=$1 AND r.channel_id=$2 AND r.member_id=$3 AND r.lo=t.lo RETURNING r.lo,r.hi),
 merged AS (SELECT unnest(range_agg(int8range(lo,hi))) r FROM (SELECT lo,hi FROM new_ranges UNION ALL SELECT lo,hi FROM removed) ranges),
 inserted AS (INSERT INTO read_range SELECT $1,$2,$3,lower(r),upper(r) FROM merged RETURNING lo,hi)
 INSERT INTO topic_read_floor VALUES ($1,$6,$3,$2,$7) ON CONFLICT (organization_id,topic_id,member_id)
 DO UPDATE SET floor_seq=greatest(topic_read_floor.floor_seq,excluded.floor_seq) RETURNING floor_seq`, lo, hi, u.topics[0], u.cursor)
}

func unreadBenchScanned(plan map[string]any) float64 {
	var total float64
	if kind, _ := plan["Node Type"].(string); plan["Relation Name"] != nil && strings.Contains(kind, "Scan") {
		for _, key := range []string{"Actual Rows", "Rows Removed by Filter", "Rows Removed by Index Recheck"} {
			if n, ok := plan[key].(float64); ok {
				total += n
			}
		}
		total *= plan["Actual Loops"].(float64)
	}
	if children, ok := plan["Plans"].([]any); ok {
		for _, child := range children {
			total += unreadBenchScanned(child.(map[string]any))
		}
	}
	return total
}

func unreadBenchInts(t *testing.T, suffix, fallback string) []int {
	t.Helper()
	name := "RIBBITTO_UNREAD_BENCH_" + suffix
	raw := os.Getenv(name)
	if raw == "" {
		raw = fallback
	}
	var out []int
	for _, field := range strings.Split(raw, ",") {
		n, err := strconv.Atoi(strings.TrimSpace(field))
		if err != nil || n < 1 {
			t.Fatalf("%s: want positive integers", name)
		}
		out = append(out, n)
	}
	return out
}
