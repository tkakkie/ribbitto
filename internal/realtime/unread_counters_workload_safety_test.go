package realtime_test

import (
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/tkakkie/ribbitto/internal/identity"
	"github.com/tkakkie/ribbitto/internal/kernel"
	"github.com/tkakkie/ribbitto/internal/org"
)

func TestUnreadCountersSchedule(t *testing.T) {
	for _, rate := range []int{5, 20} {
		slots, err := counterSchedule(rate, 30*time.Second)
		if err != nil {
			t.Fatal(err)
		}
		counts := map[string]int{}
		mix := [2]int{}
		periods := map[string]time.Duration{"post": time.Second / time.Duration(rate), "read": 10 * time.Millisecond, "move": 10 * time.Second, "join": 5 * time.Second, "detect": 5 * time.Second, "repair": 30 * time.Second}
		for i, s := range slots {
			if s.at != time.Duration(s.index)*periods[s.kind] || i > 0 && s.at < slots[i-1].at {
				t.Fatalf("slot time/order: %+v", s)
			}
			counts[s.kind]++
			if s.kind == "read" {
				group := 0
				if s.topic {
					group = 1
				}
				mix[group]++
				if s.at != time.Duration(s.index)*10*time.Millisecond || s.member != s.index || s.round != 0 || s.topic != (s.index%2 == 1) {
					t.Fatalf("read mix/target: %+v", s)
				}
			}
			if s.kind == "move" && (s.cohort != s.index || s.at != time.Duration(s.index)*10*time.Second) {
				t.Fatalf("move rotation: %+v", s)
			}
		}
		want := map[string]int{"post": 30 * rate, "read": 3000, "move": 3, "join": 6, "detect": 6, "repair": 1}
		if !reflect.DeepEqual(counts, want) || mix != [2]int{1500, 1500} {
			t.Fatalf("fixed counts/mix rate=%d: %v %v", rate, counts, mix)
		}
		// Each arm independently obtains the plan; exact slots and stable ties match.
		other, _ := counterSchedule(rate, 30*time.Second)
		if !reflect.DeepEqual(slots, other) {
			t.Fatal("design schedules differ")
		}
		if slots[0].kind != "post" || slots[1].kind != "read" || slots[2].kind != "move" || slots[3].kind != "join" || slots[4].kind != "detect" || slots[5].kind != "repair" {
			t.Fatal("stable tie order changed")
		}
	}
	slots, _ := counterSchedule(20, 100020*time.Millisecond)
	for _, s := range slots {
		if s.kind == "read" && (s.member != s.index%5000 || s.round != s.index/5000 || s.topic != (s.index%2 == 1)) {
			t.Fatalf("read rotation/advance: %+v", s)
		}
	}
	edge, _ := counterSchedule(5, time.Nanosecond)
	if len(edge) != 6 {
		t.Fatal("fractional first slot lost")
	}
	for _, bad := range []struct {
		rate   int
		window time.Duration
	}{{0, time.Second}, {10, time.Second}, {5, 0}} {
		if _, err := counterSchedule(bad.rate, bad.window); err == nil {
			t.Fatal("invalid schedule accepted")
		}
	}
}

