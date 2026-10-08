package orgpg_test

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/tkakkie/ribbitto/internal/identity"
	"github.com/tkakkie/ribbitto/internal/identity/identitypg"
	"github.com/tkakkie/ribbitto/internal/identity/identitytest"
	"github.com/tkakkie/ribbitto/internal/kernel"
	"github.com/tkakkie/ribbitto/internal/org"
	"github.com/tkakkie/ribbitto/internal/org/internal/postgres"
	"github.com/tkakkie/ribbitto/internal/org/orgpg"
	"github.com/tkakkie/ribbitto/internal/org/orgtest"
	platform "github.com/tkakkie/ribbitto/internal/platform/postgres"
	"github.com/tkakkie/ribbitto/internal/platform/postgres/pgtest"
	"github.com/tkakkie/ribbitto/internal/realtime"
	"github.com/tkakkie/ribbitto/internal/realtime/realtimepg"
)

func TestSignUp(t *testing.T) {
	t.Parallel()
	pool := pgtest.New(t)
	ctx := t.Context()
	hasher, err := identity.NewHasher()
	requireNoError(t, err)
	service := orgpg.NewSignUp(pool, hasher, true, signupAccount, signupEvents)
	_, err = service.SignUp(ctx, "Alice", "alice", "alice@example.org", "long enough password")
	if !errors.Is(err, org.ErrSignUpClosed) {
		t.Fatalf("before setup: %v", err)
	}
	// Insert the other organisation first to catch selection by creation order.
	other := orgtest.Organization(t, pool, "other", "Other", 0)
	// A handle is unique only within its organisation: "alice" is taken in the other one.
	otherAccount := identitytest.Account(t, pool, "other@example.org", "Other")
	orgtest.Member(t, pool, other, otherAccount, org.RoleOwner, "alice", 1)
	result, err := orgpg.NewSetup(pool, hasher, "secret", signupAccount, signupEvents, defaultChannel).Complete(ctx, "secret", org.SetupInput{OrganizationName: "Team", Slug: "team", Email: "owner@example.org", DisplayName: "Owner", Handle: "owner", Password: "long enough password"})
	requireNoError(t, err)
	var initialEpoch, joinedEpoch int64
	requireNoError(t, pool.QueryRow(ctx, "SELECT access_epoch FROM organization WHERE id=$1", result.OrganizationID).Scan(&initialEpoch))
	id, err := service.SignUp(ctx, "Alice", "alice", "alice@example.org", "long enough password")
	requireNoError(t, err)
	requireNoError(t, pool.QueryRow(ctx, "SELECT access_epoch FROM organization WHERE id=$1", result.OrganizationID).Scan(&joinedEpoch))
	if joinedEpoch != initialEpoch {
		t.Fatal("sign-up join bumped the access epoch")
	}
	organizationID := pgtype.UUID{Bytes: result.OrganizationID, Valid: true}
	var account struct {
		ID    pgtype.UUID
		Email string
	}
	err = pool.QueryRow(ctx, "SELECT id, email FROM account WHERE id=$1", id).Scan(&account.ID, &account.Email)
	requireNoError(t, err)
	var member struct {
		Role, Handle   string
		JoinedEventSeq int64
	}
	err = pool.QueryRow(ctx, "SELECT role, handle, joined_event_seq FROM member WHERE organization_id=$1 AND account_id=$2", organizationID, account.ID).Scan(&member.Role, &member.Handle, &member.JoinedEventSeq)
	requireNoError(t, err)
	if account.Email != "alice@example.org" || member.Role != "member" || member.JoinedEventSeq != 2 || member.Handle != "alice" {
		t.Fatalf("account/member: %+v %+v", account, member)
	}
	appendFailure := errors.New("append failed")
	// Every failure leaves no account or member behind and takes no event_seq.
	for _, tc := range []struct {
		handle, email string
		want          error
		field         string
		failAppend    bool
	}{
		{"alice2", "alice@example.org", org.ErrEmailTaken, "", false},
		{"alice2", "Bad@Email", nil, "email", false},
		{"alice2", "e\u0301@example.org", nil, "email", false},
		{"alice", "alice2@example.org", org.ErrHandleTaken, "", false},
		{"owner", "alice2@example.org", org.ErrHandleTaken, "", false},
		{"Alice2", "alice2@example.org", nil, "handle", false},
		{"here", "alice2@example.org", nil, "handle", false},
		{"alice2", "alice2@example.org", appendFailure, "", true},
	} {
		createAccounts, writes, events := org.AccountCreatorIn(signupAccount), org.RegistrationWriterIn(registrationIn), org.EventAppenderIn(signupEvents)
		handle, email := tc.handle, tc.email
		// Substitute after validation so the real CHECKs reject the raw values.
		if tc.field == "email" {
			email = "alice2@example.org"
			createAccounts = func(tx platform.Tx) org.AccountCreator { return rawSignupAccount{signupAccount(tx), tc.email} }
		}
		if tc.field == "handle" {
			handle = "alice2"
			writes = func(tx platform.Tx) org.RegistrationWriter {
				return rawSignupMember{postgres.RegistrationWriterIn(tx), tc.handle}
			}
		}
		if tc.failAppend {
			events = func(platform.Tx) org.EventAppender { return failingSignupAppender{appendFailure} }
		}
		attempt := org.NewSignUp(postgres.NewSetupState(pool), orgpg.NewTxRunnerForTest(pool), writes, createAccounts, events, hasher, true)
		_, err := attempt.SignUp(ctx, "Alice", handle, email, "long enough password")
		var fields org.ValidationErrors
		if tc.want != nil && !errors.Is(err, tc.want) || tc.field != "" && (!errors.As(err, &fields) || fields[tc.field] == nil) {
			t.Fatalf("handle %s, email %s: %v", tc.handle, tc.email, err)
		}
		assertEventLog(t, pool, result.OrganizationID, 2)
		var accounts, members, otherMembers int
		var seq, otherSeq int64
		requireNoError(t, pool.QueryRow(ctx, "SELECT (SELECT count(*) FROM account), (SELECT count(*) FROM member WHERE organization_id=$1), (SELECT event_seq FROM organization WHERE id=$1), (SELECT count(*) FROM member WHERE organization_id=$2), (SELECT event_seq FROM organization WHERE id=$2)", organizationID, other).Scan(&accounts, &members, &seq, &otherMembers, &otherSeq))
		if accounts != 3 || members != 2 || seq != member.JoinedEventSeq || otherMembers != 1 || otherSeq != 0 {
			t.Fatalf("counts/sequences: %d %d %d %d %d", accounts, members, seq, otherMembers, otherSeq)
		}
	}
}

