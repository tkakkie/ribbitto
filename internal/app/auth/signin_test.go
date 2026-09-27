package auth_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"testing"
	"time"

	"github.com/tkakkie/ribbitto/internal/app/auth"
	"github.com/tkakkie/ribbitto/internal/domain"
)

type fakeAccounts struct {
	account domain.Account
	hash    string
	err     error
}

func (f fakeAccounts) AccountCredentials(_ context.Context, email string) (domain.Account, string, error) {
	if f.err != nil {
		return domain.Account{}, "", f.err
	}
	if email != f.account.Email {
		return domain.Account{}, "", auth.ErrNoAccount
	}
	return f.account, f.hash, nil
}

func TestSignIn(t *testing.T) {
	hasher, err := auth.NewHasher()
	if err != nil {
		t.Fatal(err)
	}
	const password = "correct horse battery staple"
	hash, err := hasher.Hash(t.Context(), password)
	if err != nil {
		t.Fatal(err)
	}
	alice := domain.Account{ID: domain.ID{7}, Email: "alice@example.com", DisplayName: "Alice"}
	cancelled, cancel := context.WithCancel(t.Context())
	cancel()
	broken := errors.New("connection refused")
	for _, tt := range []struct {
		name            string
		ctx             context.Context
		accounts        fakeAccounts
		email, password string
		want            error
	}{
		{"success, email normalised", t.Context(), fakeAccounts{account: alice, hash: hash}, "  Alice@Example.com ", password, nil},
		{"wrong password", t.Context(), fakeAccounts{account: alice, hash: hash}, "alice@example.com", password + "!", auth.ErrInvalidCredentials},
		{"unknown email", t.Context(), fakeAccounts{account: alice, hash: hash}, "bob@example.com", password, auth.ErrInvalidCredentials},
		// With a cancelled context the hasher refuses to work, so ErrBusy
		// proves an unknown email still goes through a verification.
		{"unknown email still verifies", cancelled, fakeAccounts{account: alice, hash: hash}, "bob@example.com", password, auth.ErrBusy},
		{"empty password", t.Context(), fakeAccounts{account: alice, hash: hash}, "alice@example.com", "", auth.ErrInvalidInput},
		{"not an email", t.Context(), fakeAccounts{account: alice, hash: hash}, "alice", password, auth.ErrInvalidInput},
		{"store error", t.Context(), fakeAccounts{err: broken}, "alice@example.com", password, broken},
	} {
		t.Run(tt.name, func(t *testing.T) {
			store := &fakeStore{sessions: map[string]fakeSession{}}
			sessions := auth.NewSessions(store, time.Now)
			previous, _, err := sessions.Create(t.Context(), alice.ID)
			if err != nil {
				t.Fatal(err)
			}
			signIn := auth.NewSignIn(tt.accounts, hasher, sessions)
			token, _, err := signIn.SignIn(tt.ctx, tt.email, tt.password, previous)
			if !errors.Is(err, tt.want) {
				t.Fatalf("SignIn error = %v, want %v", err, tt.want)
			}
			_, previousErr := sessions.Resolve(t.Context(), previous)
			if tt.want != nil {
				// A failed sign-in changes nothing.
				if previousErr != nil {
					t.Fatalf("failed sign-in ended the previous session: %v", previousErr)
				}
				return
			}
			if account, err := sessions.Resolve(t.Context(), token); err != nil || account.ID != alice.ID {
				t.Fatalf("new session: %+v, %v", account, err)
			}
			// Session fixation: the token the browser sent before must not
			// survive sign-in.
			if token == previous || !errors.Is(previousErr, auth.ErrNoSession) {
				t.Fatalf("previous token still resolves after sign-in: %v", previousErr)
			}
			if err := signIn.SignOut(t.Context(), token); err != nil {
				t.Fatal(err)
			}
			if _, err := sessions.Resolve(t.Context(), token); !errors.Is(err, auth.ErrNoSession) {
				t.Fatalf("token resolves after sign-out: %v", err)
			}
		})
	}
}

// failingStore fails one kind of write and otherwise behaves like fakeStore.
type failingStore struct {
	*fakeStore
	failCreate bool
	failDelete []byte // the token hash whose deletion fails
}

func (f failingStore) CreateSession(ctx context.Context, hash []byte, accountID domain.ID, expiresAt time.Time) error {
	if f.failCreate {
		return errors.New("insert failed")
	}
	return f.fakeStore.CreateSession(ctx, hash, accountID, expiresAt)
}

func (f failingStore) DeleteSession(ctx context.Context, hash []byte) error {
	if f.failDelete != nil && bytes.Equal(hash, f.failDelete) {
		return errors.New("delete failed")
	}
	return f.fakeStore.DeleteSession(ctx, hash)
}

func TestSignInRotationFailures(t *testing.T) {
	hasher, err := auth.NewHasher()
	if err != nil {
		t.Fatal(err)
	}
	const password = "correct horse battery staple"
	hash, err := hasher.Hash(t.Context(), password)
	if err != nil {
		t.Fatal(err)
	}
	alice := domain.Account{ID: domain.ID{7}, Email: "alice@example.com"}
	for _, tt := range []struct {
		name                   string
		failCreate, failDelete bool
	}{
		{"creating the new session fails", true, false},
		{"ending the previous session fails", false, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			base := &fakeStore{sessions: map[string]fakeSession{}}
			previous, _, err := auth.NewSessions(base, time.Now).Create(t.Context(), alice.ID)
			if err != nil {
				t.Fatal(err)
			}
			store := failingStore{fakeStore: base, failCreate: tt.failCreate}
			if tt.failDelete {
				raw, _ := base64.RawURLEncoding.DecodeString(previous)
				sum := sha256.Sum256(raw)
				store.failDelete = sum[:]
			}
			sessions := auth.NewSessions(store, time.Now)
			signIn := auth.NewSignIn(fakeAccounts{account: alice, hash: hash}, hasher, sessions)
			if _, _, err := signIn.SignIn(t.Context(), alice.Email, password, previous); err == nil {
				t.Fatal("sign-in succeeded despite the store failure")
			}
			// The browser keeps its previous session, and no other one exists.
			if _, err := auth.NewSessions(base, time.Now).Resolve(t.Context(), previous); err != nil {
				t.Fatalf("previous session lost: %v", err)
			}
			// The new session was never created, or was undone.
			if len(base.sessions) != 1 {
				t.Fatalf("%d sessions after a failed sign-in, want 1", len(base.sessions))
			}
		})
	}
}
