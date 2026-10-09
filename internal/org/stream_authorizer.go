package org

import (
	"container/list"
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/tkakkie/ribbitto/internal/identity"
	"github.com/tkakkie/ribbitto/internal/kernel"
	"github.com/tkakkie/ribbitto/internal/realtime"
)

// DefaultAuthorizationCapacity bounds cached stream allows per process.
const DefaultAuthorizationCapacity = 10000

type allowKey struct {
	account kernel.ID
	slug    string
}
type allowEntry struct {
	key        allowKey
	membership Membership
}

// Non-zero size keeps concurrently live tokens distinct.
type checkToken struct{ marker byte }

// streamAuthorization allows only memberships whose epoch is at least an epoch
// read begun after the check starts. The installation-wide sequence preserves
// this ordering across organisation recreation. Latest-check tokens only keep
// the newest result in the cache; safety depends on the epoch comparison.
type streamAuthorization struct {
	parent   context.Context
	epochs   *realtime.Cache[kernel.ID, int64]
	mu       sync.Mutex
	capacity int
	entries  map[allowKey]*list.Element
	order    *list.List
	latest   map[allowKey]*checkToken
	hits     atomic.Uint64
	reads    atomic.Uint64
	checks   atomic.Uint64
}

// AuthorizationStats counts cached allows used and actual epoch reads, including
// failed reads. These process-wide counters support stream-cost measurements.
type AuthorizationStats struct {
	Checks     uint64
	CacheHits  uint64
	EpochReads uint64
}

// Stats returns a concurrent-safe snapshot of stream authorization counters.
func (a *Authorizer) Stats() AuthorizationStats {
	hits := a.stream.hits.Load()
	reads := a.stream.reads.Load()
	return AuthorizationStats{
		Checks:     a.stream.checks.Load(),
		CacheHits:  hits,
		EpochReads: reads,
	}
}

// NewCachedAuthorizer shares fresh epoch reads under parent and holds at most
// capacity stream allows. Non-positive capacity uses DefaultAuthorizationCapacity.
// Member and HomeSlug always read the store directly.
func NewCachedAuthorizer(parent context.Context, store MembershipStore, capacity int) *Authorizer {
	if capacity <= 0 {
		capacity = DefaultAuthorizationCapacity
	}
	return &Authorizer{store: store, stream: &streamAuthorization{
		parent: parent,
		epochs: realtime.NewCache[kernel.ID, int64](parent, 1, realtime.DefaultCacheLoads, 0, 5*time.Second,
			func(kernel.ID, int64) bool { return false }, time.Now),
		capacity: capacity,
		entries:  make(map[allowKey]*list.Element),
		order:    list.New(),
		latest:   make(map[allowKey]*checkToken),
	}}
}

func (a *Authorizer) streamMembership(
	ctx context.Context, account kernel.ID, slug string, organization kernel.ID,
) (Membership, error) {
	s := a.stream
	s.checks.Add(1)
	key := allowKey{account, slug}
	// Register before the fresh read: completion order must not let an older
	// check replace a newer result, including a deny for a missing organisation.
	token := &checkToken{}
	s.mu.Lock()
	s.latest[key] = token
	elem := s.entries[key]
	var cached Membership
	if elem != nil {
		cached = elem.Value.(allowEntry).membership
	}
	s.mu.Unlock()
	hit := false
	defer func() {
		s.mu.Lock()
		// The fresh epoch proves the snapshot even if it was evicted meanwhile.
		// Touch only the same resident entry; never resurrect or touch a replacement.
		if hit && s.entries[key] == elem {
			s.order.MoveToFront(elem)
		}
		if s.latest[key] == token {
			delete(s.latest, key)
		}
		s.mu.Unlock()
	}()
	epoch, err := s.epochs.Get(ctx, organization, func(ctx context.Context) (int64, error) {
		s.reads.Add(1)
		epoch, err := a.store.AccessEpoch(ctx, organization)
		if errors.Is(err, ErrNotFound) {
			return 0, nil
		}
		return epoch, err
	})
	if err != nil {
		return Membership{}, fmt.Errorf("reading access epoch: %w", err)
	}
	// Shutdown may begin after the cache accepts a successful result. Reject
	// it before it can authorize or populate an entry.
	for _, check := range []context.Context{s.parent, ctx} {
		if err := context.Cause(check); err != nil {
			return Membership{}, fmt.Errorf("reading access epoch after cancellation: %w", err)
		}
	}
	if epoch == 0 {
		return Membership{}, ErrNotFound
	}
	if elem != nil && cached.Organization.ID == organization && cached.AccessEpoch >= epoch {
		hit = true
		s.hits.Add(1)
		return cached, nil
	}
	s.mu.Lock()
	if elem := s.entries[key]; elem != nil {
		m := elem.Value.(allowEntry).membership
		if m.Organization.ID == organization && m.AccessEpoch >= epoch {
			s.order.MoveToFront(elem)
			s.hits.Add(1)
			s.mu.Unlock()
			return m, nil
		}
		s.order.Remove(elem)
		delete(s.entries, key)
	}
	s.mu.Unlock()
	m, err := a.Member(ctx, &identity.Account{ID: account}, slug)
	s.mu.Lock()
	defer s.mu.Unlock()
	// A store may return a successful result even when its context has ended.
	for _, check := range []context.Context{s.parent, ctx} {
		if err := context.Cause(check); err != nil {
			return Membership{}, fmt.Errorf("reading stream membership: %w", err)
		}
	}
	if s.latest[key] == token {
		if err == nil && m.Organization.ID == organization && m.AccessEpoch >= epoch {
			if elem := s.entries[key]; elem != nil {
				if elem.Value.(allowEntry).membership.AccessEpoch > m.AccessEpoch {
					return m, err
				}
				s.order.Remove(elem)
			}
			s.entries[key] = s.order.PushFront(allowEntry{key, m})
			if s.order.Len() > s.capacity {
				oldest := s.order.Back()
				delete(s.entries, oldest.Value.(allowEntry).key)
				s.order.Remove(oldest)
			}
		}
	}
	if err == nil && m.AccessEpoch < epoch {
		return Membership{}, ErrNotFound
	}
	return m, err
}
