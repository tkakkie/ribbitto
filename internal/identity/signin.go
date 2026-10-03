package identity

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/tkakkie/ribbitto/internal/domain"
)

// ErrInvalidInput means the email is not an address or the password is
// empty; nothing was looked up or hashed.
var ErrInvalidInput = errors.New("email and password required")

// ErrInvalidCredentials means the email is unknown or the password is
// wrong. Callers must not tell the two apart.
var ErrInvalidCredentials = errors.New("invalid email or password")

// ErrNoAccount is what an AccountStore returns for an unknown email.
var ErrNoAccount = errors.New("no such account")

// AccountStore finds accounts for signing in.
type AccountStore interface {
	// AccountCredentials returns the account with this normalised email and
	// its stored password hash, or ErrNoAccount.
	AccountCredentials(ctx context.Context, email string) (domain.Account, string, error)
}

// SignIn signs accounts in and out.
type SignIn struct {
	accounts AccountStore
	hasher   *Hasher
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
	email, err := domain.ValidateEmail(email)
	if err != nil || password == "" {
		return "", time.Time{}, ErrInvalidInput
	}
	account, hash, err := s.accounts.AccountCredentials(ctx, email)
	if errors.Is(err, ErrNoAccount) {
		// Do one Argon2id verification as for a real account, which makes
		// timing-based discovery of accounts much harder. Timing is not
		// identical when a stored hash uses other parameters than the
		// current ones.
		if err := s.hasher.VerifyDummy(ctx, password); err != nil {
			return "", time.Time{}, err
		}
		return "", time.Time{}, ErrInvalidCredentials
	}
	if err != nil {
		return "", time.Time{}, fmt.Errorf("finding account: %w", err)
	}
	ok, err := s.hasher.Verify(ctx, password, hash)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("verifying password: %w", err)
	}
	if !ok {
		return "", time.Time{}, ErrInvalidCredentials
	}
	// One transaction creates the new session and ends the one the browser
	// sent; an uncertain outcome still ends the previous session's streams.
	return s.sessions.Replace(ctx, previousToken, account.ID)
}

// SignOut ends the session with this token.
func (s *SignIn) SignOut(ctx context.Context, token string) error {
	return s.sessions.Delete(ctx, token)
}