func TestUnreadCountersWorkloadDispatch(t *testing.T) {
	for _, counters := range []bool{false, true} {
		t.Run(fmt.Sprint(counters), func(t *testing.T) {
			h := newCounterMatrix(t)
			// Bulk fixture metadata is untraced; no 5000-stream workload is run here.
			rows := counterQuery(t, h.pool, `WITH accounts AS (
 INSERT INTO account(email,display_name,password_hash) SELECT 'load'||s||'@example.org','Load','$argon2id$x' FROM generate_series(1,4999) s RETURNING id,email
 ) INSERT INTO member(organization_id,account_id,role,joined_event_seq,handle)
 SELECT $1,id,'member',1,split_part(email,'@',1) FROM accounts RETURNING id`, h.members[0].Organization.ID)
			ids, err := pgx.CollectRows(rows, pgx.RowTo[kernel.ID])
			if err != nil {
				t.Fatal(err)
			}
			for _, id := range ids {
				h.members = append(h.members, org.Membership{Organization: h.members[0].Organization, Member: org.Member{ID: id, JoinedEventSeq: 1}})
			}
			w := counterWorkloadFixture(t, h, 100020*time.Millisecond)
			var history int
			if err := h.pool.QueryRow(t.Context(), `SELECT count(*) FROM message WHERE organization_id=$1 AND channel_id=$2`, h.members[0].Organization.ID, w.channel).Scan(&history); err != nil || history != 6 {
				t.Fatalf("advancing read history: got %d want 6: %v", history, err)
			}
			for i, want := range []int{100, 1000, 5000} {
				if w.cohorts[i].members != want || len(w.cohorts[i].messages) != 4 {
					t.Fatalf("fixed cohort %d: got %d members/%d messages want %d/4", i, w.cohorts[i].members, len(w.cohorts[i].messages), want)
				}
			}
			pool, _ := counterWorkPools(t, h.pool, counters)
			hasher, err := identity.NewHasher()
			if err != nil {
				t.Fatal(err)
			}
			w.bind(pool, counters, hasher)
			w.now = func() time.Time { return time.Unix(0, 0) }
			slots, _ := counterSchedule(5, 100020*time.Millisecond)
			for _, s := range slots {
				if s.kind == "post" && s.index > 0 || s.kind == "read" && s.index > 3 && s.index%5000 > 1 || s.kind == "move" && s.index > 5 || (s.kind == "join" || s.kind == "detect" || s.kind == "repair") && s.index > 0 {
					continue
				}
				n, err := w.dispatch(t.Context(), s)
				if err != nil || n.failures != 0 {
					t.Fatalf("dispatch %s: %v failures=%d", s.kind, err, n.failures)
				}
				application := counters || s.kind != "detect" && s.kind != "repair"
				controls := int64(2)
				if !counters && (s.kind == "post" || s.kind == "read" && s.index > 0 && s.index < 4) {
					controls = 4 // Current writes create missing lock rows under a savepoint.
				}
				if application && (n.statements == 0 || n.controls != controls || len(n.transactions) != 1) || !application && (n.statements != 0 || n.controls != 0 || len(n.transactions) != 0) {
					t.Fatalf("dispatch account %s: %d/%d/%v", s.kind, n.statements, n.controls, n.transactions)
				}
				if s.kind == "move" {
					c := w.cohorts[s.cohort]
					want := [3]int64{}
					if counters {
						want = [3]int64{int64(c.members), int64(c.members * 3 / 4), int64(c.members * 3 / 2)}
					}
					if n.moveLocks != want {
						t.Fatalf("fixed move fan-out: got %v want %v", n.moveLocks, want)
					}
					var distribution [4]int
					for i := range c.members {
						var read int
						if err := h.pool.QueryRow(t.Context(), `SELECT count(*) FROM message m WHERE organization_id=$1 AND channel_id=$2 AND event_seq=ANY($4) AND EXISTS(SELECT 1 FROM read_range r WHERE r.organization_id=$1 AND r.channel_id=$2 AND r.member_id=$3 AND r.lo<=m.event_seq AND m.event_seq<r.hi)`, h.members[0].Organization.ID, c.channel, h.members[i].Member.ID, c.sequences).Scan(&read); err != nil {
							t.Fatal(err)
						}
						expected := []int{0, 4, 2, 2}[i%4]
						if read != expected {
							t.Fatalf("move read state member=%d got=%d want=%d", i, read, expected)
						}
						distribution[i%4]++
					}
					if distribution != [4]int{c.members / 4, c.members / 4, c.members / 4, c.members / 4} {
						t.Fatal("move distribution lost")
					}
					if counters {
						truth := counterLoad(t, h.pool, h.members[0].Organization.ID)
						tx, err := h.pool.Begin(t.Context())
						if err != nil {
							t.Fatal(err)
						}
						for i := range 4 {
							counterSQLModel(t, tx, counterView{h.members[i], c.channel, c.destination}, truth)
						}
						if err := tx.Rollback(t.Context()); err != nil {
							t.Fatal(err)
						}
					}
				}
			}
			for i, offsets := range [][]int64{{0, 1, 2, 3, 4}, {1, 3, 5}} {
				want := make([]int64, len(offsets))
				for j, offset := range offsets {
					want[j] = w.first + offset
				}
				var read []int64
				rows := counterQuery(t, h.pool, `SELECT event_seq FROM message m WHERE organization_id=$1 AND channel_id=$2 AND event_seq BETWEEN $4 AND $4+5 AND EXISTS(SELECT 1 FROM read_range r WHERE r.organization_id=$1 AND r.channel_id=$2 AND r.member_id=$3 AND r.lo<=m.event_seq AND m.event_seq<r.hi) ORDER BY event_seq`, h.members[0].Organization.ID, w.channel, w.members[i].Member.ID, w.first)
				read, err = pgx.CollectRows(rows, pgx.RowTo[int64])
				if err != nil || !reflect.DeepEqual(read, want) {
					t.Fatalf("scheduled read scope member=%d: %v %v", i, read, err)
				}
			}
			var joined, bundles int
			if err := h.pool.QueryRow(t.Context(), `SELECT count(*),count(b.member_id) FROM member m LEFT JOIN counter_bundle b ON b.organization_id=m.organization_id AND b.member_id=m.id WHERE m.organization_id=$1 AND handle='loadjoin0' AND joined_event_seq>$2`, h.members[0].Organization.ID, w.first+1).Scan(&joined, &bundles); err != nil || joined != 1 || bundles != 0 {
				t.Fatalf("scheduled lazy join: %d/%d %v", joined, bundles, err)
			}
		})
	}
}
