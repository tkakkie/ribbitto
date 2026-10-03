package identity_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/tkakkie/ribbitto/internal/domain"
	"github.com/tkakkie/ribbitto/internal/identity"
	"github.com/tkakkie/ribbitto/internal/realtime"
)

type fakeSession struct {
	accountID domain.ID
	expiresAt time.Time
}

// fakeStore keeps sessions in memory and records every hash it receives.
type fakeStore struct {
	sessions      map[string]fakeSession
	hashes        [][]byte
	before        time.Time
	err           error
	mutationErr   error
	commitOnError bool
	mutations     int
}

func (f *fakeStore) CreateSession(_ context.Context, hash []byte, accountID domain.ID, expiresAt time.Time) error {
	f.mutations++
	f.hashes = append(f.hashes, bytes.Clone(hash))
	f.sessions[string(hash)] = fakeSession{accountID, expiresAt}
	return f.err
}

// sessionID derives a fake session's id from its hash.
func sessionID(hash []byte) domain.ID { return domain.ID(hash[:16]) }

func (f *fakeStore) SessionAccount(_ context.Context, hash []byte, now time.Time) (identity.Account, identity.Session, error) {
	f.hashes = append(f.hashes, bytes.Clone(hash))
	if f.err != nil {
		return identity.Account{}, identity.Session{}, f.err
	}
	session, ok := f.sessions[string(hash)]
	if !ok || !session.expiresAt.After(now) {
		return identity.Account{}, identity.Session{}, identity.ErrNoSession
	}
	return identity.Account{ID: session.accountID}, identity.Session{ID: sessionID(hash), ExpiresAt: session.expiresAt}, nil
}

func (f *fakeStore) DeleteSession(_ context.Context, hash []byte) (domain.ID, bool, error) {
	f.mutations++
	f.hashes = append(f.hashes, bytes.Clone(hash))
	if f.err != nil {
		return domain.ID{}, false, f.err
	}
	if f.mutationErr != nil && !f.commitOnError {
		return domain.ID{}, false, f.mutationErr
	}
	_, found := f.sessions[string(hash)]
	delete(f.sessions, string(hash))
	if f.mutationErr != nil {
		return domain.ID{}, false, f.mutationErr
	}
	return sessionID(hash), found, nil
}

