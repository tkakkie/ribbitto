package postgres_test

import (
	"errors"
	"fmt"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/tkakkie/ribbitto/internal/app/auth"
	"github.com/tkakkie/ribbitto/internal/app/signup"
	"github.com/tkakkie/ribbitto/internal/domain"
	"github.com/tkakkie/ribbitto/internal/infra/postgres"
	"github.com/tkakkie/ribbitto/internal/infra/postgres/pgtest"
	"github.com/tkakkie/ribbitto/internal/infra/postgres/sqlcgen"
)

func TestSignUp(t *testing.T) {
	t.Parallel()
	pool := pgtest.New(t)
	ctx := t.Context()
	store := postgres.NewSetupStore(pool)
	_, err := store.SignUp(ctx, "Alice", "alice", "alice@example.org", "$argon2id$test")
	if !errors.Is(err, signup.ErrClosed) {
		t.Fatalf("before setup: %v", err)
	}
	// Insert the other organisation first to catch selection by creation order.
	other := pgtest.Organization(t, pool, "other", "Other", 0)
	// A handle is unique only within its organisation: "alice" is taken in the other one.
	otherAccount := pgtest.Account(t, pool, "other@example.org", "Other")
	pgtest.Member(t, pool, other, otherAccount, domain.RoleOwner, "alice", 1)
	result, err := postgres.NewSetupStore(pool).Create(ctx, "Team", "team", "owner@example.org", "Owner", "owner", "$argon2id$test")
	requireNoError(t, err)
	id, err := store.SignUp(ctx, "Alice", "alice", "alice@example.org", "$argon2id$test")
	requireNoError(t, err)
	org := pgtype.UUID{Bytes: result.OrganizationID, Valid: true}
	q := sqlcgen.New(pool)
	account, err := q.GetAccountByID(ctx, pgtype.UUID{Bytes: id, Valid: true})
	requireNoError(t, err)
	member, err := q.GetMemberByOrganizationAndAccount(ctx, sqlcgen.GetMemberByOrganizationAndAccountParams{OrganizationID: org, AccountID: account.ID})
	requireNoError(t, err)
	if account.Email != "alice@example.org" || member.Role != "member" || member.JoinedEventSeq != 2 || member.Handle != "alice" {
		t.Fatalf("account/member: %+v %+v", account, member)
	}
	// Every failure leaves no account or member behind and takes no event_seq.
	for _, tc := range []struct {
		handle, email string
		want          error
		field         string
	}{
		{"alice2", "alice@example.org", signup.ErrEmailTaken, ""},
		{"alice2", "Bad@Email", nil, "email"},
		{"alice2", "e\u0301@example.org", nil, "email"},
		{"alice", "alice2@example.org", signup.ErrHandleTaken, ""},
		{"owner", "alice2@example.org", signup.ErrHandleTaken, ""},
		{"Alice2", "alice2@example.org", nil, "handle"},
		{"here", "alice2@example.org", nil, "handle"},
	} {
		_, err := store.SignUp(ctx, "Alice", tc.handle, tc.email, "$argon2id$test")
		var fields signup.ValidationErrors
		if tc.want != nil && !errors.Is(err, tc.want) || tc.field != "" && (!errors.As(err, &fields) || fields[tc.field] == nil) {
			t.Fatalf("handle %s, email %s: %v", tc.handle, tc.email, err)
		}
		assertEventLog(t, pool, result.OrganizationID, 2)
		var accounts, members, otherMembers int
		var seq, otherSeq int64
		requireNoError(t, pool.QueryRow(ctx, "SELECT (SELECT count(*) FROM account), (SELECT count(*) FROM member WHERE organization_id=$1), (SELECT event_seq FROM organization WHERE id=$1), (SELECT count(*) FROM member WHERE organization_id=$2), (SELECT event_seq FROM organization WHERE id=$2)", org, other).Scan(&accounts, &members, &seq, &otherMembers, &otherSeq))
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
	hasher, err := auth.NewHasher()
	requireNoError(t, err)
	store := postgres.NewSetupStore(pool)
	_, err = store.Create(ctx, "Team", "team", "owner@example.org", "Owner", "owner", "$argon2id$test")
	requireNoError(t, err)
	service := signup.New(store, hasher, true)
	if _, err := service.SignUp(ctx, "Owner Two", " OWNER ", "owner2@example.org", "long enough password"); !errors.Is(err, signup.ErrHandleTaken) {
		t.Fatalf("case variant: %v", err)
	}
	const racers = 8
	results := make(chan error, racers)
	for i := range racers {
		go func() {
			_, err := store.SignUp(ctx, "Racer", "racer", fmt.Sprintf("racer%d@example.org", i), "$argon2id$test")
			results <- err
		}()
	}
	winners := 0
	for range racers {
		switch err := <-results; {
		case err == nil:
			winners++
		case !errors.Is(err, signup.ErrHandleTaken):
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
