package postgres_test

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/tkakkie/ribbitto/internal/kernel"
	"github.com/tkakkie/ribbitto/internal/org"
	platform "github.com/tkakkie/ribbitto/internal/platform/postgres"
)

type unreadListBenchGuard struct {
	t         *testing.T
	ctx       context.Context
	pool      *pgxpool.Pool
	highWater int64
	mu        sync.Mutex
	stop      error
	observe   *pgxpool.Pool
	cancel    context.CancelFunc
	lastLog   time.Time
}

func (g *unreadListBenchGuard) require(err error) {
	g.t.Helper()
	g.mu.Lock()
	stop := g.stop
	g.mu.Unlock()
	if stop != nil {
		g.t.Fatalf("INCONCLUSIVE: %v; partial results above", stop)
	}
	if g.ctx.Err() != nil {
		g.t.Fatal("INCONCLUSIVE: work deadline reached; five-minute cleanup reserve within two-hour whole-job cap; partial results above")
	}
	if err != nil {
		g.t.Fatalf("INCONCLUSIVE: benchmark stopped: %v", err)
	}
}

func (g *unreadListBenchGuard) check() {
	g.t.Helper()
	g.require(nil)
	g.require(g.usage(g.ctx))
}

// usage also runs on the admin observer during Stage 0 and long statements.
// Missing privileges or an unreadable directory fail closed, including WAL and
// every tablespace's live temp files. temp_bytes is cumulative, not disk usage.
func (g *unreadListBenchGuard) usage(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	var databases, wal, temp, other int64
	var complete bool
	err := g.observe.QueryRow(ctx, `WITH RECURSIVE dirs(path) AS (
 SELECT ''::text
 UNION ALL
 SELECT dirs.path||entry||'/' FROM dirs,
 LATERAL pg_ls_dir(CASE WHEN dirs.path='' THEN '.' ELSE dirs.path END) entry,
 LATERAL pg_stat_file(dirs.path||entry) info
 WHERE info.isdir AND NOT (dirs.path='' AND entry IN ('base','pg_wal','pg_tblspc'))
), databases AS MATERIALIZED (
 SELECT pg_database_size(oid) AS bytes FROM pg_database
)
SELECT (SELECT coalesce(sum(bytes),0)::bigint FROM databases),
 (SELECT coalesce(sum(size),0)::bigint FROM pg_ls_waldir()),
 (SELECT coalesce(sum(tmp.size),0)::bigint FROM pg_tablespace ts,
  LATERAL pg_ls_tmpdir(ts.oid) tmp WHERE ts.spcname<>'pg_global'),
 (SELECT coalesce(sum(info.size),0)::bigint FROM dirs,
  LATERAL pg_ls_dir(CASE WHEN dirs.path='' THEN '.' ELSE dirs.path END) entry,
  LATERAL pg_stat_file(dirs.path||entry) info WHERE NOT info.isdir),
 (SELECT coalesce(bool_and(bytes IS NOT NULL),false) FROM databases)`).
		Scan(&databases, &wal, &temp, &other, &complete)
	if err == nil && !complete {
		err = fmt.Errorf("a database size was unavailable; refusing partial accounting")
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if err != nil {
		g.stop = fmt.Errorf("cluster usage unreadable (all DBs/WAL/temp/other required): %w", err)
	} else {
		size := databases + wal + temp + other
		g.highWater = max(g.highWater, size)
		if time.Since(g.lastLog) >= 5*time.Second {
			g.t.Logf("cluster bytes: databases=%d WAL=%d live-temp=%d other=%d total=%d high-water=%d", databases, wal, temp, other, size, g.highWater)
			g.lastLog = time.Now()
		}
		// The volume is the hard cap; polling cannot bound WAL growth between
		// checks. Reserve 256 MB for the next batch, temp files and cleanup.
		if size >= 3_000_000_000-256_000_000 {
			g.stop = fmt.Errorf("cluster reached 3 GB stop threshold with 256 MB reserve: bytes=%d", size)
		}
	}
	if g.stop != nil {
		g.cancel()
	}
	return g.stop
}

func (g *unreadListBenchGuard) exec(sql string, args ...any) {
	g.t.Helper()
	g.check()
	_, err := g.pool.Exec(g.ctx, sql, args...)
	g.require(err)
	g.check()
}

func (g *unreadListBenchGuard) id(sql string, args ...any) kernel.ID {
	g.t.Helper()
	g.check()
	var id kernel.ID
	g.require(g.pool.QueryRow(g.ctx, sql, args...).Scan(&id))
	g.check()
	return id
}

func (g *unreadListBenchGuard) lines(sql string) {
	g.t.Helper()
	g.check()
	rows, err := g.pool.Query(g.ctx, sql)
	g.require(err)
	lines, err := pgx.CollectRows(rows, pgx.RowTo[string])
	g.require(err)
	g.t.Log(strings.Join(lines, "\n"))
	g.check()
}

type unreadListBenchScenario struct {
	name     string
	f        unreadListFixture
	caughtUp org.Membership
	topics   []kernel.ID
}

func (g *unreadListBenchGuard) seed(name string, channels, topics int, largest bool) unreadListBenchScenario {
	g.t.Helper()
	organization := g.id(`INSERT INTO organization (slug,name,event_seq) VALUES ($1,$1,1) RETURNING id`, name)
	member := func(handle string) org.Membership {
		account := g.id(`INSERT INTO account (email,display_name,password_hash) VALUES ($1,'Benchmark','$argon2id$x') RETURNING id`, name+"-"+handle+"@example.org")
		id := g.id(`INSERT INTO member (organization_id,account_id,role,handle,joined_event_seq) VALUES ($1,$2,'member',$3,1) RETURNING id`, organization, account, handle)
		return org.Membership{Organization: org.Organization{ID: organization}, Member: org.Member{ID: id, JoinedEventSeq: 1}}
	}
	scenario := unreadListBenchScenario{name: name, f: unreadListFixture{pool: g.pool, member: member("sparse")}, caughtUp: member("caught-up")}
	writer := member("writer").Member.ID
	for c := range channels {
		channel := g.id(`WITH created AS (
 INSERT INTO channel (organization_id,name,is_default) VALUES ($1,$2,$3) RETURNING *
), default_topic AS (
 INSERT INTO topic (id,organization_id,channel_id,is_default) SELECT default_topic_id,organization_id,id,true FROM created
) SELECT id FROM created`, organization, fmt.Sprintf("channel-%03d", c), c == 0)
		scenario.f.channels = append(scenario.f.channels, channel)
		g.exec(`INSERT INTO topic (organization_id,channel_id,name) SELECT $1,$2,'topic-'||lpad(i::text,3,'0') FROM generate_series(1,$3::int) i`, organization, channel, topics-1)
		g.check()
		rows, err := g.pool.Query(g.ctx, `SELECT id FROM topic WHERE organization_id=$1 AND channel_id=$2 ORDER BY is_default DESC,lower(name),id`, organization, channel)
		g.require(err)
		ids, err := pgx.CollectRows(rows, pgx.RowTo[kernel.ID])
		g.require(err)
		base := int64(c+1) * 1_000_000
		n, cursor := topics, base+int64(topics)*10
		heavy := largest && c == 0
		if heavy {
			// Keep the dense channel newest, so its topics reach the measured
			// first page as well as stressing the sidebar's selected channel.
			base = int64(channels+1) * 1_000_000
			n, cursor = 110_000, base+110_009
		}
		// Heavy channel: 10,000 read singletons, each separated by an unread
		// message, then 90,000 unread: exactly 100,000 unread in this channel.
		for lo := 1; lo <= n; lo += 1000 {
			g.exec(`INSERT INTO message (organization_id,channel_id,topic_id,member_id,body,event_seq)
 SELECT $1,$2,($3::uuid[])[1+(i-1)%$4],$5,'benchmark',CASE WHEN $6 THEN $7::bigint+i+9 ELSE $7::bigint+i*10 END
 FROM generate_series($8::int,$9::int) i`, organization, channel, ids, topics, writer, heavy, base, lo, min(lo+999, n))
		}
		for _, m := range []org.Membership{scenario.f.member, scenario.caughtUp} {
			g.exec(`INSERT INTO channel_read VALUES ($1,$2,$3)`, organization, channel, m.Member.ID)
			prefix := base + 100
			if heavy {
				prefix = base + 10
			}
			if m.Member.ID == scenario.caughtUp.Member.ID {
				prefix = cursor + 1
			}
			g.exec(`INSERT INTO read_range VALUES ($1,$2,$3,0,$4)`, organization, channel, m.Member.ID, prefix)
		}
		if heavy {
			for lo := 0; lo < 10_000; lo += 1000 {
				g.exec(`INSERT INTO read_range SELECT $1,$2,$3,$4::bigint+11+2*i,$4::bigint+12+2*i FROM generate_series($5::int,$6::int) i`, organization, channel, scenario.f.member.Member.ID, base, lo, lo+999)
			}
		} else {
			g.exec(`INSERT INTO read_range SELECT $1,$2,$3,$4::bigint+i*100+1,$4::bigint+(i+1)*100 FROM generate_series(1,$5::int-1) i`, organization, channel, scenario.f.member.Member.ID, base, topics/10)
		}
		g.exec(`INSERT INTO topic_read_floor (organization_id,channel_id,member_id,topic_id,floor_seq)
 SELECT $1,$2,$3,id,CASE WHEN $6 THEN $4::bigint+9 ELSE $4::bigint+i*10-CASE WHEN i%10=0 THEN 1 ELSE 0 END END
 FROM unnest($5::uuid[]) WITH ORDINALITY t(id,i)`, organization, channel, scenario.f.member.Member.ID, base, ids, heavy)
		g.exec(`UPDATE organization SET event_seq=greatest(event_seq,$2::bigint),event_log_boundary_seq=greatest(event_log_boundary_seq,$2::bigint) WHERE id=$1`, organization, cursor)
	}
	g.require(platform.InSnapshot(g.ctx, g.pool, func(s platform.Snapshot) error {
		var err error
		scenario.topics, err = unreadListBenchTopics(g.ctx, s, scenario.f)
		return err
	}))
	g.t.Logf("seed scenario=%s channels=%d topics/channel=%d sparse=every-tenth-topic heavy-channel=%t (100000 unread,10000 positive fragmented ranges); separate caught-up member", name, channels, topics, largest)
	g.check()
	return scenario
}
