package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/tkakkie/ribbitto/internal/conversation/conversationpg"
	"github.com/tkakkie/ribbitto/internal/conversation/conversationtest"
	"github.com/tkakkie/ribbitto/internal/kernel"
	"github.com/tkakkie/ribbitto/internal/org"
	"github.com/tkakkie/ribbitto/internal/platform/postgres/pgtest"
	"github.com/tkakkie/ribbitto/internal/unread"
	"github.com/tkakkie/ribbitto/internal/unread/unreadpg"
	"github.com/tkakkie/ribbitto/internal/web/middleware"
	"github.com/tkakkie/ribbitto/internal/web/view"
)

type readAdmissionKey struct{}
type readAdmissionEndKey struct{}

type readAdmissionRound struct {
	ready, release chan struct{}
	active, peak   atomic.Int64
}

type readAdmissionAttempt struct {
	round       *readAdmissionRound
	began, held bool
	downstream  []string
	response    *httptest.ResponseRecorder
}

type readAdmissionTrace struct{}

func (readAdmissionTrace) TraceQueryStart(ctx context.Context, _ *pgx.Conn, d pgx.TraceQueryStartData) context.Context {
	a, ok := ctx.Value(readAdmissionKey{}).(*readAdmissionAttempt)
	if !ok {
		return ctx
	}
	// Session and membership resolution necessarily precede admission. Every
	// other statement, including lookups and BEGIN, is downstream work.
	if !strings.HasPrefix(d.SQL, "-- name: GetSessionByTokenHash ") && !strings.HasPrefix(d.SQL, "-- name: GetMembershipBySlug ") {
		a.downstream = append(a.downstream, strings.Split(d.SQL, "\n")[0])
	}
	if d.SQL == "begin" {
		a.began = true
		n := a.round.active.Add(1)
		for peak := a.round.peak.Load(); n > peak; peak = a.round.peak.Load() {
			if a.round.peak.CompareAndSwap(peak, n) {
				break
			}
		}
	}
	if strings.HasPrefix(d.SQL, "-- name: LockChannelRead ") && !a.held {
		a.held = true
		// BEGIN has succeeded. No read can finish until all attempts have
		// reached the lock query or returned. This uses at most 16 connections,
		// even without a concurrency ceiling; the controller holds no connection.
		a.round.ready <- struct{}{}
		select {
		case <-a.round.release:
		case <-ctx.Done():
		}
	}
	return context.WithValue(ctx, readAdmissionEndKey{}, d.SQL == "commit" || d.SQL == "rollback")
}

func (readAdmissionTrace) TraceQueryEnd(ctx context.Context, _ *pgx.Conn, _ pgx.TraceQueryEndData) {
	if a, ok := ctx.Value(readAdmissionKey{}).(*readAdmissionAttempt); ok && ctx.Value(readAdmissionEndKey{}) == true {
		a.round.active.Add(-1)
	}
}

