package postgres

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/tkakkie/ribbitto/internal/kernel"
	"github.com/tkakkie/ribbitto/internal/org"
	"github.com/tkakkie/ribbitto/internal/org/internal/postgres/sqlcgen"
	platform "github.com/tkakkie/ribbitto/internal/platform/postgres"
	"github.com/tkakkie/ribbitto/internal/platform/postgres/pgxbridge"
)

// RegistrationWriterIn binds setup's and sign-up's writes to the caller's
// transaction without managing its lifecycle.
func RegistrationWriterIn(tx platform.Tx) RegistrationWriter {
	return RegistrationWriter{queries: sqlcgen.New(pgxbridge.Tx(tx))}
}

// RegistrationWriter implements org.RegistrationWriter. It translates org's
// constraint names into org's errors, so the use cases never see pgconn.
type RegistrationWriter struct{ queries *sqlcgen.Queries }

// CreateOrganization inserts an organisation with a validated name and slug.
func (w RegistrationWriter) CreateOrganization(ctx context.Context, name, slug string) (kernel.ID, error) {
	row, err := w.queries.CreateOrganization(ctx, sqlcgen.CreateOrganizationParams{Name: name, Slug: slug})
	var pgErr *pgconn.PgError
	switch {
	// Match by column prefix: the slug has a UNIQUE (…_key) and a CHECK
	// (…_check), and a later CHECK on the column would be …_check1.
	case errors.As(err, &pgErr) && (pgErr.Code == "23505" || pgErr.Code == "23514") && strings.HasPrefix(pgErr.ConstraintName, "organization_slug_"):
		return kernel.ID{}, org.ErrSlugUnavailable
	case err != nil:
		return kernel.ID{}, fmt.Errorf("creating organization: %w", err)
	}
	return row.ID.Bytes, nil
}

// SetupOrganization reads the organisation the setup row names.
func (w RegistrationWriter) SetupOrganization(ctx context.Context) (kernel.ID, error) {
	id, err := w.queries.SetupOrganization(ctx)
	if errors.Is(err, pgx.ErrNoRows) {
		return kernel.ID{}, org.ErrSignUpClosed
	}
	if err != nil {
		return kernel.ID{}, fmt.Errorf("reading the setup organization: %w", err)
	}
	return id.Bytes, nil
}

// NextEventSeq takes the organisation's next event_seq, as Sequence does.
func (w RegistrationWriter) NextEventSeq(ctx context.Context, organizationID kernel.ID) (int64, error) {
	return Sequence{queries: w.queries}.NextEventSeq(ctx, organizationID)
}

// CreateMember inserts a member with a validated, canonical handle.
func (w RegistrationWriter) CreateMember(ctx context.Context, organizationID, accountID kernel.ID, role org.Role, joinedEventSeq int64, handle string) (kernel.ID, error) {
	row, err := w.queries.CreateMember(ctx, sqlcgen.CreateMemberParams{
		OrganizationID: pgtype.UUID{Bytes: organizationID, Valid: true},
		AccountID:      pgtype.UUID{Bytes: accountID, Valid: true},
		Role:           string(role),
		JoinedEventSeq: joinedEventSeq,
		Handle:         handle,
	})
	var pgErr *pgconn.PgError
	switch {
	// The database is the last line of defence for uniqueness: a concurrent
	// registration may claim the handle after validation.
	case errors.As(err, &pgErr) && pgErr.Code == "23505" && pgErr.ConstraintName == "member_organization_id_handle_key":
		return kernel.ID{}, org.ErrHandleTaken
	case errors.As(err, &pgErr) && pgErr.Code == "23514" && strings.HasPrefix(pgErr.ConstraintName, "member_handle_"):
		return kernel.ID{}, fmt.Errorf("%w: %w", org.ErrInvalidHandle, err)
	case err != nil:
		return kernel.ID{}, fmt.Errorf("creating member: %w", err)
	}
	return row.ID.Bytes, nil
}

// CompleteSetup inserts the installation's only setup row.
func (w RegistrationWriter) CompleteSetup(ctx context.Context, organizationID kernel.ID) error {
	err := w.queries.CompleteSetup(ctx, pgtype.UUID{Bytes: organizationID, Valid: true})
	var pgErr *pgconn.PgError
	switch {
	case errors.As(err, &pgErr) && pgErr.Code == "23505" && pgErr.ConstraintName == "setup_pkey":
		return org.ErrSetupCompleted
	case err != nil:
		return fmt.Errorf("completing setup: %w", err)
	}
	return nil
}

// SetupState reads installation-wide state on the pool (org.SetupState).
type SetupState struct{ queries *sqlcgen.Queries }

// NewSetupState returns the setup-state reader on db.
func NewSetupState(db sqlcgen.DBTX) SetupState {
	return SetupState{queries: sqlcgen.New(db)}
}

// Open reports whether no setup row exists, without an organisation filter.
func (s SetupState) Open(ctx context.Context) (bool, error) {
	open, err := s.queries.SetupOpen(ctx)
	if err != nil {
		return false, fmt.Errorf("reading setup: %w", err)
	}
	return open, nil
}
