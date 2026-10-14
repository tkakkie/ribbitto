package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/tkakkie/ribbitto/internal/conversation"
	"github.com/tkakkie/ribbitto/internal/conversation/conversationpg"
	"github.com/tkakkie/ribbitto/internal/conversation/conversationtest"
	"github.com/tkakkie/ribbitto/internal/identity"
	"github.com/tkakkie/ribbitto/internal/identity/identitytest"
	"github.com/tkakkie/ribbitto/internal/kernel"
	"github.com/tkakkie/ribbitto/internal/org"
	"github.com/tkakkie/ribbitto/internal/org/orgpg"
	"github.com/tkakkie/ribbitto/internal/org/orgtest"
	platform "github.com/tkakkie/ribbitto/internal/platform/postgres"
	"github.com/tkakkie/ribbitto/internal/platform/postgres/pgtest"
	"github.com/tkakkie/ribbitto/internal/unread"
	"github.com/tkakkie/ribbitto/internal/unread/unreadpg"
)

func witnessIn(ctx context.Context, s platform.Snapshot, m org.Membership, channel kernel.ID, channels, topics []kernel.ID) ([]conversation.WitnessRange, map[kernel.ID]int64, error) {
	state, err := unreadpg.PersistedIn(s).Read(ctx, unread.Scope{OrganizationID: m.Organization.ID, MemberID: m.Member.ID, ChannelID: channel}, channels, topics)
	ranges := make([]conversation.WitnessRange, len(state.Ranges))
	for i, r := range state.Ranges {
		ranges[i] = conversation.WitnessRange{ChannelID: r.ChannelID, Lo: r.Lo, Hi: r.Hi}
	}
	return ranges, state.Floors, err
}

func witnessRead(t *testing.T, r *conversation.Reader, m org.Membership, channel kernel.ID, selected *kernel.ID, kind string) (conversation.PageCounts, *conversation.SidebarWitness) {
	t.Helper()
	switch kind {
	case "page":
		p, err := r.Page(t.Context(), m, channel, selected, nil)
		feedRequire(t, err)
		return p.PageCounts, p.Witness
	case "members":
		p, err := r.Members(t.Context(), m, channel, nil)
		feedRequire(t, err)
		return p.PageCounts, p.Witness
	case "list":
		p, err := r.ChannelList(t.Context(), m, channel)
		feedRequire(t, err)
		return conversation.PageCounts{ChannelCounts: p.ChannelCounts, TopicCounts: p.TopicCounts}, p.Witness
	default:
		p, err := r.Sidebar(t.Context(), m, channel, selected)
		feedRequire(t, err)
		return conversation.PageCounts{ChannelCounts: p.ChannelCounts, TopicCounts: p.TopicCounts}, p.Witness
	}
}

type witnessQuery struct {
	SQL  string
	Args []json.RawMessage
}
type witnessTrace struct {
	t       *testing.T
	labels  map[string]string
	queries []witnessQuery
	hook    func(string)
}

func (w *witnessTrace) TraceQueryStart(ctx context.Context, _ *pgx.Conn, d pgx.TraceQueryStartData) context.Context {
	b, err := json.Marshal(d.Args)
	feedRequire(w.t, err)
	for id, label := range w.labels {
		b = []byte(strings.ReplaceAll(string(b), `"`+id+`"`, `"`+label+`"`))
	}
	q := witnessQuery{SQL: d.SQL}
	feedRequire(w.t, json.Unmarshal(b, &q.Args))
	w.queries = append(w.queries, q)
	if w.hook != nil {
		w.hook(d.SQL)
	}
	return ctx
}
func (*witnessTrace) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}

func witnessPool(t *testing.T, pool *pgxpool.Pool, trace *witnessTrace) *pgxpool.Pool {
	t.Helper()
	c := pool.Config()
	c.ConnConfig.Tracer = trace
	p, err := pgxpool.NewWithConfig(t.Context(), c)
	feedRequire(t, err)
	t.Cleanup(p.Close)
	return p
}