// Four rounds (initial attempt plus three Retry-After retries), with 4 successes
// per round, bound convergence to 16+12+8+4 = 40 attempts without competing load.
func TestReadAdmissionConvergence(t *testing.T) {
	for _, newer := range []bool{false, true} {
		t.Run(fmt.Sprintf("newer=%t", newer), func(t *testing.T) {
			pool := pgtest.New(t)
			ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
			defer cancel()
			f := conversationtest.OrganizationWithOwner(t, pool, "admission", "general")
			member := org.Membership{Organization: org.Organization{ID: f.OrganizationID}, Member: org.Member{ID: f.MemberID}}
			posting := conversationpg.NewPosting(pool, postingSequence, postingEvents, nil)
			type tab struct {
				scope         unread.Scope
				topic, target kernel.ID
				path          string
				cursor        int64
			}
			tabs := make([]tab, 16)
			var cursor int64
			for i := range tabs {
				channel := conversationtest.Channel(t, pool, f.OrganizationID, fmt.Sprintf("tab-%d", i), false)
				topic := conversationtest.Topic(t, pool, f.OrganizationID, channel.ID, "target")
				p := &tabs[i]
				p.scope = unread.Scope{OrganizationID: f.OrganizationID, ChannelID: channel.ID, MemberID: f.MemberID}
				p.target = topic.ID
				p.path = view.ChannelURL("admission", channel.ID)
				if i%2 == 1 {
					p.topic = topic.ID
					p.path = view.ConversationURL("admission", channel.ID, &topic.ID)
				}
				// Topic reads leave the interleaved default-topic message unread;
				// feed reads cover both topics, so these are distinct writes.
				for _, id := range []kernel.ID{topic.ID, channel.DefaultTopicID, topic.ID} {
					message, err := posting.PostToTopic(ctx, member, channel.ID, &id, "unread")
					feedRequire(t, err)
					cursor = message.EventSeq
				}
			}
			// Row-level auditing detects duplicate effective writes even when a
			// duplicate request would leave identical final ranges and floors.
			_, err := pool.Exec(ctx, `
CREATE TABLE admission_write (relation text, operation text, data jsonb);
CREATE FUNCTION admission_audit() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  INSERT INTO admission_write VALUES (TG_TABLE_NAME, TG_OP, to_jsonb(CASE WHEN TG_OP='DELETE' THEN OLD ELSE NEW END));
  RETURN NULL;
END $$;
CREATE TRIGGER admission_audit AFTER INSERT OR UPDATE OR DELETE ON channel_read FOR EACH ROW EXECUTE FUNCTION admission_audit();
CREATE TRIGGER admission_audit AFTER INSERT OR UPDATE OR DELETE ON read_range FOR EACH ROW EXECUTE FUNCTION admission_audit();
CREATE TRIGGER admission_audit AFTER INSERT OR UPDATE OR DELETE ON topic_read_floor FOR EACH ROW EXECUTE FUNCTION admission_audit();`)
			feedRequire(t, err)
			traced := readTracedPool(t, pool, readAdmissionTrace{})
			clock := time.Unix(1, 0)
			handler, sessions, err := buildHandler(ctx, traced, handlerConfig{readLimits: middleware.NewReadRateLimiter(func() time.Time { return clock })})
			feedRequire(t, err)
			token, _, err := sessions.Create(ctx, f.AccountID)
			feedRequire(t, err)
			request := func(ctx context.Context, method, path string, cursor int64) *httptest.ResponseRecorder {
				form := url.Values{"cursor": {fmt.Sprint(cursor)}}
				r := httptest.NewRequest(method, path, strings.NewReader(form.Encode())).WithContext(ctx)
				r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
				r.Header.Set("HX-Request", "true")
				r.AddCookie(&http.Cookie{Name: middleware.SessionCookie, Value: token})
				w := httptest.NewRecorder()
				handler.ServeHTTP(w, r)
				return w
			}
			pending := make([]int, len(tabs))
			for i := range tabs {
				pending[i], tabs[i].cursor = i, cursor
				w := request(ctx, "GET", tabs[i].path, 0)
				if w.Code != 200 || !strings.Contains(w.Body.String(), fmt.Sprintf(`name="cursor" value="%d"`, cursor)) {
					t.Fatalf("tab %d page: status=%d missing cursor %d", i, w.Code, cursor)
				}
			}
			for _, table := range []string{"channel_read", "read_range", "topic_read_floor", "admission_write"} {
				acceptanceCount(t, pool, 0, "SELECT count(*) FROM "+table)
			}
			attempts, rounds, peak := 0, 0, int64(0)
			for len(pending) > 0 && rounds < 4 {
				rounds++
				g := &readAdmissionRound{ready: make(chan struct{}, 16), release: make(chan struct{})}
				start := make(chan struct{})
				results := make([]*readAdmissionAttempt, len(pending))
				var workers sync.WaitGroup
				for j, i := range pending {
					a := &readAdmissionAttempt{round: g}
					results[j] = a
					workers.Go(func() {
						<-start
						a.response = request(context.WithValue(ctx, readAdmissionKey{}, a), "POST", tabs[i].path+"/read", tabs[i].cursor)
						if !a.held {
							g.ready <- struct{}{}
						}
					})
				}
				close(start)
				for range pending {
					select {
					case <-g.ready:
					case <-ctx.Done():
						close(g.release)
						workers.Wait()
						t.Fatal("round did not reach overlap barrier: ", ctx.Err())
					}
				}
				measured := g.active.Load()
				peak = max(peak, g.peak.Load())
				close(g.release)
				workers.Wait()
				if measured != 4 || peak > 4 || g.active.Load() != 0 {
					t.Fatalf("round %d concurrency: held=%d peak=%d remaining=%d; want 4, <=4, 0", rounds, measured, peak, g.active.Load())
				}
				next := make([]int, 0, len(pending))
				var retryAfter time.Duration
				for j, a := range results {
					attempts++
					i, w := pending[j], a.response
					switch w.Code {
					case http.StatusNoContent:
						if !a.began {
							t.Fatalf("tab %d succeeded without a transaction", i)
						}
					case http.StatusTooManyRequests:
						if w.Header().Get("Retry-After") != "1" {
							t.Fatalf("tab %d refusal Retry-After=%q; want 1", i, w.Header().Get("Retry-After"))
						}
						if len(a.downstream) != 0 {
							t.Fatalf("tab %d refusal did downstream database work: %v", i, a.downstream)
						}
						delay, err := time.ParseDuration(w.Header().Get("Retry-After") + "s")
						feedRequire(t, err)
						retryAfter = max(retryAfter, delay)
						next = append(next, i)
					default:
						t.Fatalf("tab %d round %d: status=%d body=%s", i, rounds, w.Code, w.Body.String())
					}
				}
				if len(pending)-len(next) != 4 {
					t.Fatalf("round %d successes=%d; want 4", rounds, len(pending)-len(next))
				}
				if newer && rounds == 1 {
					for _, i := range next {
						p := &tabs[i]
						message, err := posting.PostToTopic(ctx, member, p.scope.ChannelID, &p.target, "arrived during Retry-After")
						feedRequire(t, err)
						p.cursor = message.EventSeq
					}
				}
				// Each refused tab schedules its own retry for the same one-second
				// deadline. Advance shared time once; send the newest shown cursor,
				// with no GET, visibility event or other trigger between attempts.
				clock = clock.Add(retryAfter)
				pending = next
			}
			if len(pending) != 0 || rounds != 4 || attempts != 40 || peak != 4 {
				t.Fatalf("convergence: pending=%d rounds=%d attempts=%d peak=%d; want 0, 4, 40, 4", len(pending), rounds, attempts, peak)
			}
			for i, p := range tabs {
				if p.topic == (kernel.ID{}) {
					feedRanges(t, pool, p.scope, []unread.Range{{Lo: 0, Hi: p.cursor + 1}})
				} else {
					var floor int64
					feedRequire(t, pool.QueryRow(ctx, "SELECT floor_seq FROM topic_read_floor WHERE organization_id=$1 AND member_id=$2 AND topic_id=$3", f.OrganizationID, f.MemberID, p.topic).Scan(&floor))
					if floor != p.cursor {
						t.Fatalf("tab %d stored floor=%d; want newest cursor %d", i, floor, p.cursor)
					}
				}
			}
			actual := readAdmissionDatabase(t, pool)
			// Replay exactly one unthrottled read per tab from empty state. Compare
			// every row and the multiset of actual INSERT/UPDATE/DELETE row effects.
			_, err = pool.Exec(ctx, "DELETE FROM channel_read; DELETE FROM admission_write")
			feedRequire(t, err)
			reading := unreadpg.NewReading(pool, newFeedWriter(), newTopicWriter(), nil)
			for _, p := range tabs {
				if p.topic == (kernel.ID{}) {
					err = reading.Feed(ctx, p.scope, 1, p.cursor)
				} else {
					err = reading.Topic(ctx, unread.TopicScope{Scope: p.scope, TopicID: p.topic}, 1, p.cursor)
				}
				feedRequire(t, err)
			}
			if want := readAdmissionDatabase(t, pool); actual != want {
				t.Fatalf("database state/write audit differs from 16 unthrottled reads:\nactual=%v\nwant=%v", actual, want)
			}
			t.Logf("16 tabs: measured peak=%d rounds=%d attempts=%d refusals=%d newer=%t", peak, rounds, attempts, attempts-len(tabs), newer)
		})
	}
}

func readAdmissionDatabase(t *testing.T, pool *pgxpool.Pool) [2]string {
	t.Helper()
	var result [2]string
	feedRequire(t, pool.QueryRow(t.Context(), `
SELECT coalesce(jsonb_agg(data ORDER BY data), '[]'::jsonb)::text FROM (
  SELECT jsonb_build_array('channel_read', to_jsonb(r)) data FROM channel_read r
  UNION ALL SELECT jsonb_build_array('read_range', to_jsonb(r)) FROM read_range r
  UNION ALL SELECT jsonb_build_array('topic_read_floor', to_jsonb(r)) FROM topic_read_floor r
) state`).Scan(&result[0]))
	feedRequire(t, pool.QueryRow(t.Context(), `
SELECT coalesce(jsonb_agg(to_jsonb(w) ORDER BY relation, operation, data), '[]'::jsonb)::text
FROM admission_write w`).Scan(&result[1]))
	return result
}
