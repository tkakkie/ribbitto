package auth_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/tkakkie/ribbitto/internal/app/auth"
	"github.com/tkakkie/ribbitto/internal/domain"
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

func (f *fakeStore) SessionAccount(_ context.Context, hash []byte, now time.Time) (domain.Account, error) {
	f.hashes = append(f.hashes, bytes.Clone(hash))
	if f.err != nil {
		return domain.Account{}, f.err
	}
	session, ok := f.sessions[string(hash)]
	if !ok || !session.expiresAt.After(now) {
		return domain.Account{}, auth.ErrNoSession
	}
	return domain.Account{ID: session.accountID}, nil
}

func (f *fakeStore) DeleteSession(_ context.Context, hash []byte) error {
	f.hashes = append(f.hashes, bytes.Clone(hash))
	delete(f.sessions, string(hash))
	return f.err
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
			got, err := sessions.Resolve(t.Context(), tt.token(token))
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
	if _, err := sessions.Resolve(t.Context(), token); !errors.Is(err, broken) || errors.Is(err, auth.ErrNoSession) {
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