// Only the paired map-derived arguments may permute. Reject duplicate IDs rather
// than letting a map comparison silently lose a pair.
func witnessCanonical(queries []witnessQuery) bool {
	for i := range queries {
		q := &queries[i]
		q.Args = slices.Clone(q.Args)
		if !strings.HasPrefix(q.SQL, "-- name: CountTopicUnread :many\n") {
			continue
		}
		if len(q.Args) != 8 {
			return false
		}
		var ids, floors []json.RawMessage
		if json.Unmarshal(q.Args[6], &ids) != nil || json.Unmarshal(q.Args[7], &floors) != nil || len(ids) != len(floors) {
			return false
		}
		pairs := make([]string, len(ids))
		seen := map[string]bool{}
		for j, id := range ids {
			if seen[string(id)] {
				return false
			}
			seen[string(id)] = true
			pairs[j] = string(id) + ":" + string(floors[j])
		}
		slices.Sort(pairs)
		q.Args[6], _ = json.Marshal(pairs)
		q.Args[7] = nil
	}
	return true
}

func TestWitnessCanonicalComparison(t *testing.T) {
	baselineJSON := `[
		{"SQL":"begin", "Args":null},
		{"SQL":"-- name: CountTopicUnread :many\nSELECT paired", "Args":["org","channel",1,[[0,2]],"selected",true,["a","b"],[10,20]]},
		{"SQL":"-- name: Other :one\nSELECT exact", "Args":["member",["a","b"]]},
		{"SQL":"commit", "Args":null}
	]`
	fresh := func() []witnessQuery {
		var queries []witnessQuery
		feedRequire(t, json.Unmarshal([]byte(baselineJSON), &queries))
		return queries
	}
	args := func(statement, argument int, value string) func([]witnessQuery) []witnessQuery {
		return func(q []witnessQuery) []witnessQuery {
			q[statement].Args[argument] = json.RawMessage(value)
			return q
		}
	}
	pairs := func(ids, floors string) func([]witnessQuery) []witnessQuery {
		return func(q []witnessQuery) []witnessQuery {
			q[1].Args[6], q[1].Args[7] = json.RawMessage(ids), json.RawMessage(floors)
			return q
		}
	}
	type comparisonCase struct {
		name         string
		mutate       func([]witnessQuery) []witnessQuery
		valid, equal bool
	}
	cases := []comparisonCase{
		{"unchanged", func(q []witnessQuery) []witnessQuery { return q }, true, true},
		{"paired permutation", pairs(`["b","a"]`, `[20,10]`), true, true},
		{"swapped floors", pairs(`["a","b"]`, `[20,10]`), true, false},
		{"dropped pair", pairs(`["a"]`, `[10]`), true, false},
		{"duplicated pair", pairs(`["a","b","a"]`, `[10,20,10]`), false, false},
		{"unequal length fewer floors", pairs(`["a","b"]`, `[10]`), false, false},
		{"unequal length extra floor", pairs(`["a","b"]`, `[10,20,30]`), false, false},
		{"missing argument", func(q []witnessQuery) []witnessQuery { q[1].Args = q[1].Args[:7]; return q }, false, false},
		{"statement count", func(q []witnessQuery) []witnessQuery { return q[:3] }, true, false},
		{"transaction order", func(q []witnessQuery) []witnessQuery { q[0], q[3] = q[3], q[0]; return q }, true, false},
	}
	for statement, q := range fresh() {
		cases = append(cases, comparisonCase{fmt.Sprintf("SQL %d", statement), func(q []witnessQuery) []witnessQuery { q[statement].SQL += " changed"; return q }, true, false})
		for argument := range q.Args {
			if statement == 1 && argument >= 6 {
				continue
			}
			cases = append(cases, comparisonCase{fmt.Sprintf("argument %d/%d", statement, argument+1), args(statement, argument, `"changed"`), true, false})
		}
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			baseline, candidate := fresh(), tc.mutate(fresh())
			if !witnessCanonical(baseline) {
				t.Fatal("valid baseline rejected")
			}
			valid := witnessCanonical(candidate)
			if valid != tc.valid {
				t.Errorf("canonical validity = %v, want %v", valid, tc.valid)
			}
			if equal := valid && reflect.DeepEqual(baseline, candidate); equal != tc.equal {
				t.Errorf("canonical equality = %v, want %v", equal, tc.equal)
			}
		})
	}
}

