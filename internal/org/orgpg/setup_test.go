package orgpg_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/tkakkie/ribbitto/internal/conversation/conversationpg"
	"github.com/tkakkie/ribbitto/internal/identity"
	"github.com/tkakkie/ribbitto/internal/identity/identitytest"
	"github.com/tkakkie/ribbitto/internal/kernel"
	"github.com/tkakkie/ribbitto/internal/org"
	"github.com/tkakkie/ribbitto/internal/org/orgpg"
	"github.com/tkakkie/ribbitto/internal/org/orgtest"
	platform "github.com/tkakkie/ribbitto/internal/platform/postgres"
	"github.com/tkakkie/ribbitto/internal/platform/postgres/pgtest"
)

// Hold all attempts after the open check, so every contender reaches the
// transaction; the re-check after a failed one passes once released.
type setupBarrier struct {
	org.SetupState
	ready, release chan struct{}
}

func (s setupBarrier) Open(ctx context.Context) (bool, error) {
	open, err := s.SetupState.Open(ctx)
	select {
	case <-s.release:
	default:
		s.ready <- struct{}{}
		<-s.release
	}
	return open, err
}

func newSetup(pool *pgxpool.Pool, hasher *identity.Hasher, state org.SetupState, writes org.RegistrationWriterIn, accounts org.AccountCreatorIn) *org.Setup {
	return org.NewSetup(state, orgpg.NewTxRunner(pool), writes, accounts, signupEvents, defaultChannel, hasher, "secret")
}

func defaultChannel(tx platform.Tx) org.DefaultChannelCreator {
	return conversationpg.DefaultChannelCreatorIn(tx)
}

type rawSetupOrganization struct {
	org.RegistrationWriter
	slug string
}

func (w rawSetupOrganization) CreateOrganization(ctx context.Context, name, _ string) (kernel.ID, error) {
	return w.RegistrationWriter.CreateOrganization(ctx, name, w.slug)
}

func TestSetup(t *testing.T) {
	t.Parallel()
	hasher, err := identity.NewHasher()
	requireNoError(t, err)
	for _, attempts := range []int{1, 10} {
		t.Run(fmt.Sprint(attempts), func(t *testing.T) {
			pool := pgtest.New(t)
			ctx := t.Context()
			s := orgpg.NewSetup(pool, hasher, "secret", signupAccount, signupEvents, defaultChannel)
			input := org.SetupInput{OrganizationName: "Example", Slug: "example", Email: " Owner@Example.org ", DisplayName: " Owner ", Handle: " Owner ", Password: "long enough password"}
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
			if !errors.Is(err, org.ErrSetupToken) {
				t.Fatalf("rejected token: %v", err)
			}
			counts(0)
			for _, tc := range []struct{ slug, email, handle, field string }{{"example", "A@b", "owner", "email"}, {"example", "e\u0301@b", "owner", "email"}, {"Bad", "a@b", "owner", "slug"}, {"example", "a@b", "Owner", "handle"}, {"example", "a@b", "all", "handle"}} {
				// Substitute after validation so the real constraints reject the raw values.
				writes := func(tx platform.Tx) org.RegistrationWriter {
					return rawSetupOrganization{rawSignupMember{orgpg.RegistrationWriterIn(tx), tc.handle}, tc.slug}
				}
				accounts := func(tx platform.Tx) org.AccountCreator { return rawSignupAccount{signupAccount(tx), tc.email} }
				_, err := newSetup(pool, hasher, orgpg.NewSetupState(pool), writes, accounts).Complete(ctx, "secret", input)
				var fields org.ValidationErrors
				if !errors.As(err, &fields) || fields[tc.field] == nil {
					t.Fatalf("database validation for %s: %v", tc.field, err)
				}
				counts(0)
			}
			barrier := setupBarrier{SetupState: orgpg.NewSetupState(pool), ready: make(chan struct{}, attempts), release: make(chan struct{})}
			contender := newSetup(pool, hasher, barrier, orgpg.RegistrationWriterIn, signupAccount)
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
				} else if !errors.Is(err, org.ErrSetupCompleted) {
					t.Fatalf("contender: %v", err)
				}
			}
			if successes != 1 {
				t.Fatalf("successes = %d", successes)
			}
			_, err = s.Complete(ctx, "secret", input)
			if !errors.Is(err, org.ErrSetupCompleted) {
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
			assertEventLog(t, pool, organizationID.Bytes, 1)
			var account struct{ Email, DisplayName, PasswordHash string }
			requireNoError(t, pool.QueryRow(ctx, "SELECT email, display_name, password_hash FROM account WHERE id = $1", accountID).Scan(&account.Email, &account.DisplayName, &account.PasswordHash))
			matches, err := hasher.Verify(ctx, input.Password, account.PasswordHash)
			if err != nil || !matches || seq != 1 || joined != 1 || role != "owner" || handle != "owner" || !completed || account.DisplayName != "Owner" || account.Email != strings.ToLower(strings.TrimSpace(account.Email)) {
				t.Fatalf("invalid owner/setup: seq=%d joined=%d role=%s hash match=%t error=%v", seq, joined, role, matches, err)
			}
		})
	}
}

