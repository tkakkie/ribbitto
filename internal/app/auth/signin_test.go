package auth_test

import (
	"context"
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