func TestWitnessOffBaseline(t *testing.T) {
	pool := pgtest.New(t)
	f := conversationtest.OrganizationWithOwner(t, pool, "witness", "general")
	trace := &witnessTrace{t: t, labels: map[string]string{}}
	label := func(id kernel.ID, name string) {
		v, err := (pgtype.UUID{Bytes: id, Valid: true}).Value()
		feedRequire(t, err)
		trace.labels[v.(string)] = name
	}
	label(f.OrganizationID, "organization")
	label(f.MemberID, "member")
	label(f.AccountID, "account")
	label(f.Channel.ID, "channel")
	label(f.Channel.DefaultTopicID, "default")
	var selected kernel.ID
	for i := 1; i <= 50; i++ {
		selected = conversationtest.Topic(t, pool, f.OrganizationID, f.Channel.ID, fmt.Sprintf("topic%02d", i)).ID
		label(selected, fmt.Sprintf("topic%02d", i))
	}
	m, err := orgpg.NewAuthorizer(pool).Member(t.Context(), &identity.Account{ID: f.AccountID}, "witness")
	feedRequire(t, err)
	r := conversationpg.NewReader(witnessPool(t, pool, trace), lookupMembers, lookupAccounts, eventCursor, pageCountsIn)
	for _, view := range []*kernel.ID{nil, &selected} {
		witnessRead(t, r, m, f.Channel.ID, view, "sidebar")
		witnessRead(t, r, m, f.Channel.ID, view, "page")
	}
	witnessRead(t, r, m, f.Channel.ID, nil, "members")
	witnessRead(t, r, m, f.Channel.ID, nil, "list")
	b, err := os.ReadFile("testdata/witness-off.json")
	feedRequire(t, err)
	if fmt.Sprintf("%x", sha256.Sum256(b)) != "447ea459c29ac4cbe20fa40180ed732fa55a8a20164aa10be5c832e6e8055c36" {
		t.Fatal("sidebar-contract baseline pin changed")
	}
	var lines string
	feedRequire(t, json.Unmarshal(b, &lines))
	var baseline []witnessQuery
	for _, line := range strings.Split(strings.TrimSpace(lines), "\n") {
		var q witnessQuery
		feedRequire(t, json.Unmarshal([]byte(line), &q))
		baseline = append(baseline, q)
	}
	if !witnessCanonical(baseline) || !witnessCanonical(trace.queries) || !reflect.DeepEqual(baseline, trace.queries) {
		t.Fatal("off SQL/arguments/transaction order differs from pinned sidebar-contract baseline")
	}
}

