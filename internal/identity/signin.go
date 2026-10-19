package identity

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/tkakkie/ribbitto/internal/kernel"
)

// ErrInvalidInput means the email is not an address or the password is
// empty; nothing was looked up or hashed.
var ErrInvalidInput = errors.New("email and password required")

// ErrInvalidCredentials means the email is unknown or the password is
// wrong. Callers must not tell the two apart.
var ErrInvalidCredentials = errors.New("invalid email or password")

// ErrNoAccount reports an unknown email at sign-in or a missing account
// when updating its send-key preference.
var ErrNoAccount = errors.New("no such account")

// AccountStore finds accounts and migrates legacy password hashes at sign-in.
type AccountStore interface {
	// AccountCredentials returns the ID and stored password hash of the
	// account with this normalised email, or ErrNoAccount.
	AccountCredentials(ctx context.Context, email string) (accountID kernel.ID, passwordHash string, err error)
	// RehashPassword replaces oldHash only if it is still current. A stale
	// hash is a no-op, so concurrent credential changes are never overwritten.
	RehashPassword(ctx context.Context, accountID kernel.ID, oldHash, newHash string) error
}

type signInHasher interface {
	Hash(context.Context, string) (string, error)
	Verify(context.Context, string, string) (bool, error)
	verifyDummy(context.Context, string) error
}

// SignIn signs accounts in and out.
type SignIn struct {
	accounts AccountStore
	hasher   signInHasher
	sessions *Sessions
}

// NewSignIn returns SignIn; share the process's one Hasher.
func NewSignIn(accounts AccountStore, hasher *Hasher, sessions *Sessions) *SignIn {
	return &SignIn{accounts: accounts, hasher: hasher, sessions: sessions}
}

// SignIn checks the credentials and starts a new session. previousToken is
// the session cookie the browser sent, if any; on success it is deleted
// first, so a token that existed before sign-in (possibly planted by an
// attacker) never becomes a signed-in session.
func (s *SignIn) SignIn(ctx context.Context, email, password, previousToken string) (string, time.Time, error) {
	email, err := ValidateEmail(email)
	if err != nil || password == "" {
		return "", time.Time{}, ErrInvalidInput
	}
	normalized := normalizePassword(password)
	accountID, hash, err := s.accounts.AccountCredentials(ctx, email)
	if errors.Is(err, ErrNoAccount) {
		// Match a known account's verification count regardless of result.
		// Stored hashes with older parameters may still cost differently.
		if err := s.hasher.verifyDummy(ctx, normalized); err != nil {
			return "", time.Time{}, err
		}
		if normalized != password {
			if err := s.hasher.verifyDummy(ctx, password); err != nil {
				return "", time.Time{}, err
			}
		}
		return "", time.Time{}, ErrInvalidCredentials
	}
	if err != nil {
		return "", time.Time{}, fmt.Errorf("finding account: %w", err)
	}
	ok, err := s.hasher.Verify(ctx, normalized, hash)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("verifying password: %w", err)
	}
	legacyOK := false
	if normalized != password {
		// Always verify both, even when NFC succeeds, so the count reveals
		// neither the result nor whether this account has a legacy hash.
		legacyOK, err = s.hasher.Verify(ctx, password, hash)
		if err != nil {
			return "", time.Time{}, fmt.Errorf("verifying legacy password: %w", err)
		}
	}
	if !ok && !legacyOK {
		return "", time.Time{}, ErrInvalidCredentials
	}
	if !ok && legacyOK {
		// Migration is best effort: failure must not lock out a valid legacy
		// password. No errors or credentials are logged by this package.
		if newHash, err := s.hasher.Hash(ctx, normalized); err == nil {
			_ = s.accounts.RehashPassword(ctx, accountID, hash, newHash)
		}
	}
	// One transaction creates the new session and ends the one the browser
	// sent; an uncertain outcome still ends the previous session's streams.
	return s.sessions.Replace(ctx, previousToken, accountID)
}

// SignOut ends the session with this token.
func (s *SignIn) SignOut(ctx context.Context, token string) error {
	return s.sessions.Delete(ctx, token)
}

// SignOutOthers ends the account's other sessions, keeping the resolved current one.
func (s *SignIn) SignOutOthers(ctx context.Context, accountID, currentID kernel.ID) error {
	return s.sessions.DeleteOthers(ctx, accountID, currentID)
}

// SessionHorizon returns a UUIDv7 generated now by the clock that stamps
// session ids, so every session created after it returns has a millisecond
// prefix at or after the horizon's. The signed-out sign-in page removes only
// stored drafts strictly older than it (decision 37 (d)); a web server clock
// could be skewed against session ids either way. It grants nothing.
func (s *SignIn) SessionHorizon(ctx context.Context) (kernel.ID, error) {
	id, err := s.sessions.store.SessionHorizon(ctx)
	if err != nil {
		return kernel.ID{}, fmt.Errorf("reading session horizon: %w", err)
	}
	return id, nil
}
