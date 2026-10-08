package org

import (
	"context"
	"errors"
	"runtime"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/tkakkie/ribbitto/internal/kernel"
	"github.com/tkakkie/ribbitto/internal/realtime"
)

type streamStore struct {
	MembershipStore
	epoch  func(context.Context, kernel.ID) (int64, error)
	member func(context.Context, kernel.ID, string) (Membership, error)
}

func (s streamStore) AccessEpoch(ctx context.Context, id kernel.ID) (int64, error) {
	return s.epoch(ctx, id)
}
func (s streamStore) Membership(ctx context.Context, id kernel.ID, slug string) (Membership, error) {
	return s.member(ctx, id, slug)
}

func TestCachedAllow(t *testing.T) {
	broken := errors.New("read failed")
	scenarios := []string{
		"unchanged", "revoked", "epoch-error", "member-error", "missing", "older",
		"organization", "audience", "account", "slug", "capacity",
	}
	for _, scenario := range scenarios {
		t.Run(scenario, func(t *testing.T) {
			m := Membership{
				Organization: Organization{ID: kernel.ID{1}, Slug: "acme"},
				Member:       Member{ID: kernel.ID{2}},
				AccessEpoch:  1,
			}
			epoch, queries := int64(1), 0
			var epochErr, memberErr error
			store := streamStore{
				epoch: func(context.Context, kernel.ID) (int64, error) { return epoch, epochErr },
				member: func(_ context.Context, id kernel.ID, slug string) (Membership, error) {
					queries++
					if id != (kernel.ID{3}) || slug != "acme" {
						return Membership{}, ErrNotFound
					}
					return m, memberErr
				}}
			a := NewCachedAuthorizer(t.Context(), store, 1)
			event := realtime.Event{OrganizationID: m.Organization.ID}
			check := func(account kernel.ID, slug string, want bool, wantErr error) {
				t.Helper()
				got, err := a.MayReceive(t.Context(), account, slug, event)
				if got != want || !errors.Is(err, wantErr) {
					t.Fatalf("allow=%v error=%v, want %v %v", got, err, want, wantErr)
				}
			}
			check(kernel.ID{3}, "acme", true, nil)
			want, wantErr, count := false, error(nil), 1
			account, slug := kernel.ID{3}, "acme"
			switch scenario {
			case "unchanged":
				want = true
			case "revoked":
				epoch, memberErr, count = 2, ErrNotFound, 2
			case "epoch-error":
				epochErr, wantErr = broken, broken
			case "member-error":
				epoch, memberErr, wantErr, count = 2, broken, broken, 2
			case "missing":
				epochErr = ErrNotFound
			case "older":
				epoch, count = 2, 2
			case "organization":
				event.OrganizationID = kernel.ID{4}
				count = 2
			case "audience":
				other := kernel.ID{5}
				event.AudienceMemberID = &other
			case "account":
				account, count = kernel.ID{6}, 2
			case "slug":
				slug, count = "renamed", 2
			case "capacity":
				// A second valid membership evicts the first at capacity one.
				a.store = streamStore{
					epoch: store.epoch,
					member: func(context.Context, kernel.ID, string) (Membership, error) {
						queries++
						return m, nil
					},
				}
				check(kernel.ID{6}, "acme", true, nil)
				want, count = true, 3
			}
			check(account, slug, want, wantErr)
			if scenario == "revoked" || scenario == "member-error" {
				check(account, slug, want, wantErr)
				count++ // Denies and failures are never cached.
			}
			if queries != count {
				t.Fatalf("membership queries=%d, want %d", queries, count)
			}
			if scenario == "unchanged" && a.Stats() != (AuthorizationStats{Checks: 2, CacheHits: 1, EpochReads: 2}) {
				t.Fatalf("stats=%+v", a.Stats())
			}
		})
	}
}