func TestSetupConflictRace(t *testing.T) {
	t.Parallel()
	hasher, err := identity.NewHasher()
	requireNoError(t, err)
	for _, field := range []string{"slug", "email"} {
		t.Run(field, func(t *testing.T) {
			pool := pgtest.New(t)
			ctx := t.Context()
			const attempts = 10
			barrier := setupBarrier{SetupState: orgpg.NewSetupState(pool), ready: make(chan struct{}, attempts), release: make(chan struct{})}
			s := newSetup(pool, hasher, barrier, orgpg.RegistrationWriterIn, signupAccount)
			results := make(chan error, attempts)
			for i := range attempts {
				go func() {
					input := org.SetupInput{OrganizationName: "Example", Slug: "example", Email: "owner@example.org", DisplayName: "Owner", Handle: "owner", Password: "long enough password"}
					if field == "slug" {
						input.Email = fmt.Sprintf("owner%d@example.org", i)
					} else {
						input.Slug = fmt.Sprintf("example-%d", i)
					}
					_, err := s.Complete(ctx, "secret", input)
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
				} else if !errors.Is(err, org.ErrSetupCompleted) {
					t.Errorf("contender: %v; want ErrSetupCompleted", err)
				}
			}
			if successes != 1 {
				t.Errorf("successes = %d; want 1", successes)
			}
			var orgs, accounts, members, setups int
			requireNoError(t, pool.QueryRow(ctx, "SELECT (SELECT count(*) FROM organization), (SELECT count(*) FROM account), (SELECT count(*) FROM member), (SELECT count(*) FROM setup)").Scan(&orgs, &accounts, &members, &setups))
			if orgs != 1 || accounts != 1 || members != 1 || setups != 1 {
				t.Fatalf("row counts: %d %d %d %d; want 1 each", orgs, accounts, members, setups)
			}
		})
	}
}

func TestSetupOpenConflict(t *testing.T) {
	t.Parallel()
	hasher, err := identity.NewHasher()
	requireNoError(t, err)
	for _, field := range []string{"slug", "email"} {
		t.Run(field, func(t *testing.T) {
			pool := pgtest.New(t)
			ctx := t.Context()
			input := org.SetupInput{OrganizationName: "Example", Slug: "example", Email: "owner@example.org", DisplayName: "Owner", Handle: "owner", Password: "long enough password"}
			if field == "slug" {
				orgtest.Organization(t, pool, input.Slug, "Existing", 0)
			} else {
				identitytest.Account(t, pool, input.Email, "Existing")
			}
			s := orgpg.NewSetup(pool, hasher, "secret", signupAccount, signupEvents, defaultChannel)
			open, err := s.Open(ctx)
			if err != nil || !open {
				t.Fatalf("before conflict: setup open = %t: %v", open, err)
			}
			_, err = s.Complete(ctx, "secret", input)
			var fields org.ValidationErrors
			if errors.Is(err, org.ErrSetupCompleted) || !errors.As(err, &fields) || len(fields) != 1 || fields[field] == nil {
				t.Fatalf("open setup conflict: %v; want %s field error", err, field)
			}
			open, err = s.Open(ctx)
			if err != nil || !open {
				t.Fatalf("after conflict: setup open = %t: %v", open, err)
			}
		})
	}
}

type failingDefaultChannel struct{ err error }

func (c failingDefaultChannel) CreateDefaultChannel(context.Context, kernel.ID) error {
	return c.err
}

func TestSetupDefaultChannelRollback(t *testing.T) {
	t.Parallel()
	pool := pgtest.New(t)
	ctx := t.Context()
	hasher, err := identity.NewHasher()
	requireNoError(t, err)
	failure := errors.New("default channel refused")
	channels := func(platform.Tx) org.DefaultChannelCreator {
		return failingDefaultChannel{err: failure}
	}
	s := orgpg.NewSetup(pool, hasher, "secret", signupAccount, signupEvents, channels)
	_, err = s.Complete(ctx, "secret", org.SetupInput{OrganizationName: "Example", Slug: "example", Email: "owner@example.org", DisplayName: "Owner", Handle: "owner", Password: "long enough password"})
	if !errors.Is(err, failure) {
		t.Fatalf("Complete = %v, want injected default-channel error", err)
	}
	var rows int
	requireNoError(t, pool.QueryRow(ctx, `SELECT
		(SELECT count(*) FROM organization) + (SELECT count(*) FROM account) +
		(SELECT count(*) FROM member) + (SELECT count(*) FROM setup) +
		(SELECT count(*) FROM event_log)`).Scan(&rows))
	if rows != 0 {
		t.Fatalf("%d rows left behind after default-channel failure", rows)
	}
}