func TestWitnessSnapshot(t *testing.T) {
	for _, kind := range []string{"sidebar", "page", "members", "list"} {
		for _, gate := range []string{"GetEventSeq", "WitnessRanges", "WitnessFloors"} {
			t.Run(kind+"/"+gate, func(t *testing.T) {
				pool := pgtest.New(t)
				f := conversationtest.OrganizationWithOwner(t, pool, "witness", "general")
				m, err := orgpg.NewAuthorizer(pool).Member(t.Context(), &identity.Account{ID: f.AccountID}, "witness")
				feedRequire(t, err)
				post := conversationpg.NewPosting(pool, postingSequence, postingEvents, nil)
				_, err = post.PostToTopic(t.Context(), m, f.Channel.ID, nil, "old")
				feedRequire(t, err)
				scope := unread.Scope{OrganizationID: f.OrganizationID, MemberID: f.MemberID, ChannelID: f.Channel.ID}
				_, err = pool.Exec(t.Context(), "INSERT INTO channel_read VALUES ($1,$2,$3)", scope.OrganizationID, scope.ChannelID, scope.MemberID)
				feedRequire(t, err)
				_, err = pool.Exec(t.Context(), "INSERT INTO read_range VALUES ($1,$2,$3,0,2);", scope.OrganizationID, scope.ChannelID, scope.MemberID)
				feedRequire(t, err)
				_, err = pool.Exec(t.Context(), "INSERT INTO topic_read_floor (organization_id,channel_id,member_id,topic_id,floor_seq) VALUES ($1,$2,$3,$4,1)", scope.OrganizationID, scope.ChannelID, scope.MemberID, f.Channel.DefaultTopicID)
				feedRequire(t, err)
				trace := &witnessTrace{t: t}
				committed := false
				trace.hook = func(sql string) {
					if committed || !strings.HasPrefix(sql, "-- name: "+gate+" ") {
						return
					}
					committed = true
					p, err := post.PostToTopic(t.Context(), m, f.Channel.ID, nil, "new")
					feedRequire(t, err)
					reads := unreadpg.NewReading(pool, newFeedWriter(), newTopicWriter(), nil)
					feedRequire(t, reads.Feed(t.Context(), scope, m.Member.JoinedEventSeq, p.EventSeq))
					feedRequire(t, reads.Topic(t.Context(), unread.TopicScope{Scope: scope, TopicID: f.Channel.DefaultTopicID}, m.Member.JoinedEventSeq, p.EventSeq))
				}
				r := conversationpg.NewReader(witnessPool(t, pool, trace), lookupMembers, lookupAccounts, eventCursor, pageCountsIn).WithWitness(witnessIn)
				counts, w := witnessRead(t, r, m, f.Channel.ID, &f.Channel.DefaultTopicID, kind)
				if !committed || w == nil || counts.ChannelCounts[f.Channel.ID] != 1 || counts.TopicCounts[f.Channel.DefaultTopicID] != 1 || w.EventSeq != 2 || w.JoinedEventSeq != 1 || !reflect.DeepEqual(w.Ranges, []conversation.WitnessRange{{ChannelID: f.Channel.ID, Lo: 0, Hi: 2}}) || w.Floors[f.Channel.DefaultTopicID] != 1 {
					t.Fatalf("witness mixed snapshots: counts=%v witness=%+v", counts, w)
				}
				counts, w = witnessRead(t, r, m, f.Channel.ID, &f.Channel.DefaultTopicID, kind)
				if w == nil || counts.ChannelCounts[f.Channel.ID] != 0 || counts.TopicCounts[f.Channel.DefaultTopicID] != 0 || w.EventSeq != 3 || len(w.Ranges) != 1 || w.Ranges[0].Hi != 4 || w.Floors[f.Channel.DefaultTopicID] != 3 {
					t.Fatalf("next snapshot missing commits: counts=%v witness=%+v", counts, w)
				}
			})
		}
	}
}

func TestWitnessOwnership(t *testing.T) {
	for _, kind := range []string{"sidebar", "page"} {
		for _, selection := range []string{"selected", "nil"} {
			t.Run(kind+"/"+selection, func(t *testing.T) {
				pool := pgtest.New(t)
				f := conversationtest.OrganizationWithOwner(t, pool, "witness", "general")
				m, err := orgpg.NewAuthorizer(pool).Member(t.Context(), &identity.Account{ID: f.AccountID}, "witness")
				feedRequire(t, err)
				selectedID := f.Channel.DefaultTopicID
				var selected *kernel.ID
				if selection == "selected" {
					selected = &selectedID
				}
				wantRange := conversation.WitnessRange{ChannelID: f.Channel.ID, Lo: 0, Hi: 2}
				ranges := []conversation.WitnessRange{wantRange}
				floors := map[kernel.ID]int64{f.Channel.DefaultTopicID: 1}
				r := conversationpg.NewReader(pool, lookupMembers, lookupAccounts, eventCursor, pageCountsIn).WithWitness(func(context.Context, platform.Snapshot, org.Membership, kernel.ID, []kernel.ID, []kernel.ID) ([]conversation.WitnessRange, map[kernel.ID]int64, error) {
					return ranges, floors, nil
				})
				_, w := witnessRead(t, r, m, f.Channel.ID, selected, kind)
				if w == nil {
					t.Fatal("missing witness")
				}
				selectedID = kernel.ID{255}
				ranges[0] = conversation.WitnessRange{ChannelID: kernel.ID{255}, Lo: 9, Hi: 10}
				floors[f.Channel.DefaultTopicID] = 9
				floors[kernel.ID{255}] = 10
				if selected != nil {
					if w.Selected == nil {
						t.Error("missing selected witness ID")
					} else if *w.Selected != f.Channel.DefaultTopicID {
						t.Errorf("witness selected ID changed after caller mutation: got %x, want %x", *w.Selected, f.Channel.DefaultTopicID)
					}
				} else if w.Selected != nil {
					t.Errorf("nil selection became %x", *w.Selected)
				}
				if !reflect.DeepEqual(w.Ranges, []conversation.WitnessRange{wantRange}) {
					t.Errorf("witness ranges changed after callback storage reuse: got %+v, want %+v", w.Ranges, []conversation.WitnessRange{wantRange})
				}
				if !reflect.DeepEqual(w.Floors, map[kernel.ID]int64{f.Channel.DefaultTopicID: 1}) {
					t.Errorf("witness floors changed after callback storage reuse: got %v, want %v", w.Floors, map[kernel.ID]int64{f.Channel.DefaultTopicID: 1})
				}
			})
		}
	}
}