func TestFreshSharedEpoch(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	var reads atomic.Int64
	store := streamStore{epoch: func(ctx context.Context, _ kernel.ID) (int64, error) {
		if reads.Add(1) == 1 {
			close(entered)
			select {
			case <-release:
			case <-ctx.Done():
				return 0, ctx.Err()
			}
			return 1, nil
		}
		return 2, nil
	}, member: func(context.Context, kernel.ID, string) (Membership, error) {
		return Membership{Organization: Organization{ID: kernel.ID{1}, Slug: "acme"}, AccessEpoch: 1}, nil
	}}
	a := NewCachedAuthorizer(t.Context(), store, 1)
	type result struct {
		allow bool
		err   error
	}
	done := make(chan result, 2)
	check := func() {
		allow, err := a.MayReceive(t.Context(), kernel.ID{3}, "acme", realtime.Event{OrganizationID: kernel.ID{1}})
		done <- result{allow, err}
	}
	go check()
	waitAuthorizationGate(t, entered)
	go check()
	deadline := time.After(5 * time.Second)
	for a.stream.epochs.Waiting(kernel.ID{1}) != 2 {
		select {
		case <-deadline:
			t.Fatal("second check did not join")
		default:
			runtime.Gosched()
		}
	}
	close(release)
	allows := 0
	for range 2 {
		r := <-done
		if r.err != nil {
			t.Fatal(r.err)
		}
		if r.allow {
			allows++
		}
	}
	if allows != 1 {
		t.Fatalf("overlapping allows=%d, want only the first check", allows)
	}
	check()
	if r := <-done; r.err != nil || r.allow {
		t.Fatalf("sequential check=%+v", r)
	}
	if reads.Load() != 3 {
		t.Fatalf("epoch reads=%d, want 3", reads.Load())
	}
}

func TestStaleLoadCannotReplaceNewResult(t *testing.T) {
	for _, result := range []string{"allow", "deny", "error", "missing-organization"} {
		t.Run(result, func(t *testing.T) {
			entered, release := make(chan struct{}), make(chan struct{})
			var reads, queries atomic.Int64
			m := Membership{Organization: Organization{ID: kernel.ID{1}, Slug: "acme"}, AccessEpoch: 1}
			broken := errors.New("membership read failed")
			store := streamStore{epoch: func(context.Context, kernel.ID) (int64, error) {
				epoch := reads.Add(1)
				if result == "missing-organization" && epoch > 1 {
					return 0, ErrNotFound
				}
				return epoch, nil
			}, member: func(ctx context.Context, _ kernel.ID, _ string) (Membership, error) {
				loaded := m
				if queries.Add(1) == 1 {
					close(entered)
					select {
					case <-release:
					case <-ctx.Done():
						return Membership{}, ctx.Err()
					}
				} else {
					switch result {
					case "deny":
						return Membership{}, ErrNotFound
					case "error":
						return Membership{}, broken
					}
					loaded.AccessEpoch = 3
				}
				return loaded, nil
			}}
			a := NewCachedAuthorizer(t.Context(), store, 1)
			event := realtime.Event{OrganizationID: m.Organization.ID}
			done := make(chan error, 1)
			go func() { _, err := a.MayReceive(t.Context(), kernel.ID{3}, "acme", event); done <- err }()
			waitAuthorizationGate(t, entered)
			wantErr := error(nil)
			if result == "error" {
				wantErr = broken
			}
			allow, err := a.MayReceive(t.Context(), kernel.ID{3}, "acme", event)
			if allow != (result == "allow") || !errors.Is(err, wantErr) {
				t.Fatalf("new load=%v, %v", allow, err)
			}
			close(release)
			if err := <-done; err != nil {
				t.Fatal(err)
			}
			a.stream.mu.Lock()
			entry := a.stream.entries[allowKey{kernel.ID{3}, "acme"}]
			a.stream.mu.Unlock()
			if result != "allow" {
				if entry != nil {
					t.Fatal("old allow stored after newer deny/error")
				}
			} else {
				allow, err := a.MayReceive(t.Context(), kernel.ID{3}, "acme", event)
				if !allow || err != nil || queries.Load() != 2 {
					t.Fatalf("later check=%v, %v, queries=%d", allow, err, queries.Load())
				}
			}
		})
	}
}

func waitAuthorizationGate(t *testing.T, entered <-chan struct{}) {
	t.Helper()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("authorization gate not reached")
	}
}