func (f *fakeStore) ReplaceSession(_ context.Context, oldHash, newHash []byte, accountID domain.ID, expiresAt time.Time) (domain.ID, bool, error) {
	f.mutations++
	f.hashes = append(f.hashes, bytes.Clone(oldHash), bytes.Clone(newHash))
	if f.err != nil {
		return domain.ID{}, false, f.err
	}
	if f.mutationErr != nil && !f.commitOnError {
		return domain.ID{}, false, f.mutationErr
	}
	_, found := f.sessions[string(oldHash)]
	delete(f.sessions, string(oldHash))
	f.sessions[string(newHash)] = fakeSession{accountID, expiresAt}
	if f.mutationErr != nil {
		return domain.ID{}, false, f.mutationErr
	}
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
		{"one second before expiry", func(c string) string { return c }, identity.SessionLifetime - time.Second, false, nil},
		{"at expiry", func(c string) string { return c }, identity.SessionLifetime, false, identity.ErrNoSession},
		{"deleted", func(c string) string { return c }, 0, true, identity.ErrNoSession},
		{"unknown", func(string) string { return base64.RawURLEncoding.EncodeToString(make([]byte, 32)) }, 0, false, identity.ErrNoSession},
		{"empty", func(string) string { return "" }, 0, false, identity.ErrNoSession},
		{"too short", func(c string) string { return c[:42] }, 0, false, identity.ErrNoSession},
		{"too long", func(c string) string { return c + "A" }, 0, false, identity.ErrNoSession},
		{"padded", func(c string) string { return c[:40] + "==" }, 0, false, identity.ErrNoSession},
		{"standard alphabet", func(string) string { return strings.Repeat("+", 43) }, 0, false, identity.ErrNoSession},
	} {
		t.Run(tt.name, func(t *testing.T) {
			now := start
			store := &fakeStore{sessions: map[string]fakeSession{}}
			sessions := identity.NewSessions(store, func() time.Time { return now })
			token, expiresAt, err := sessions.Create(t.Context(), account)
			if err != nil {
				t.Fatal(err)
			}
			if !expiresAt.Equal(start.Add(identity.SessionLifetime)) {
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
	sessions := identity.NewSessions(store, func() time.Time { return now })
	token, _, err := sessions.Create(t.Context(), domain.ID{1})
	if err != nil {
		t.Fatal(err)
	}
	store.err = broken
	if _, _, err := sessions.Resolve(t.Context(), token); !errors.Is(err, broken) || errors.Is(err, identity.ErrNoSession) {
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

// canceller records the IDs and forwards cancellation to real streams.
type canceller struct {
	ended []domain.ID
	hub   *realtime.Hub
}

func (c *canceller) CancelSession(id domain.ID) {
	c.ended = append(c.ended, id)
	c.hub.CancelSession(id)
}

func TestSessionsEndStreamsFailClosed(t *testing.T) {
	broken := errors.New("store unavailable")
	for _, replace := range []bool{false, true} {
		operation := "Delete"
		if replace {
			operation = "Replace"
		}
		for _, tt := range []struct {
			name                 string
			readErr, mutationErr error
			commit               bool
		}{
			{"success", nil, nil, false},
			{"rollback", nil, broken, false},
			{"commit then error", nil, broken, true},
			{"read error", broken, nil, false},
		} {
			t.Run(operation+"/"+tt.name, func(t *testing.T) {
				store := &fakeStore{sessions: map[string]fakeSession{}}
				ended := &canceller{hub: realtime.NewHub()}
				sessions := identity.NewSessionsWithCanceller(store, time.Now, ended)
				open := func() (string, identity.Session, context.Context) {
					t.Helper()
					token, _, err := sessions.Create(t.Context(), domain.ID{1})
					if err != nil {
						t.Fatal(err)
					}
					_, session, err := sessions.Resolve(t.Context(), token)
					if err != nil {
						t.Fatal(err)
					}
					stream, unregister, err := ended.hub.Register(t.Context(), realtime.Connection{Account: domain.ID{1}, Session: session.ID}, 2)
					if err != nil {
						t.Fatal(err)
					}
					t.Cleanup(unregister)
					return token, session, stream
				}
				token, session, stream := open()
				otherToken, _, otherStream := open()
				store.err, store.mutationErr, store.commitOnError = tt.readErr, tt.mutationErr, tt.commit
				store.mutations = 0
				var err error
				if replace {
					var next string
					var expiry time.Time
					next, expiry, err = sessions.Replace(t.Context(), token, domain.ID{1})
					if err != nil && (next != "" || !expiry.IsZero()) {
						t.Fatal("failed replacement returned credentials")
					}
					if err == nil {
						if _, _, err := sessions.Resolve(t.Context(), next); err != nil {
							t.Fatalf("new session: %v", err)
						}
					}
				} else {
					err = sessions.Delete(t.Context(), token)
				}
				wantErr := tt.mutationErr
				if tt.readErr != nil {
					wantErr = tt.readErr
				}
				if !errors.Is(err, wantErr) {
					t.Fatalf("error = %v, want %v", err, wantErr)
				}
				wantMutations := 1
				if tt.readErr != nil {
					wantMutations = 0
				}
				if store.mutations != wantMutations {
					t.Fatalf("mutations = %d, want %d", store.mutations, wantMutations)
				}
				if tt.readErr != nil {
					if len(ended.ended) != 0 || stream.Err() != nil {
						t.Fatal("read failure ended a stream")
					}
				} else {
					if len(ended.ended) != 1 || ended.ended[0] != session.ID {
						t.Fatalf("cancelled %v, want only %v", ended.ended, session.ID)
					}
					if !errors.Is(context.Cause(stream), realtime.ErrSessionEnded) {
						t.Fatalf("stream cause = %v", context.Cause(stream))
					}
				}
				if otherStream.Err() != nil {
					t.Fatalf("other session's stream ended: %v", context.Cause(otherStream))
				}
				store.err, store.mutationErr = nil, nil
				if _, _, err := sessions.Resolve(t.Context(), otherToken); err != nil {
					t.Fatalf("other session: %v", err)
				}
				changed := tt.readErr == nil && (tt.mutationErr == nil || tt.commit)
				wantResolve := error(nil)
				wantRows := 2
				if changed {
					wantResolve = identity.ErrNoSession
					if !replace {
						wantRows = 1
					}
				}
				if _, _, err := sessions.Resolve(t.Context(), token); !errors.Is(err, wantResolve) {
					t.Fatalf("old session: %v, want %v", err, wantResolve)
				}
				if len(store.sessions) != wantRows {
					t.Fatalf("stored sessions = %d, want %d", len(store.sessions), wantRows)
				}
				if changed {
					cancels := len(ended.ended)
					if err := sessions.Delete(t.Context(), token); err != nil {
						t.Fatalf("retry sign-out: %v", err)
					}
					if len(ended.ended) != cancels {
						t.Fatal("retry cancelled another session")
					}
				}
			})
		}
	}
}

func TestSessionsWithoutLivePreviousSession(t *testing.T) {
	for _, replace := range []bool{false, true} {
		operation := "Delete"
		if replace {
			operation = "Replace"
		}
		for _, state := range []string{"empty", "malformed", "unknown", "deleted", "expired"} {
			t.Run(operation+"/"+state, func(t *testing.T) {
				now := time.Now()
				store := &fakeStore{sessions: map[string]fakeSession{}}
				ended := &canceller{hub: realtime.NewHub()}
				sessions := identity.NewSessionsWithCanceller(store, func() time.Time { return now }, ended)
				token, _, err := sessions.Create(t.Context(), domain.ID{1})
				if err != nil {
					t.Fatal(err)
				}
				switch state {
				case "empty":
					token = ""
				case "malformed":
					token = "not-a-token"
				case "unknown":
					token = base64.RawURLEncoding.EncodeToString(make([]byte, 32))
				case "deleted":
					if err := sessions.Delete(t.Context(), token); err != nil {
						t.Fatal(err)
					}
				case "expired":
					now = now.Add(identity.SessionLifetime)
				}
				other, _, err := sessions.Create(t.Context(), domain.ID{1})
				if err != nil {
					t.Fatal(err)
				}
				_, otherSession, err := sessions.Resolve(t.Context(), other)
				if err != nil {
					t.Fatal(err)
				}
				stream, unregister, err := ended.hub.Register(t.Context(), realtime.Connection{Account: domain.ID{1}, Session: otherSession.ID}, 1)
				if err != nil {
					t.Fatal(err)
				}
				defer unregister()
				ended.ended = nil
				if _, _, err := sessions.Resolve(t.Context(), token); !errors.Is(err, identity.ErrNoSession) {
					t.Fatalf("previous session: %v", err)
				}
				if replace {
					next, _, err := sessions.Replace(t.Context(), token, domain.ID{2})
					if err != nil {
						t.Fatal(err)
					}
					account, _, err := sessions.Resolve(t.Context(), next)
					if err != nil || account.ID != (domain.ID{2}) {
						t.Fatalf("replacement account: %v, %v", account, err)
					}
				} else if err := sessions.Delete(t.Context(), token); err != nil {
					t.Fatal(err)
				}
				if stream.Err() != nil {
					t.Fatalf("other session's stream ended: %v", context.Cause(stream))
				}
				for _, id := range ended.ended {
					if state != "expired" || id == otherSession.ID {
						t.Fatalf("unexpected cancellation: %v", id)
					}
				}
				if _, _, err := sessions.Resolve(t.Context(), other); err != nil {
					t.Fatalf("other session: %v", err)
				}
			})
		}
	}
}
