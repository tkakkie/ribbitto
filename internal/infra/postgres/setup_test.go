package postgres_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/tkakkie/ribbitto/internal/app/auth"
	"github.com/tkakkie/ribbitto/internal/app/setup"
	"github.com/tkakkie/ribbitto/internal/infra/postgres"
	"github.com/tkakkie/ribbitto/internal/infra/postgres/pgtest"
	"github.com/tkakkie/ribbitto/internal/infra/postgres/sqlcgen"
)

// Hold all attempts after the open check, so every contender reaches Create.
type setupBarrier struct {
	setup.Store
	ready, release chan struct{}
}

func (s setupBarrier) Open(ctx context.Context) (bool, error) {
	open, err := s.Store.Open(ctx)
	s.ready <- struct{}{}
	<-s.release
	return open, err
}

func TestSetup(t *testing.T) {
	t.Parallel()
	hasher, err := auth.NewHasher()
	requireNoError(t, err)
	for _, attempts := range []int{1, 10} {
		t.Run(fmt.Sprint(attempts), func(t *testing.T) {
			pool := pgtest.New(t)
			ctx := t.Context()
			store := postgres.NewSetupStore(pool)
			s := setup.New(store, hasher, "secret")
			input := setup.Input{OrganizationName: "Example", Slug: "example", Email: " Owner@Example.org ", DisplayName: " Owner ", Handle: " Owner ", Password: "long enough password"}
			counts := func(want int) {
				t.Helper()
				var orgs, accounts, members, setups, defaults int
				requireNoError(t, pool.QueryRow(ctx, "SELECT (SELECT count(*) FROM organization), (SELECT count(*) FROM account), (SELECT count(*) FROM member), (SELECT count(*) FROM setup), (SELECT count(*) FROM channel WHERE is_default AND name = 'general')").Scan(&orgs, &accounts, &members, &setups, &defaults))
				// A completed setup always comes with exactly one default channel.
				if orgs != want || accounts != want || members != want || setups != want || defaults != want {
					t.Fatalf("row counts: %d %d %d %d %d; want %d each", orgs, accounts, members, setups, defaults, want)
				}
			}
			_, err := s.Complete(ctx, "wrong", input)
			if !errors.Is(err, setup.ErrToken) {
				t.Fatalf("rejected token: %v", err)
			}
			counts(0)
			for _, tc := range []struct{ slug, email, handle, field string }{{"example", "A@b", "owner", "email"}, {"example", "e\u0301@b", "owner", "email"}, {"Bad", "a@b", "owner", "slug"}, {"example", "a@b", "Owner", "handle"}, {"example", "a@b", "all", "handle"}} {
				_, err := store.Create(ctx, "Example", tc.slug, tc.email, "Owner", tc.handle, "$argon2id$test")
				var fields setup.ValidationErrors
				if !errors.As(err, &fields) || fields[tc.field] == nil {
					t.Fatalf("database validation for %s: %v", tc.field, err)
				}
				counts(0)
			}
			barrier := setupBarrier{Store: store, ready: make(chan struct{}, attempts), release: make(chan struct{})}
			contender := setup.New(barrier, hasher, "secret")
			results := make(chan error, attempts)
			for i := range attempts {
				go func() {
					in := input
					in.Slug, in.Email = fmt.Sprintf("example-%d", i), fmt.Sprintf(" Owner%d@Example.org ", i)
					_, err := contender.Complete(ctx, "secret", in)
					results <- err
				}()
			}
			for range attempts {
				<-barrier.ready
			}
			close(barrier.release)
			successes := 0
			for range attempts {
				if err := <-results; err == nil {
					successes++
				} else if !errors.Is(err, setup.ErrCompleted) {
					t.Fatalf("contender: %v", err)
				}
			}
			if successes != 1 {
				t.Fatalf("successes = %d", successes)
			}
			_, err = s.Complete(ctx, "secret", input)
			if !errors.Is(err, setup.ErrCompleted) {
				t.Fatalf("repeated setup: %v", err)
			}
			counts(1)
			open, err := s.Open(ctx)
			if err != nil || open {
				t.Fatalf("completed setup open = %t: %v", open, err)
			}
			var organizationID, accountID pgtype.UUID
			var seq, joined int64
			var role, handle string
			var completed bool
			requireNoError(t, pool.QueryRow(ctx, "SELECT s.organization_id, m.account_id, o.event_seq, m.joined_event_seq, m.role, m.handle, s.completed_at IS NOT NULL FROM setup s JOIN organization o ON o.id = s.organization_id JOIN member m ON m.organization_id = s.organization_id WHERE s.id").Scan(&organizationID, &accountID, &seq, &joined, &role, &handle, &completed))
			account, err := sqlcgen.New(pool).GetAccountByID(ctx, accountID)
			requireNoError(t, err)
			matches, err := hasher.Verify(ctx, input.Password, account.PasswordHash)
			if err != nil || !matches || seq != 1 || joined != 1 || role != "owner" || handle != "owner" || !completed || account.DisplayName != "Owner" || account.Email != strings.ToLower(strings.TrimSpace(account.Email)) {
				t.Fatalf("invalid owner/setup: seq=%d joined=%d role=%s hash match=%t error=%v", seq, joined, role, matches, err)
			}
		})
	}
}