func TestShutdownRacingSharedEpoch(t *testing.T) {
	for _, warm := range []bool{false, true} {
		t.Run(map[bool]string{false: "cold", true: "warm"}[warm], func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				parent, cancel := context.WithCancel(t.Context())
				defer cancel()
				m := Membership{Organization: Organization{ID: kernel.ID{1}, Slug: "acme"}, AccessEpoch: 1}
				var queries atomic.Int64
				store := streamStore{
					epoch: func(context.Context, kernel.ID) (int64, error) { return 1, nil },
					member: func(context.Context, kernel.ID, string) (Membership, error) {
						queries.Add(1)
						return m, nil
					},
				}
				a := NewCachedAuthorizer(parent, store, 1)
				event := realtime.Event{OrganizationID: m.Organization.ID}
				if warm {
					allow, err := a.MayReceive(t.Context(), kernel.ID{3}, "acme", event)
					if !allow || err != nil {
						t.Fatalf("warming allow=%v, error=%v", allow, err)
					}
				}
				beforeQueries := queries.Load()
				loaded, releaseRead := make(chan struct{}), make(chan struct{})
				rejected, releaseKeep := make(chan struct{}), make(chan struct{})
				store.epoch = func(context.Context, kernel.ID) (int64, error) {
					close(loaded)
					<-releaseRead
					return 1, nil
				}
				a.store = store
				// Gate after run has collected a successful result, before load
				// decides whether joiners need a second read. Keep still rejects
				// every epoch, exactly as in the production authorizer.
				a.stream.epochs = realtime.NewCache[kernel.ID, int64](
					parent, 1, realtime.DefaultCacheLoads, 0, 5*time.Second,
					func(kernel.ID, int64) bool {
						close(rejected)
						<-releaseKeep
						return false
					}, time.Now,
				)
				type result struct {
					role  string
					allow bool
					err   error
				}
				done := make(chan result, 2)
				check := func(role string) {
					allow, err := a.MayReceive(t.Context(), kernel.ID{3}, "acme", event)
					done <- result{role, allow, err}
				}
				go check("starter")
				<-loaded
				go check("joiner")
				synctest.Wait()
				if waiting := a.stream.epochs.Waiting(m.Organization.ID); waiting != 2 {
					t.Fatalf("epoch waiters=%d, want starter and joiner", waiting)
				}
				close(releaseRead)
				<-rejected
				cancel()
				// Ensure the parent's AfterFunc has cancelled the load context
				// before releasing keep; no scheduling race or sleeps remain.
				synctest.Wait()
				close(releaseKeep)
				results := make(map[string]result)
				for range 2 {
					r := <-done
					results[r.role] = r
				}
				for _, role := range []string{"joiner", "starter"} {
					r := results[role]
					if r.allow || !errors.Is(r.err, context.Canceled) {
						t.Fatalf("shutdown %s allow=%v, error=%v", role, r.allow, r.err)
					}
				}
				if queries.Load() != beforeQueries || a.Stats().CacheHits != 0 {
					t.Fatalf("cancelled epoch used: queries=%d, stats=%+v", queries.Load(), a.Stats())
				}
				if len(a.stream.entries) != int(beforeQueries) || len(a.stream.latest) != 0 {
					t.Fatal("cancelled result changed cached allows or left a check token")
				}
				check("later")
				r := <-done
				if r.allow || !errors.Is(r.err, context.Canceled) || a.Stats().EpochReads != uint64(beforeQueries+1) {
					t.Fatalf("later check=%+v, stats=%+v", r, a.Stats())
				}
			})
		})
	}
}

func TestCancelledMembershipResultIsNotCached(t *testing.T) {
	parent, cancel := context.WithCancel(t.Context())
	defer cancel()
	store := streamStore{
		epoch: func(context.Context, kernel.ID) (int64, error) { return 1, nil },
		member: func(context.Context, kernel.ID, string) (Membership, error) {
			cancel()
			return Membership{Organization: Organization{ID: kernel.ID{1}}, AccessEpoch: 1}, nil
		},
	}
	a := NewCachedAuthorizer(parent, store, 1)
	allow, err := a.MayReceive(t.Context(), kernel.ID{3}, "acme", realtime.Event{OrganizationID: kernel.ID{1}})
	if allow || !errors.Is(err, context.Canceled) || len(a.stream.entries) != 0 {
		t.Fatalf("cancelled membership allow=%v, error=%v, entries=%d", allow, err, len(a.stream.entries))
	}
}
