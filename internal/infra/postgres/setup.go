package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/tkakkie/ribbitto/internal/app/setup"
	"github.com/tkakkie/ribbitto/internal/infra/postgres/sqlcgen"
)

// SetupStore implements setup.Store with a single transaction per attempt.
type SetupStore struct{ pool *pgxpool.Pool }

// NewSetupStore returns a setup store backed by pool.
func NewSetupStore(pool *pgxpool.Pool) *SetupStore { return &SetupStore{pool: pool} }

// Open reads installation-wide state without an organization filter.
func (s *SetupStore) Open(ctx context.Context) (bool, error) {
	open, err := sqlcgen.New(s.pool).SetupOpen(ctx)
	if err != nil {
		return false, fmt.Errorf("reading setup: %w", err)
	}
	return open, nil
}

// Create commits the organization, owner and completion marker together.
func (s *SetupStore) Create(ctx context.Context, organizationName, slug, email, displayName, passwordHash string) (setup.Result, error) {
	var result setup.Result
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		q := sqlcgen.New(tx)
		org, err := q.CreateOrganization(ctx, sqlcgen.CreateOrganizationParams{Name: organizationName, Slug: slug})
		if err != nil {
			return err
		}
		seq, err := q.NextEventSeq(ctx, org.ID)
		if err != nil {
			return err
		}
		account, err := q.CreateAccount(ctx, sqlcgen.CreateAccountParams{Email: email, DisplayName: displayName, PasswordHash: passwordHash})
		if err != nil {
			return err
		}
		if _, err := q.CreateMember(ctx, sqlcgen.CreateMemberParams{OrganizationID: org.ID, AccountID: account.ID, Role: "owner", JoinedEventSeq: seq}); err != nil {
			return err
		}
		if err := q.CompleteSetup(ctx, org.ID); err != nil {
			return err
		}
		result = setup.Result{OrganizationID: org.ID.Bytes, AccountID: account.ID.Bytes}
		return nil
	})
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && (pgErr.Code == "23505" || pgErr.Code == "23514") {
			// A concurrent winner may have collided on email or slug first.
			if open, checkErr := s.Open(ctx); checkErr == nil && !open {
				return setup.Result{}, setup.ErrCompleted
			}
			switch pgErr.ConstraintName {
			case "setup_pkey":
				return setup.Result{}, setup.ErrCompleted
			case "account_email_key", "account_email_check":
				return setup.Result{}, setup.ValidationErrors{"email": errors.New("email is unavailable or invalid")}
			case "organization_slug_key", "organization_slug_check":
				return setup.Result{}, setup.ValidationErrors{"slug": errors.New("slug is unavailable or invalid")}
			}
		}
		return setup.Result{}, fmt.Errorf("storing setup transaction: %w", err)
	}
	return result, nil
}
