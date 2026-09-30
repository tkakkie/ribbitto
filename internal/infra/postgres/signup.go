package postgres

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/tkakkie/ribbitto/internal/app/signup"
	"github.com/tkakkie/ribbitto/internal/domain"
	"github.com/tkakkie/ribbitto/internal/infra/postgres/sqlcgen"
)

// SignUp takes the organisation sequence before inserting either account or member.
// SetupStore also implements signup.Store because both use the installation setup row.
func (s *SetupStore) SignUp(ctx context.Context, displayName, handle, email, hash string) (domain.ID, error) {
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
		member, err := q.CreateMember(ctx, sqlcgen.CreateMemberParams{OrganizationID: org, AccountID: account.ID, Role: "member", JoinedEventSeq: seq, Handle: handle})
		if err != nil {
			return err
		}
		if err := q.InsertMemberEvent(ctx, sqlcgen.InsertMemberEventParams{OrganizationID: org, Seq: seq, Kind: string(domain.EventMemberJoined), MemberID: member.ID}); err != nil {
			return err
		}
		id = account.ID.Bytes
		return nil
	})
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) {
			if pgErr.Code == "23505" && pgErr.ConstraintName == "account_email_key" {
				return domain.ID{}, signup.ErrEmailTaken
			}
			// The database is the last line of defence for uniqueness: a
			// concurrent sign-up may claim the handle after validation.
			if pgErr.Code == "23505" && pgErr.ConstraintName == "member_organization_id_handle_key" {
				return domain.ID{}, signup.ErrHandleTaken
			}
			if pgErr.Code == "23514" && strings.HasPrefix(pgErr.ConstraintName, "member_handle_") {
				return domain.ID{}, signup.ValidationErrors{"handle": err}
			}
			// account.email has two CHECKs; the second (NFC) is named
			// account_email_check1, so match by column prefix as setup does.
			if pgErr.Code == "23514" && strings.HasPrefix(pgErr.ConstraintName, "account_email_") {
				return domain.ID{}, signup.ValidationErrors{"email": err}
			}
		}
		return domain.ID{}, fmt.Errorf("storing sign-up transaction: %w", err)
	}
	return id, nil
}
