package main

import (
	"context"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/tkakkie/ribbitto/internal/compose"
	"github.com/tkakkie/ribbitto/internal/conversation"
	"github.com/tkakkie/ribbitto/internal/conversation/conversationpg"
	"github.com/tkakkie/ribbitto/internal/kernel"
	"github.com/tkakkie/ribbitto/internal/org"
	platform "github.com/tkakkie/ribbitto/internal/platform/postgres"
	"github.com/tkakkie/ribbitto/internal/realtime"
	"github.com/tkakkie/ribbitto/internal/unread"
	"github.com/tkakkie/ribbitto/internal/unread/unreadpg"
)

type readingDBRunner struct {
	pool *pgxpool.Pool
	gate func()
}

func (r readingDBRunner) InTx(ctx context.Context, fn func(platform.Tx) error) error {
	return platform.InTx(ctx, r.pool, func(tx platform.Tx) error {
		if err := fn(tx); err != nil {
			return err
		}
		if r.gate != nil {
			r.gate()
		}
		return nil
	})
}

type publicationSpy func(kernel.ID, kernel.ID)

func (f publicationSpy) ReadChanged(org, member kernel.ID) { f(org, member) }

func TestReadPublicationConcurrent(t *testing.T) {
	for _, kinds := range [][2]string{{"feed", "feed"}, {"topic", "topic"}, {"feed", "topic"}, {"topic", "feed"}} {
		for _, exists := range []bool{false, true} {
			t.Run(fmt.Sprintf("%v existing=%t", kinds, exists), func(t *testing.T) {
				pool, scope := feedFixture(t)
				var topic kernel.ID
				feedRequire(t, pool.QueryRow(t.Context(), "SELECT default_topic_id FROM channel WHERE id=$1", scope.ChannelID).Scan(&topic))
				if exists {
					feedRequire(t, unreadpg.NewReading(pool, compose.NewFeedWriter(), compose.NewTopicWriter(), nil).Feed(t.Context(), scope, 1, 1))
				}
				ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
				defer cancel()
				locked, release := make(chan struct{}), make(chan struct{})
				unblock := sync.OnceFunc(func() { close(release) })
				var calls atomic.Int32
				publisher := publicationSpy(func(org, member kernel.ID) {
					if org != scope.OrganizationID || member != scope.MemberID {
						t.Error("wrong publication scope")
					}
					// A separate connection must see the read, proving publication
					// did not escape from inside the transaction.
					var read bool
					if err := pool.QueryRow(ctx, "SELECT EXISTS (SELECT FROM read_range WHERE organization_id=$1 AND member_id=$2 AND channel_id=$3 AND lo <= 2 AND hi > 2)", scope.OrganizationID, scope.MemberID, scope.ChannelID).Scan(&read); err != nil || !read {
						t.Errorf("publication preceded committed read: read=%t error=%v", read, err)
					}
					calls.Add(1)
				})
				first := unread.NewReading(readingDBRunner{pool, func() {
					close(locked)
					select {
					case <-release:
					case <-ctx.Done():
					}
				}}, compose.NewFeedWriter(), compose.NewTopicWriter(), publisher)
				second := unreadpg.NewReading(pool, compose.NewFeedWriter(), compose.NewTopicWriter(), publisher)
				read := func(r *unread.Reading, kind string) error {
					if kind == "feed" {
						return r.Feed(ctx, scope, 1, 2)
					}
					return r.Topic(ctx, unread.TopicScope{Scope: scope, TopicID: topic}, 1, 2)
				}
				a, b := make(chan error, 1), make(chan error, 1)
				go func() { a <- read(first, kinds[0]) }()
				defer func() {
					unblock()
					feedRequire(t, <-a)
					if got := calls.Load(); got != 1 {
						t.Errorf("same unread message published %d times, want 1", got)
					}
				}()
				select {
				case <-locked:
				case <-ctx.Done():
					t.Fatal(ctx.Err())
				}
				go func() { b <- read(second, kinds[1]) }()
				for {
					var blocked bool
					feedRequire(t, pool.QueryRow(ctx, "SELECT EXISTS (SELECT FROM pg_stat_activity WHERE datname=current_database() AND wait_event_type='Lock' AND cardinality(pg_blocking_pids(pid)) > 0)").Scan(&blocked))
					if blocked {
						break
					}
					select {
					case err := <-b:
						t.Fatalf("second read did not wait: %v", err)
					default:
					}
				}
				unblock()
				feedRequire(t, <-b)
			})
		}
	}
}

// Guard the composition boundary too: a private publication in either adapter
// bypasses Reading's commit contract and duplicates the durable notification.
func TestReadAdaptersDoNotPublish(t *testing.T) {
	for _, file := range []struct {
		name     string
		expected []string
	}{
		{"../../internal/compose/read_adapters.go", []string{"PostReads", "Read", "NewFeedWriter", "NewTopicWriter"}},
		{"../../internal/compose/branch_reads.go", []string{"BranchReads", "Merge"}},
	} {
		missing, violations, err := checkReadAdapterPublication(file.name, nil, file.expected)
		feedRequire(t, err)
		for _, name := range missing {
			t.Errorf("%s: missing expected function %s", file.name, name)
		}
		for _, violation := range violations {
			t.Errorf("%s: %s", file.name, violation)
		}
	}
}

