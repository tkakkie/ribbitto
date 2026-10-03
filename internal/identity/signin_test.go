package identity_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/tkakkie/ribbitto/internal/identity"
	"github.com/tkakkie/ribbitto/internal/kernel"
)

type fakeAccounts struct {
	account identity.Account
	hash    string
	err     error
}

func (f fakeAccounts) AccountCredentials(_ context.Context, email string) (kernel.ID, string, error) {
	if f.err != nil {
		return kernel.ID{}, "", f.err
	}
	if email != f.account.Email {
		return kernel.ID{}, "", identity.ErrNoAccount
	}
	return f.account.ID, f.hash, nil
}

func TestSignIn(t *testing.T) {
	hasher, err := identity.NewHasher()
	if err != nil {
		t.Fatal(err)
	}
	const password = "correct horse battery staple"
	hash, err := hasher.Hash(t.Context(), password)
	if err != nil {
		t.Fatal(err)
	}
	alice := identity.Account{ID: kernel.ID{7}, Email: "alice@example.com", DisplayName: "Alice"}
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
		{"wrong password", t.Context(), fakeAccounts{account: alice, hash: hash}, "alice@example.com", password + "!", identity.ErrInvalidCredentials},
		{"unknown email", t.Context(), fakeAccounts{account: alice, hash: hash}, "bob@example.com", password, identity.ErrInvalidCredentials},
		// With a cancelled context the hasher refuses to work, so ErrBusy
		// proves an unknown email still goes through a verification.
		{"unknown email still verifies", cancelled, fakeAccounts{account: alice, hash: hash}, "bob@example.com", password, identity.ErrBusy},
		{"empty password", t.Context(), fakeAccounts{account: alice, hash: hash}, "alice@example.com", "", identity.ErrInvalidInput},
		{"not an email", t.Context(), fakeAccounts{account: alice, hash: hash}, "alice", password, identity.ErrInvalidInput},
		{"store error", t.Context(), fakeAccounts{err: broken}, "alice@example.com", password, broken},
	} {
		t.Run(tt.name, func(t *testing.T) {
			store := &fakeStore{sessions: map[string]fakeSession{}}
			sessions := identity.NewSessions(store, time.Now, nil)
			previous, _, err := sessions.Create(t.Context(), alice.ID)
			if err != nil {
				t.Fatal(err)
			}
			signIn := identity.NewSignIn(tt.accounts, hasher, sessions)
			token, _, err := signIn.SignIn(tt.ctx, tt.email, tt.password, previous)
			if !errors.Is(err, tt.want) {
				t.Fatalf("SignIn error = %v, want %v", err, tt.want)
			}
			_, _, previousErr := sessions.Resolve(t.Context(), previous)
			if tt.want != nil {
				// A failed sign-in changes nothing.
				if previousErr != nil {
					t.Fatalf("failed sign-in ended the previous session: %v", previousErr)
				}
				return
			}
			if account, _, err := sessions.Resolve(t.Context(), token); err != nil || account.ID != alice.ID {
				t.Fatalf("new session: %+v, %v", account, err)
			}
			// Session fixation: the token the browser sent before must not
			// survive sign-in.
			if token == previous || !errors.Is(previousErr, identity.ErrNoSession) {
				t.Fatalf("previous token still resolves after sign-in: %v", previousErr)
			}
			if err := signIn.SignOut(t.Context(), token); err != nil {
				t.Fatal(err)
			}
			if _, _, err := sessions.Resolve(t.Context(), token); !errors.Is(err, identity.ErrNoSession) {
				t.Fatalf("token resolves after sign-out: %v", err)
			}
		})
	}
}

// failingReplace is a store whose atomic replacement fails.
type failingReplace struct{ *fakeStore }

func (failingReplace) ReplaceSession(context.Context, []byte, []byte, kernel.ID, time.Time) (kernel.ID, bool, error) {
	return kernel.ID{}, false, errors.New("transaction failed")
}

func TestSignInReplacementFailure(t *testing.T) {
	hasher, err := identity.NewHasher()
	if err != nil {
		t.Fatal(err)
	}
	const password = "correct horse battery staple"
	hash, err := hasher.Hash(t.Context(), password)
	if err != nil {
		t.Fatal(err)
	}
	alice := identity.Account{ID: kernel.ID{7}, Email: "alice@example.com"}
	base := &fakeStore{sessions: map[string]fakeSession{}}
	previous, _, err := identity.NewSessions(base, time.Now, nil).Create(t.Context(), alice.ID)
	if err != nil {
		t.Fatal(err)
	}
	signIn := identity.NewSignIn(fakeAccounts{account: alice, hash: hash}, hasher, identity.NewSessions(failingReplace{base}, time.Now, nil))
	if _, _, err := signIn.SignIn(t.Context(), alice.Email, password, previous); err == nil {
		t.Fatal("sign-in succeeded despite the store failure")
	}
	// A failed replacement changes nothing: the browser keeps its session.
	if _, _, err := identity.NewSessions(base, time.Now, nil).Resolve(t.Context(), previous); err != nil || len(base.sessions) != 1 {
		t.Fatalf("previous session: %v; %d sessions, want 1", err, len(base.sessions))
	}
}
