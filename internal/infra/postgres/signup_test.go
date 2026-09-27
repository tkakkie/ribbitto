package postgres_test

import (
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/tkakkie/ribbitto/internal/app/signup"
	"github.com/tkakkie/ribbitto/internal/infra/postgres"
	"github.com/tkakkie/ribbitto/internal/infra/postgres/pgtest"
	"github.com/tkakkie/ribbitto/internal/infra/postgres/sqlcgen"
)

func TestSignUp(t *testing.T) {
	pool := pgtest.New(t)
	ctx := t.Context()
	store := postgres.NewSetupStore(pool)
	_, err := store.SignUp(ctx, "Alice", "alice@example.org", "$argon2id$test")
	if !errors.Is(err, signup.ErrClosed) {
		t.Fatalf("before setup: %v", err)
	}
	// Insert the other organisation first to catch selection by creation order.
	q := sqlcgen.New(pool)
	other, err := q.CreateOrganization(ctx, sqlcgen.CreateOrganizationParams{Name: "Other", Slug: "other"})
	requireAccountSchema(t, err)
	result, err := postgres.NewSetupStore(pool).Create(ctx, "Team", "team", "owner@example.org", "Owner", "$argon2id$test")
	requireAccountSchema(t, err)
	id, err := store.SignUp(ctx, "Alice", "alice@example.org", "$argon2id$test")
	requireAccountSchema(t, err)
	org := pgtype.UUID{Bytes: result.OrganizationID, Valid: true}
	account, err := q.GetAccountByID(ctx, pgtype.UUID{Bytes: id, Valid: true})
	requireAccountSchema(t, err)
	member, err := q.GetMemberByOrganizationAndAccount(ctx, sqlcgen.GetMemberByOrganizationAndAccountParams{OrganizationID: org, AccountID: account.ID})
	requireAccountSchema(t, err)
	if account.Email != "alice@example.org" || member.Role != "member" || member.JoinedEventSeq != 2 {
		t.Fatalf("account/member: %+v %+v", account, member)
	}
	for _, email := range []string{"alice@example.org", "Bad@Email"} {
		_, err := store.SignUp(ctx, "Alice", email, "$argon2id$test")
		var fields signup.ValidationErrors
		if email == "alice@example.org" && !errors.Is(err, signup.ErrEmailTaken) || email == "Bad@Email" && (!errors.As(err, &fields) || fields["email"] == nil) {
			t.Fatalf("email %s: %v", email, err)
		}
		var accounts, members, otherMembers int
		var seq, otherSeq int64
		requireAccountSchema(t, pool.QueryRow(ctx, "SELECT (SELECT count(*) FROM account), (SELECT count(*) FROM member WHERE organization_id=$1), (SELECT event_seq FROM organization WHERE id=$1), (SELECT count(*) FROM member WHERE organization_id=$2), (SELECT event_seq FROM organization WHERE id=$2)", org, other.ID).Scan(&accounts, &members, &seq, &otherMembers, &otherSeq))
		if accounts != 2 || members != 2 || seq != member.JoinedEventSeq || otherMembers != 0 || otherSeq != 0 {
			t.Fatalf("counts/sequences: %d %d %d %d %d", accounts, members, seq, otherMembers, otherSeq)
		}
	}
}
