package postgres

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/tkakkie/ribbitto/internal/app/channel"
	appmember "github.com/tkakkie/ribbitto/internal/app/member"
	"github.com/tkakkie/ribbitto/internal/app/setup"
	"github.com/tkakkie/ribbitto/internal/infra/postgres/sqlcgen"
	platform "github.com/tkakkie/ribbitto/internal/platform/postgres"
	"github.com/tkakkie/ribbitto/internal/platform/postgres/pgxbridge"
)

// SetupStore implements setup.Store with a single transaction per attempt.
type SetupStore struct {
	pool   *pgxpool.Pool
	events EventAppenderIn
}

// NewSetupStore returns a setup store backed by pool that appends events
// through events.
func NewSetupStore(pool *pgxpool.Pool, events EventAppenderIn) *SetupStore {
	return &SetupStore{pool: pool, events: events}
}

// Open reads installation-wide state without an organization filter.
func (s *SetupStore) Open(ctx context.Context) (bool, error) {
	open, err := sqlcgen.New(s.pool).SetupOpen(ctx)
	if err != nil {
		return false, fmt.Errorf("reading setup: %w", err)
	}
	return open, nil
}

// Create commits the organization, owner, default channel and completion
// marker together.
func (s *SetupStore) Create(ctx context.Context, organizationName, slug, email, displayName, handle, passwordHash string) (setup.Result, error) {
	var result setup.Result
	err := platform.InTx(ctx, s.pool, func(platformTx platform.Tx) error {
		tx := pgxbridge.Tx(platformTx)
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
		member, err := q.CreateMember(ctx, sqlcgen.CreateMemberParams{OrganizationID: org.ID, AccountID: account.ID, Role: "owner", JoinedEventSeq: seq, Handle: handle})
		if err != nil {
			return err
		}
		data := appmember.EncodeJoined(member.ID.Bytes)
		if err := s.events(platformTx).Append(ctx, org.ID.Bytes, seq, appmember.KindJoined, nil, data); err != nil {
			return err
		}
		// Listed exception (feature map): setup writes the channel feature's
		// table so that a completed setup never exists without its default
		// channel; a failure here rolls the organisation back too.
		if _, err := NewChannelStore(tx).CreateChannel(ctx, org.ID.Bytes, channel.DefaultName, true); err != nil {
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
			// Match by column prefix: PostgreSQL names a column's second
			// CHECK "…_check1" (account.email has two), and more may follow.
			switch name := pgErr.ConstraintName; {
			case name == "setup_pkey":
				return setup.Result{}, setup.ErrCompleted
			case strings.HasPrefix(name, "account_email_"):
				return setup.Result{}, setup.ValidationErrors{"email": errors.New("email is unavailable or invalid")}
			case strings.HasPrefix(name, "organization_slug_"):
				return setup.Result{}, setup.ValidationErrors{"slug": errors.New("slug is unavailable or invalid")}
			case strings.HasPrefix(name, "member_handle_"):
				return setup.Result{}, setup.ValidationErrors{"handle": errors.New("handle is invalid")}
			}
		}
		return setup.Result{}, fmt.Errorf("storing setup transaction: %w", err)
	}
	return result, nil
}