// The service normalises a handle before the store sees it, so case variants
// collide; concurrent claims leave exactly one winner and a typed conflict.
func TestSignUpHandleConflicts(t *testing.T) {
	t.Parallel()
	pool := pgtest.New(t)
	ctx := t.Context()
	hasher, err := identity.NewHasher()
	requireNoError(t, err)
	_, err = orgpg.NewSetup(pool, hasher, "secret", signupAccount, signupEvents, defaultChannel).Complete(ctx, "secret", org.SetupInput{OrganizationName: "Team", Slug: "team", Email: "owner@example.org", DisplayName: "Owner", Handle: "owner", Password: "long enough password"})
	requireNoError(t, err)
	service := orgpg.NewSignUp(pool, hasher, true, signupAccount, signupEvents)
	if _, err := service.SignUp(ctx, "Owner Two", " OWNER ", "owner2@example.org", "long enough password"); !errors.Is(err, org.ErrHandleTaken) {
		t.Fatalf("case variant: %v", err)
	}
	const racers = 8
	results := make(chan error, racers)
	for i := range racers {
		go func() {
			_, err := service.SignUp(ctx, "Racer", "racer", fmt.Sprintf("racer%d@example.org", i), "long enough password")
			results <- err
		}()
	}
	winners := 0
	for range racers {
		switch err := <-results; {
		case err == nil:
			winners++
		case !errors.Is(err, org.ErrHandleTaken):
			t.Fatalf("racer: %v", err)
		}
	}
	var accounts, members int
	var seq int64
	requireNoError(t, pool.QueryRow(ctx, "SELECT (SELECT count(*) FROM account), (SELECT count(*) FROM member WHERE handle = 'racer'), (SELECT event_seq FROM organization)").Scan(&accounts, &members, &seq))
	if winners != 1 || accounts != 2 || members != 1 || seq != 2 {
		t.Fatalf("winners=%d accounts=%d racer members=%d event_seq=%d", winners, accounts, members, seq)
	}
}

func registrationIn(tx platform.Tx) org.RegistrationWriter { return postgres.RegistrationWriterIn(tx) }

func signupAccount(tx platform.Tx) org.AccountCreator { return identitypg.AccountCreatorIn(tx) }
func signupEvents(tx platform.Tx) org.EventAppender   { return realtimepg.AppenderIn(tx) }

type rawSignupAccount struct {
	org.AccountCreator
	email string
}

func (w rawSignupAccount) CreateAccount(ctx context.Context, _ string, name, hash string) (kernel.ID, error) {
	return w.AccountCreator.CreateAccount(ctx, w.email, name, hash)
}

type rawSignupMember struct {
	org.RegistrationWriter
	handle string
}

func (w rawSignupMember) CreateMember(ctx context.Context, organizationID, accountID kernel.ID, role org.Role, seq int64, _ string) (kernel.ID, error) {
	return w.RegistrationWriter.CreateMember(ctx, organizationID, accountID, role, seq, w.handle)
}

type failingSignupAppender struct{ err error }

func (w failingSignupAppender) Append(context.Context, kernel.ID, int64, realtime.EventKind, *kernel.ID, []byte) error {
	return w.err
}
