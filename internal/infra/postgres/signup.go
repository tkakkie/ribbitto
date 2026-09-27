package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/tkakkie/ribbitto/internal/app/signup"
	"github.com/tkakkie/ribbitto/internal/domain"
	"github.com/tkakkie/ribbitto/internal/infra/postgres/sqlcgen"
)

// SignUp takes the organisation sequence before inserting either account or member.
// SetupStore also implements signup.Store because both use the installation setup row.
func (s *SetupStore) SignUp(ctx context.Context, displayName, email, hash string) (domain.ID, error) {
	var id domain.ID
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		q := sqlcgen.New(tx)
		org, err := q.SetupOrganization(ctx)
		if errors.Is(err, pgx.ErrNoRows) {
			return signup.ErrClosed
		}
		if err != nil {
			return err
		}
		seq, err := q.NextEventSeq(ctx, org)
		if err != nil {
			return err
		}
		account, err := q.CreateAccount(ctx, sqlcgen.CreateAccountParams{DisplayName: displayName, Email: email, PasswordHash: hash})
		if err != nil {
			return err
		}
		_, err = q.CreateMember(ctx, sqlcgen.CreateMemberParams{OrganizationID: org, AccountID: account.ID, Role: "member", JoinedEventSeq: seq})
		id = account.ID.Bytes
		return err
	})
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) {
			if pgErr.Code == "23505" && pgErr.ConstraintName == "account_email_key" {
				return domain.ID{}, signup.ErrEmailTaken
			}
			if pgErr.Code == "23514" && pgErr.ConstraintName == "account_email_check" {
				return domain.ID{}, signup.ValidationErrors{"email": err}
			}
		}
		return domain.ID{}, fmt.Errorf("storing sign-up transaction: %w", err)
	}
	return id, nil
}