func checkReadAdapterPublication(name string, src any, expected []string) (missing, violations []string, err error) {
	file, err := parser.ParseFile(token.NewFileSet(), name, src, 0)
	if err != nil {
		return nil, nil, fmt.Errorf("parsing read adapter %s: %w", name, err)
	}
	found := make(map[string]bool, len(expected))
	for _, want := range expected {
		found[want] = false
	}
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok {
			continue
		}
		if _, ok := found[fn.Name.Name]; !ok {
			continue
		}
		found[fn.Name.Name] = true
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			if selector, ok := n.(*ast.SelectorExpr); ok && (selector.Sel.Name == "ReadChanged" || selector.Sel.Name == "NewReading") {
				violations = append(violations, fmt.Sprintf("%s calls reading publication via %s", fn.Name.Name, selector.Sel.Name))
			}
			return true
		})
	}
	// A renamed or moved adapter must not silently remove guard coverage.
	for _, want := range expected {
		if !found[want] {
			missing = append(missing, want)
		}
	}
	return missing, violations, nil
}

func TestReadAdapterPublicationGuard(t *testing.T) {
	for _, tt := range []struct {
		name       string
		source     string
		missing    []string
		violations []string
	}{
		{name: "all present", source: "func Read() {}; func (adapter) Merge() {}"},
		{name: "renamed", source: "func ReadX() {}; func (adapter) Merge() {}", missing: []string{"Read"}},
		{name: "no matches", source: "func Other() {}", missing: []string{"Read", "Merge"}},
		{
			name: "publication", source: "func Read() { x.ReadChanged(); x.NewReading() }; func (adapter) Merge() {}",
			violations: []string{"Read calls reading publication via ReadChanged", "Read calls reading publication via NewReading"},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			missing, violations, err := checkReadAdapterPublication("adapter.go", "package adapter; type adapter struct{}; "+tt.source, []string{"Read", "Merge"})
			feedRequire(t, err)
			if !reflect.DeepEqual(missing, tt.missing) || !reflect.DeepEqual(violations, tt.violations) {
				t.Errorf("missing=%v, violations=%v; want missing=%v, violations=%v", missing, violations, tt.missing, tt.violations)
			}
		})
	}
}

type readAdapterEvents struct {
	conversation.EventAppender
	steps *[]string
}

func (e readAdapterEvents) Append(ctx context.Context, org kernel.ID, seq int64, kind realtime.EventKind, audience *kernel.ID, data []byte) error {
	*e.steps = append(*e.steps, string(kind))
	return e.EventAppender.Append(ctx, org, seq, kind, audience, data)
}

type readAdapterNotifier func(kernel.ID, int64)

func (f readAdapterNotifier) Raise(org kernel.ID, seq int64) { f(org, seq) }

func TestReadAdaptersCommitAndNotify(t *testing.T) {
	for _, kind := range []string{"feed", "topic", "branch"} {
		t.Run(kind, func(t *testing.T) {
			pool, scope := feedFixture(t)
			var topic, message kernel.ID
			feedRequire(t, pool.QueryRow(t.Context(), "SELECT topic_id,id FROM message WHERE organization_id=$1 AND event_seq=2", scope.OrganizationID).Scan(&topic, &message))
			m := org.Membership{Organization: org.Organization{ID: scope.OrganizationID}, Member: org.Member{ID: scope.MemberID, JoinedEventSeq: 1}}
			var steps []string
			sequences := func(tx platform.Tx) conversation.EventSequence {
				steps = append(steps, "sequence")
				return compose.PostingSequence(tx)
			}
			events := func(tx platform.Tx) conversation.EventAppender {
				return readAdapterEvents{compose.PostingEvents(tx), &steps}
			}
			notifier := readAdapterNotifier(func(org kernel.ID, seq int64) {
				steps = append(steps, "notify")
				if org != scope.OrganizationID {
					t.Error("wrong durable notification scope")
				}
				acceptanceCount(t, pool, 1, "SELECT count(*) FROM event_log WHERE organization_id=$1 AND seq=$2 AND kind='message.posted'", org, seq)
				acceptanceCount(t, pool, 1, "SELECT count(*) FROM read_range WHERE organization_id=$1 AND member_id=$2 AND lo <= $3 AND hi > $3", org, scope.MemberID, seq)
			})
			want := []string{"sequence", "read", "message.posted", "notify"}
			if kind == "branch" {
				reads := func(tx platform.Tx) conversation.ReadRangeWriter {
					steps = append(steps, "read")
					return compose.BranchReads(tx)
				}
				_, err := conversationpg.NewBrancher(pool, sequences, events, reads, notifier).Branch(t.Context(), m, scope.ChannelID, conversation.Branch{From: topic, NewName: "branch", Messages: []kernel.ID{message}}, func(conversation.Topic) string { return "notice" })
				feedRequire(t, err)
				want = []string{"sequence", "messages.moved", "read", "message.posted", "notify"}
			} else {
				reads := func(tx platform.Tx) conversation.PostReadWriter {
					steps = append(steps, "read")
					return compose.PostReads(tx)
				}
				var target *kernel.ID
				if kind == "topic" {
					target = &topic
				}
				cursor := int64(2)
				_, err := conversationpg.NewPagePosting(pool, sequences, events, reads, compose.LookupMembers, notifier).Send(t.Context(), m, scope.ChannelID, conversation.Send{Topic: target, Body: "post", Cursor: &cursor})
				feedRequire(t, err)
			}
			if !reflect.DeepEqual(steps, want) {
				t.Fatalf("transaction/notification order=%v, want %v", steps, want)
			}
		})
	}
}
