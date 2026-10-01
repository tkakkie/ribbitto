package auth_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/tkakkie/ribbitto/internal/app/auth"
	"github.com/tkakkie/ribbitto/internal/domain"
	"github.com/tkakkie/ribbitto/internal/realtime"
)

type fakeSession struct {
	accountID domain.ID
	expiresAt time.Time
}

// fakeStore keeps sessions in memory and records every hash it receives.
type fakeStore struct {
	sessions map[string]fakeSession
	hashes   [][]byte
	before   time.Time
	err      error
}

func (f *fakeStore) CreateSession(_ context.Context, hash []byte, accountID domain.ID, expiresAt time.Time) error {
	f.hashes = append(f.hashes, bytes.Clone(hash))
	f.sessions[string(hash)] = fakeSession{accountID, expiresAt}
	return f.err
}

// sessionID derives a fake session's id from its hash.
func sessionID(hash []byte) domain.ID { return domain.ID(hash[:16]) }

func (f *fakeStore) SessionAccount(_ context.Context, hash []byte, now time.Time) (domain.Account, auth.Session, error) {
	f.hashes = append(f.hashes, bytes.Clone(hash))
	if f.err != nil {
		return domain.Account{}, auth.Session{}, f.err
	}
	session, ok := f.sessions[string(hash)]
	if !ok || !session.expiresAt.After(now) {
		return domain.Account{}, auth.Session{}, auth.ErrNoSession
	}
	return domain.Account{ID: session.accountID}, auth.Session{ID: sessionID(hash), ExpiresAt: session.expiresAt}, nil
}

func (f *fakeStore) DeleteSession(_ context.Context, hash []byte) (domain.ID, bool, error) {
	f.hashes = append(f.hashes, bytes.Clone(hash))
	if f.err != nil {
		return domain.ID{}, false, f.err
	}
	_, found := f.sessions[string(hash)]
	delete(f.sessions, string(hash))
	return sessionID(hash), found, nil
}

func (f *fakeStore) ReplaceSession(_ context.Context, oldHash, newHash []byte, accountID domain.ID, expiresAt time.Time) (domain.ID, bool, error) {
	f.hashes = append(f.hashes, bytes.Clone(oldHash), bytes.Clone(newHash))
	if f.err != nil {
		return domain.ID{}, false, f.err
	}
	_, found := f.sessions[string(oldHash)]
	delete(f.sessions, string(oldHash))
	f.sessions[string(newHash)] = fakeSession{accountID, expiresAt}
	return sessionID(oldHash), found, nil
}

func (f *fakeStore) DeleteExpiredSessions(_ context.Context, before time.Time) error {
	f.before = before
	return f.err
}

func TestSessions(t *testing.T) {
	start := time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)
	account := domain.ID{1, 2, 3}
	for _, tt := range []struct {
		name  string
		token func(created string) string
		after time.Duration
		del   bool
		want  error
	}{
		{"live", func(c string) string { return c }, 0, false, nil},
		{"one second before expiry", func(c string) string { return c }, auth.SessionLifetime - time.Second, false, nil},
		{"at expiry", func(c string) string { return c }, auth.SessionLifetime, false, auth.ErrNoSession},
		{"deleted", func(c string) string { return c }, 0, true, auth.ErrNoSession},
		{"unknown", func(string) string { return base64.RawURLEncoding.EncodeToString(make([]byte, 32)) }, 0, false, auth.ErrNoSession},
		{"empty", func(string) string { return "" }, 0, false, auth.ErrNoSession},
		{"too short", func(c string) string { return c[:42] }, 0, false, auth.ErrNoSession},
		{"too long", func(c string) string { return c + "A" }, 0, false, auth.ErrNoSession},
		{"padded", func(c string) string { return c[:40] + "==" }, 0, false, auth.ErrNoSession},
		{"standard alphabet", func(string) string { return strings.Repeat("+", 43) }, 0, false, auth.ErrNoSession},
	} {
		t.Run(tt.name, func(t *testing.T) {
			now := start
			store := &fakeStore{sessions: map[string]fakeSession{}}
			sessions := auth.NewSessions(store, func() time.Time { return now })
			token, expiresAt, err := sessions.Create(t.Context(), account)
			if err != nil {
				t.Fatal(err)
			}
			if !expiresAt.Equal(start.Add(auth.SessionLifetime)) {
				t.Fatalf("expiry = %v", expiresAt)
			}
			if tt.del {
				if err := sessions.Delete(t.Context(), token); err != nil {
					t.Fatal(err)
				}
			}
			now = start.Add(tt.after)
			got, _, err := sessions.Resolve(t.Context(), tt.token(token))
			if !errors.Is(err, tt.want) || (err == nil && got.ID != account) {
				t.Fatalf("Resolve = %+v, %v; want %v", got, err, tt.want)
			}
			raw, err := base64.RawURLEncoding.DecodeString(token)
			if err != nil || len(raw) != 32 {
				t.Fatalf("token is not 32 bytes of base64url: %q", token)
			}
			for _, hash := range store.hashes {
				if len(hash) != 32 || bytes.Equal(hash, raw) || bytes.Contains(hash, []byte(token)) {
					t.Fatalf("store received %x, not a 32-byte hash of the token", hash)
				}
			}
		})
	}
}

