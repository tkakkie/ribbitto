package orgpg_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/tkakkie/ribbitto/internal/identity/identitytest"
	"github.com/tkakkie/ribbitto/internal/kernel"
	"github.com/tkakkie/ribbitto/internal/org"
	"github.com/tkakkie/ribbitto/internal/org/orgpg"
	"github.com/tkakkie/ribbitto/internal/org/orgtest"
	"github.com/tkakkie/ribbitto/internal/platform/postgres/pgtest"
	"github.com/tkakkie/ribbitto/internal/realtime"
)

type deliveryFixture struct {
	pool   *pgxpool.Pool
	sub    realtime.Subscription
	member kernel.ID
}

func streamFixture(t *testing.T) deliveryFixture {
	t.Helper()
	pool := pgtest.New(t)
	id := orgtest.Organization(t, pool, "acme", "Acme", 0)
	account := identitytest.Account(t, pool, "alice@example.org", "Alice")
	member := orgtest.Member(t, pool, id, account, org.RoleMember, "alice", 1)
	return deliveryFixture{
		pool:   pool,
		sub:    realtime.Subscription{Interests: []realtime.Interest{realtime.InterestMessages}, Organization: id, OrganizationSlug: "acme", Account: account},
		member: member,
	}
}

// The batch is already read, including across TRUNCATE. Only authorization
// reads use PostgreSQL here; the HTTP acceptance test also uses the real log.
type deliveryBatch struct {
	events []realtime.Event
	ended  error
}

func (b deliveryBatch) EventsAfter(_ context.Context, _ kernel.ID, after int64, _ int) ([]realtime.Event, error) {
	if after >= int64(len(b.events)) {
		return nil, b.ended
	}
	return b.events[after:], nil
}

type deliveryRender func(context.Context, realtime.Event) (realtime.Outgoing, error)

func (r deliveryRender) Render(ctx context.Context, e realtime.Event) (realtime.Outgoing, error) {
	return r(ctx, e)
}

type deliverySender struct{ ids []int64 }

func (s *deliverySender) Send(_ context.Context, out realtime.Outgoing) error {
	s.ids = append(s.ids, out.ID)
	return nil
}
func (*deliverySender) Heartbeat(context.Context) error { return nil }

type deliveryResult struct {
	cursor int64
	err    error
	sent   []int64
}

