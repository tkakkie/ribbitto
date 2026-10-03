package identity

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"time"

	"github.com/tkakkie/ribbitto/internal/domain"
)

// SessionLifetime is how long a session lasts after sign-in. It is absolute:
// using a session does not extend it.
const SessionLifetime = 30 * 24 * time.Hour

const tokenBytes = 32

// ErrNoSession means a token is malformed, unknown, deleted or expired.
// Callers treat all of these the same: the request is signed out.
var ErrNoSession = errors.New("no such session")

// Session identifies a live session and says when it expires. Streams use
// it to end with the session.
type Session struct {
	ID        domain.ID
	ExpiresAt time.Time
}

// SessionStore persists sessions by the SHA-256 hash of their token. It never
// receives a token, so a leaked table cannot be used to sign in.
type SessionStore interface {
	CreateSession(ctx context.Context, tokenHash []byte, accountID domain.ID, expiresAt time.Time) error
	// SessionAccount returns the account and the session with this hash if
	// it expires after now, and ErrNoSession otherwise.
	SessionAccount(ctx context.Context, tokenHash []byte, now time.Time) (domain.Account, Session, error)
	// DeleteSession deletes the session with this hash, if there is one, and
	// reports its id.
	DeleteSession(ctx context.Context, tokenHash []byte) (ended domain.ID, found bool, err error)
	// ReplaceSession deletes the session with oldHash, if there is one, and
	// stores the new session, atomically: either both happen or neither. It
	// reports the deleted session's id.
	ReplaceSession(ctx context.Context, oldHash, newHash []byte, accountID domain.ID, expiresAt time.Time) (ended domain.ID, found bool, err error)
	DeleteExpiredSessions(ctx context.Context, before time.Time) error
}

// SessionCanceller ends what still runs on a session that is deleted or may
// have been deleted, such as open event streams. realtime.Hub satisfies it.
type SessionCanceller interface {
	CancelSession(sessionID domain.ID)
}

// Sessions creates, resolves and deletes server-side sessions.
type Sessions struct {
	store  SessionStore
	now    func() time.Time
	cancel SessionCanceller
}

// NewSessions returns Sessions backed by store; now is time.Now outside tests.
func NewSessions(store SessionStore, now func() time.Time) *Sessions {
	return &Sessions{store: store, now: now}
}

// NewSessionsWithCanceller also ends streams after deletion or replacement,
// including when the store reports an error and the outcome is unknown.
func NewSessionsWithCanceller(store SessionStore, now func() time.Time, cancel SessionCanceller) *Sessions {
	return &Sessions{store: store, now: now, cancel: cancel}
}

// ended tells the canceller, if any, to stop a session's streams.
func (s *Sessions) ended(id domain.ID, found bool) {
	if found && s.cancel != nil {
		s.cancel.CancelSession(id)
	}
}

// Create starts a session for the account and returns its token, which only
// the caller (the browser's cookie) keeps, and its expiry.
func (s *Sessions) Create(ctx context.Context, accountID domain.ID) (string, time.Time, error) {
	token, hash, expiresAt, err := s.newToken()
	if err != nil {
		return "", time.Time{}, err
	}
	if err := s.store.CreateSession(ctx, hash, accountID, expiresAt); err != nil {
		return "", time.Time{}, fmt.Errorf("storing session: %w", err)
	}
	return token, expiresAt, nil
}

// Replace starts a session for the account and, in the same transaction,
// ends the session named by previousToken (the browser's cookie, possibly
// empty or stale). A mutation error ends the previous session's streams
// anyway: the transaction may have committed before reporting the error.
func (s *Sessions) Replace(ctx context.Context, previousToken string, accountID domain.ID) (string, time.Time, error) {
	old, ok := hashToken(previousToken)
	if !ok {
		return s.Create(ctx, accountID)
	}
	_, previous, resolveErr := s.Resolve(ctx, previousToken)
	if resolveErr != nil && !errors.Is(resolveErr, ErrNoSession) {
		return "", time.Time{}, resolveErr
	}
	token, hash, expiresAt, err := s.newToken()
	if err != nil {
		return "", time.Time{}, err
	}
	ended, found, err := s.store.ReplaceSession(ctx, old, hash, accountID, expiresAt)
	if err != nil {
		// The store may return no ID even if the deletion committed.
		s.ended(previous.ID, resolveErr == nil)
		return "", time.Time{}, fmt.Errorf("replacing session: %w", err)
	}
	s.ended(ended, found)
	return token, expiresAt, nil
}

func (s *Sessions) newToken() (token string, hash []byte, expiresAt time.Time, err error) {
	raw := make([]byte, tokenBytes)
	if _, err := rand.Read(raw); err != nil {
		return "", nil, time.Time{}, fmt.Errorf("creating session token: %w", err)
	}
	sum := sha256.Sum256(raw)
	return base64.RawURLEncoding.EncodeToString(raw), sum[:], s.now().Add(SessionLifetime), nil
}

// Resolve returns the account and session of a live session. ErrNoSession
// covers every reason the token does not sign anyone in; any other error is
// the store's.
func (s *Sessions) Resolve(ctx context.Context, token string) (domain.Account, Session, error) {
	hash, ok := hashToken(token)
	if !ok {
		return domain.Account{}, Session{}, ErrNoSession
	}
	account, session, err := s.store.SessionAccount(ctx, hash, s.now())
	if err != nil && !errors.Is(err, ErrNoSession) {
		return domain.Account{}, Session{}, fmt.Errorf("resolving session: %w", err)
	}
	return account, session, err
}

// Delete ends the session with this token, if there is one. A mutation error
// still ends its streams, since the deletion may have committed.
func (s *Sessions) Delete(ctx context.Context, token string) error {
	hash, ok := hashToken(token)
	if !ok {
		return nil
	}
	_, previous, resolveErr := s.Resolve(ctx, token)
	if resolveErr != nil && !errors.Is(resolveErr, ErrNoSession) {
		return resolveErr
	}
	ended, found, err := s.store.DeleteSession(ctx, hash)
	if err != nil {
		s.ended(previous.ID, resolveErr == nil)
		return fmt.Errorf("deleting session: %w", err)
	}
	s.ended(ended, found)
	return nil
}

// DeleteExpired removes sessions that have expired by now. Expired sessions
// are already rejected by Resolve; this only keeps the table small.
func (s *Sessions) DeleteExpired(ctx context.Context) error {
	if err := s.store.DeleteExpiredSessions(ctx, s.now()); err != nil {
		return fmt.Errorf("cleaning up expired sessions: %w", err)
	}
	return nil
}

// hashToken accepts only the exact encoding Create produces, checking the
// length before decoding so that an oversized cookie costs nothing.
func hashToken(token string) ([]byte, bool) {
	if len(token) != base64.RawURLEncoding.EncodedLen(tokenBytes) {
		return nil, false
	}
	raw, err := base64.RawURLEncoding.Strict().DecodeString(token)
	if err != nil || len(raw) != tokenBytes {
		return nil, false
	}
	hash := sha256.Sum256(raw)
	return hash[:], true
}