func TestSessionsStoreErrors(t *testing.T) {
	broken := errors.New("connection refused")
	store := &fakeStore{sessions: map[string]fakeSession{}}
	now := time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)
	sessions := auth.NewSessions(store, func() time.Time { return now })
	token, _, err := sessions.Create(t.Context(), domain.ID{1})
	if err != nil {
		t.Fatal(err)
	}
	store.err = broken
	if _, _, err := sessions.Resolve(t.Context(), token); !errors.Is(err, broken) || errors.Is(err, auth.ErrNoSession) {
		t.Fatalf("Resolve: want the store error, distinct from ErrNoSession; got %v", err)
	}
	if _, _, err := sessions.Create(t.Context(), domain.ID{1}); !errors.Is(err, broken) {
		t.Fatalf("Create: want the store error, got %v", err)
	}
	if err := sessions.Delete(t.Context(), token); !errors.Is(err, broken) {
		t.Fatalf("Delete: want the store error, got %v", err)
	}
	if err := sessions.DeleteExpired(t.Context()); !errors.Is(err, broken) || !store.before.Equal(now) {
		t.Fatalf("DeleteExpired: got %v with cutoff %v", err, store.before)
	}
}

// canceller records the sessions Sessions reported as ended.
type canceller struct{ ended []domain.ID }

func (c *canceller) CancelSession(id domain.ID) { c.ended = append(c.ended, id) }

func TestSessionsCancelOnlyDeletedSessions(t *testing.T) {
	broken := errors.New("transaction failed")
	tests := []struct {
		name string
		// act ends or replaces the session behind token; it may break the store.
		act         func(s *auth.Sessions, store *fakeStore, token string) error
		wantCancels int
	}{
		{"sign-out", func(s *auth.Sessions, _ *fakeStore, token string) error { return s.Delete(t.Context(), token) }, 1},
		{"sign-out of an unknown token", func(s *auth.Sessions, _ *fakeStore, _ string) error {
			other, _, err := s.Create(t.Context(), domain.ID{2})
			if err != nil {
				return err
			}
			if err := s.Delete(t.Context(), other); err != nil {
				return err
			}
			return s.Delete(t.Context(), other) // already gone: nothing to cancel twice
		}, 1},
		{"successful replacement", func(s *auth.Sessions, _ *fakeStore, token string) error {
			_, _, err := s.Replace(t.Context(), token, domain.ID{1})
			return err
		}, 1},
		{"failed replacement", func(s *auth.Sessions, store *fakeStore, token string) error {
			store.err = broken
			if _, _, err := s.Replace(t.Context(), token, domain.ID{1}); !errors.Is(err, broken) {
				return fmt.Errorf("Replace = %v, want %v", err, broken)
			}
			store.err = nil
			return nil
		}, 0},
		{"failed sign-out", func(s *auth.Sessions, store *fakeStore, token string) error {
			store.err = broken
			if err := s.Delete(t.Context(), token); !errors.Is(err, broken) {
				return fmt.Errorf("Delete = %v, want %v", err, broken)
			}
			store.err = nil
			return nil
		}, 0},
		{"replacement without a previous session", func(s *auth.Sessions, _ *fakeStore, _ string) error {
			_, _, err := s.Replace(t.Context(), "", domain.ID{1})
			return err
		}, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := &fakeStore{sessions: map[string]fakeSession{}}
			ended := &canceller{}
			sessions := auth.NewSessionsWithCanceller(store, time.Now, ended)
			token, _, err := sessions.Create(t.Context(), domain.ID{1})
			if err != nil {
				t.Fatal(err)
			}
			_, session, err := sessions.Resolve(t.Context(), token)
			if err != nil {
				t.Fatal(err)
			}
			if err := tt.act(sessions, store, token); err != nil {
				t.Fatal(err)
			}
			if len(ended.ended) != tt.wantCancels {
				t.Fatalf("cancelled %v, want %d", ended.ended, tt.wantCancels)
			}
			if tt.wantCancels == 1 && tt.name != "sign-out of an unknown token" && ended.ended[0] != session.ID {
				t.Fatalf("cancelled %x, want the session %x", ended.ended[0], session.ID)
			}
		})
	}
}

// With the real hub as canceller: a stream of the session stays usable after
// a failed replacement, and ends after a successful one.
func TestReplacementEndsStreamsOnlyWhenItSucceeds(t *testing.T) {
	store := &fakeStore{sessions: map[string]fakeSession{}}
	hub := realtime.NewHub()
	sessions := auth.NewSessionsWithCanceller(store, time.Now, hub)
	token, _, err := sessions.Create(t.Context(), domain.ID{1})
	if err != nil {
		t.Fatal(err)
	}
	_, session, err := sessions.Resolve(t.Context(), token)
	if err != nil {
		t.Fatal(err)
	}
	stream, unregister, err := hub.Register(t.Context(), realtime.Connection{Account: domain.ID{1}, Session: session.ID}, 1)
	if err != nil {
		t.Fatal(err)
	}
	defer unregister()

	store.err = errors.New("transaction failed")
	if _, _, err := sessions.Replace(t.Context(), token, domain.ID{1}); err == nil {
		t.Fatal("Replace succeeded with a broken store")
	}
	if stream.Err() != nil {
		t.Fatalf("a failed replacement ended the stream: %v", context.Cause(stream))
	}
	store.err = nil
	if _, _, err := sessions.Replace(t.Context(), token, domain.ID{1}); err != nil {
		t.Fatal(err)
	}
	if !errors.Is(context.Cause(stream), realtime.ErrSessionEnded) {
		t.Fatalf("stream after a successful replacement: %v, want %v", context.Cause(stream), realtime.ErrSessionEnded)
	}
}