func deliver(t *testing.T, f deliveryFixture, a *org.Authorizer, n int, render deliveryRender) <-chan deliveryResult {
	t.Helper()
	result := make(chan deliveryResult, 1)
	events := make([]realtime.Event, n)
	for i := range events {
		events[i] = realtime.Event{OrganizationID: f.sub.Organization, Seq: int64(i + 1), Kind: org.KindJoined}
	}
	ended := errors.New("batch drained")
	go func() {
		sender := &deliverySender{}
		stream := realtime.Stream{
			Hub:        realtime.NewHub(),
			Events:     deliveryBatch{events, ended},
			Authorizer: a,
			Renderer:   render,
			BatchSize:  n,
		}
		cursor, err := stream.Run(t.Context(), f.sub, 0, sender)
		if errors.Is(err, ended) {
			err = nil
		}
		result <- deliveryResult{cursor, err, sender.ids}
	}()
	return result
}
func rendered(_ context.Context, e realtime.Event) (realtime.Outgoing, error) {
	return realtime.Outgoing{ID: e.Seq}, nil
}
func awaitDelivery(t *testing.T, result <-chan deliveryResult, cursor int64, sent int) {
	t.Helper()
	select {
	case r := <-result:
		if r.err != nil || r.cursor != cursor || len(r.sent) != sent {
			t.Fatalf("delivery=%+v, want cursor=%d sends=%d", r, cursor, sent)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("delivery did not finish")
	}
}
func awaitGate(t *testing.T, gate <-chan struct{}) {
	t.Helper()
	select {
	case <-gate:
	case <-time.After(5 * time.Second):
		t.Fatal("gate not reached")
	}
}

func TestStreamCachedAllowRevocation(t *testing.T) {
	for _, change := range []string{"delete", "rename", "recreate"} {
		t.Run(change, func(t *testing.T) {
			f := streamFixture(t)
			a := orgpg.NewCachedAuthorizer(t.Context(), f.pool, 10)
			entered, release := make(chan struct{}), make(chan struct{})
			// Event 1 is actually delivered before event 2 pauses after rendering.
			result := deliver(t, f, a, 3, func(ctx context.Context, e realtime.Event) (realtime.Outgoing, error) {
				out, err := rendered(ctx, e)
				if e.Seq == 2 {
					close(entered)
					select {
					case <-release:
					case <-ctx.Done():
						return out, ctx.Err()
					}
				}
				return out, err
			})
			awaitGate(t, entered)
			switch change {
			case "delete":
				_, err := f.pool.Exec(t.Context(),
					"DELETE FROM member WHERE organization_id=$1 AND id=$2", f.sub.Organization, f.member)
				requireNoError(t, err)
			case "rename":
				_, err := f.pool.Exec(t.Context(), "UPDATE organization SET slug='renamed' WHERE id=$1", f.sub.Organization)
				requireNoError(t, err)
				renamed := f
				renamed.sub.OrganizationSlug = "renamed"
				awaitDelivery(t, deliver(t, renamed, a, 1, rendered), 1, 1)
			case "recreate":
				tx, err := f.pool.Begin(t.Context())
				requireNoError(t, err)
				defer func() { _ = tx.Rollback(t.Context()) }()
				_, err = tx.Exec(t.Context(), "TRUNCATE organization CASCADE")
				requireNoError(t, err)
				_, err = tx.Exec(t.Context(),
					"INSERT INTO organization (id,slug,name) VALUES ($1,'acme','Replacement')", f.sub.Organization)
				requireNoError(t, err)
				requireNoError(t, tx.Commit(t.Context()))
			}
			close(release)
			awaitDelivery(t, result, 3, 1)
			awaitDelivery(t, deliver(t, f, a, 1, rendered), 1, 0)
		})
	}
}

type gatedMembership struct {
	org.MembershipStore
	calls            atomic.Int64
	entered, release chan struct{}
}

func (s *gatedMembership) Membership(ctx context.Context, account kernel.ID, slug string) (org.Membership, error) {
	m, err := s.MembershipStore.Membership(ctx, account, slug)
	if s.calls.Add(1) == 1 {
		close(s.entered)
		select {
		case <-s.release:
		case <-ctx.Done():
			return org.Membership{}, ctx.Err()
		}
	}
	return m, err
}

// The epoch comparison also protects this case; TestStaleLoadCannotReplaceNewResult pins the token guard.
func TestStreamStaleInflightMembership(t *testing.T) {
	f := streamFixture(t)
	gate := &gatedMembership{entered: make(chan struct{}), release: make(chan struct{})}
	a := orgpg.NewWrappedAuthorizerForTest(t.Context(), f.pool, func(store org.MembershipStore) org.MembershipStore {
		gate.MembershipStore = store
		return gate
	})
	first := deliver(t, f, a, 1, rendered)
	awaitGate(t, gate.entered)
	_, err := f.pool.Exec(t.Context(),
		"DELETE FROM member WHERE organization_id=$1 AND id=$2", f.sub.Organization, f.member)
	requireNoError(t, err)
	// Check 2 finishes while check 1's pre-revocation result is still gated.
	awaitDelivery(t, deliver(t, f, a, 1, rendered), 1, 0)
	close(gate.release)
	awaitDelivery(t, first, 1, 1) // Its snapshot preceded the commit: the accepted window.
	awaitDelivery(t, deliver(t, f, a, 1, rendered), 1, 0)
	if a.Stats().CacheHits != 0 {
		t.Fatalf("stale allow was cached: %+v", a.Stats())
	}
}