func TestWitnessScopes(t *testing.T) {
	pool := pgtest.New(t)
	f := conversationtest.OrganizationWithOwner(t, pool, "witness", "general")
	m, err := orgpg.NewAuthorizer(pool).Member(t.Context(), &identity.Account{ID: f.AccountID}, "witness")
	feedRequire(t, err)
	r := conversationpg.NewReader(pool, lookupMembers, lookupAccounts, eventCursor, pageCountsIn).WithWitness(witnessIn)
	_, w := witnessRead(t, r, m, f.Channel.ID, nil, "sidebar")
	if w == nil || len(w.Ranges) != 0 || len(w.Floors) != 0 || w.JoinedEventSeq != 1 {
		t.Fatal("empty persisted state fabricated")
	}
	var selected, excluded kernel.ID
	for i := 1; i <= 51; i++ {
		id := conversationtest.Topic(t, pool, f.OrganizationID, f.Channel.ID, fmt.Sprintf("topic%02d", i)).ID
		if i == 50 {
			selected = id
		}
		excluded = id
	}
	other := conversationtest.Channel(t, pool, f.OrganizationID, "other", false)
	member := orgtest.Member(t, pool, f.OrganizationID, identitytest.Account(t, pool, "other@example.org", "other"), org.RoleMember, "other", 1)
	foreign := conversationtest.OrganizationWithOwner(t, pool, "foreign", "foreign")
	for _, s := range []unread.Scope{{OrganizationID: f.OrganizationID, MemberID: f.MemberID, ChannelID: f.Channel.ID}, {OrganizationID: f.OrganizationID, MemberID: member, ChannelID: f.Channel.ID}, {OrganizationID: f.OrganizationID, MemberID: f.MemberID, ChannelID: other.ID}, {OrganizationID: foreign.OrganizationID, MemberID: foreign.MemberID, ChannelID: foreign.Channel.ID}} {
		_, err = pool.Exec(t.Context(), "INSERT INTO channel_read VALUES ($1,$2,$3)", s.OrganizationID, s.ChannelID, s.MemberID)
		feedRequire(t, err)
		_, err = pool.Exec(t.Context(), "INSERT INTO read_range SELECT $1,$2,$3,2*n,2*n+1 FROM generate_series(0,102) n", s.OrganizationID, s.ChannelID, s.MemberID)
		feedRequire(t, err)
	}
	for _, row := range []struct{ organization, channel, member, topic kernel.ID }{{f.OrganizationID, f.Channel.ID, f.MemberID, selected}, {f.OrganizationID, f.Channel.ID, f.MemberID, excluded}, {f.OrganizationID, f.Channel.ID, member, selected}, {f.OrganizationID, other.ID, f.MemberID, other.DefaultTopicID}, {foreign.OrganizationID, foreign.Channel.ID, foreign.MemberID, foreign.Channel.DefaultTopicID}} {
		_, err = pool.Exec(t.Context(), "INSERT INTO topic_read_floor (organization_id,channel_id,member_id,topic_id,floor_seq) VALUES ($1,$2,$3,$4,9)", row.organization, row.channel, row.member, row.topic)
		feedRequire(t, err)
	}
	_, w = witnessRead(t, r, m, f.Channel.ID, &selected, "sidebar")
	if w == nil || w.Version != 1 || w.OrganizationID != f.OrganizationID || w.MemberID != f.MemberID || w.ChannelID != f.Channel.ID || w.Selected == nil || *w.Selected != selected || len(w.Ranges) != 206 || !reflect.DeepEqual(w.Floors, map[kernel.ID]int64{selected: 9}) {
		t.Fatalf("witness scope/completeness: %+v", w)
	}
	for i, span := range w.Ranges {
		if span.Lo != int64(2*(i%103)) || span.Hi != span.Lo+1 {
			t.Fatal("persisted range values lost")
		}
	}
	scope := unread.Scope{OrganizationID: f.OrganizationID, MemberID: f.MemberID, ChannelID: f.Channel.ID}
	for _, tc := range []struct {
		name             string
		scope            unread.Scope
		channels, topics []kernel.ID
		ranges, floors   int
	}{
		{"channel and dedup", scope, []kernel.ID{f.Channel.ID, f.Channel.ID}, []kernel.ID{selected, selected}, 103, 1},
		{"organization", unread.Scope{OrganizationID: foreign.OrganizationID, MemberID: f.MemberID, ChannelID: f.Channel.ID}, []kernel.ID{f.Channel.ID}, []kernel.ID{selected}, 0, 0},
		{"member", unread.Scope{OrganizationID: f.OrganizationID, MemberID: kernel.ID{255}, ChannelID: f.Channel.ID}, []kernel.ID{f.Channel.ID}, []kernel.ID{selected}, 0, 0},
		{"floor channel", scope, []kernel.ID{}, []kernel.ID{other.DefaultTopicID}, 0, 0},
		{"floor topic", scope, []kernel.ID{}, []kernel.ID{f.Channel.DefaultTopicID}, 0, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			feedRequire(t, platform.InSnapshot(t.Context(), pool, func(s platform.Snapshot) error {
				state, err := unreadpg.PersistedIn(s).Read(t.Context(), tc.scope, tc.channels, tc.topics)
				if len(state.Ranges) != tc.ranges || len(state.Floors) != tc.floors {
					t.Fatalf("persisted scope %s: ranges=%d floors=%d", tc.name, len(state.Ranges), len(state.Floors))
				}
				return err
			}))
		})
	}
	for channel, topic := range map[kernel.ID]kernel.ID{foreign.Channel.ID: foreign.Channel.DefaultTopicID, f.Channel.ID: other.DefaultTopicID} {
		p, err := r.Sidebar(t.Context(), m, channel, &topic)
		if (!errors.Is(err, conversation.ErrChannelNotFound) && !errors.Is(err, conversation.ErrTopicNotFound)) || p.Witness != nil {
			t.Fatal("rejected access captured witness")
		}
	}
}

func TestWitnessErrors(t *testing.T) {
	pool := pgtest.New(t)
	f := conversationtest.OrganizationWithOwner(t, pool, "witness", "general")
	m, err := orgpg.NewAuthorizer(pool).Member(t.Context(), &identity.Account{ID: f.AccountID}, "witness")
	feedRequire(t, err)
	r := conversationpg.NewReader(pool, lookupMembers, lookupAccounts, eventCursor, pageCountsIn).WithWitness(func(ctx context.Context, s platform.Snapshot, _ org.Membership, _ kernel.ID, _, _ []kernel.ID) ([]conversation.WitnessRange, map[kernel.ID]int64, error) {
		return []conversation.WitnessRange{{Hi: 999}}, nil, errors.New("private capture detail")
	})
	for _, kind := range []string{"sidebar", "page", "members", "list"} {
		counts, w := witnessRead(t, r, m, f.Channel.ID, nil, kind)
		if w != nil || counts.ChannelCounts[f.Channel.ID] != 0 {
			t.Fatal("capture error escaped or partial witness retained")
		}
	}
}
