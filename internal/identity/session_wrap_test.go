package identity_test

import (
	"errors"
	"testing"
	"time"

	"github.com/tkakkie/ribbitto/internal/identity"
	"github.com/tkakkie/ribbitto/internal/kernel"
)

func TestSessionsWrapStoreErrors(t *testing.T) {
	account := kernel.ID{1}
	broken := errors.New("connection refused")

	for _, tc := range []struct {
		name     string
		wantText string
		call     func(t *testing.T, sessions *identity.Sessions, store *fakeStore, now time.Time) error
	}{
		{
			name:     "Create",
			wantText: "storing session: connection refused",
			call: func(t *testing.T, sessions *identity.Sessions, store *fakeStore, _ time.Time) error {
				store.err = broken
				token, expiresAt, err := sessions.Create(t.Context(), account)
				if token != "" || !expiresAt.IsZero() {
					t.Errorf("Create: got token=%q expiresAt=%v, want empty token and zero time", token, expiresAt)
				}
				return err
			},
		},
		{
			name:     "Replace",
			wantText: "replacing session: connection refused",
			call: func(t *testing.T, sessions *identity.Sessions, store *fakeStore, _ time.Time) error {
				prev, _, err := sessions.Create(t.Context(), account)
				if err != nil {
					t.Fatalf("Replace setup Create: %v", err)
				}
				store.mutationErr = broken
				token, expiresAt, err := sessions.Replace(t.Context(), prev, account)
				if token != "" || !expiresAt.IsZero() {
					t.Errorf("Replace: got token=%q expiresAt=%v, want empty token and zero expiry", token, expiresAt)
				}
				return err
			},
		},
		{
			name:     "Delete",
			wantText: "deleting session: connection refused",
			call: func(t *testing.T, sessions *identity.Sessions, store *fakeStore, _ time.Time) error {
				prev, _, err := sessions.Create(t.Context(), account)
				if err != nil {
					t.Fatalf("Delete setup Create: %v", err)
				}
				store.mutationErr = broken
				return sessions.Delete(t.Context(), prev)
			},
		},
		{
			name:     "Resolve",
			wantText: "resolving session: connection refused",
			call: func(t *testing.T, sessions *identity.Sessions, store *fakeStore, _ time.Time) error {
				prev, _, err := sessions.Create(t.Context(), account)
				if err != nil {
					t.Fatalf("Resolve setup Create: %v", err)
				}
				store.err = broken
				gotAccount, gotSession, err := sessions.Resolve(t.Context(), prev)
				if gotAccount != (identity.Account{}) || gotSession != (identity.Session{}) {
					t.Errorf("Resolve: got account=%+v session=%+v, want zero Account and Session", gotAccount, gotSession)
				}
				if errors.Is(err, identity.ErrNoSession) {
					t.Errorf("Resolve: err = %v, must not match ErrNoSession", err)
				}
				return err
			},
		},
		{
			name:     "DeleteExpired",
			wantText: "cleaning up expired sessions: connection refused",
			call: func(t *testing.T, sessions *identity.Sessions, store *fakeStore, now time.Time) error {
				store.err = broken
				err := sessions.DeleteExpired(t.Context())
				if !store.before.Equal(now) {
					t.Errorf("DeleteExpired: store.before = %v, want %v", store.before, now)
				}
				return err
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			now := time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)
			store := &fakeStore{sessions: map[string]fakeSession{}}
			sessions := identity.NewSessions(store, func() time.Time { return now }, nil)

			err := tc.call(t, sessions, store, now)
			if err == nil || err.Error() != tc.wantText {
				t.Errorf("%s: err = %v, want exact %q", tc.name, err, tc.wantText)
			}
			if !errors.Is(err, broken) {
				t.Errorf("%s: err = %v, want errors.Is(_, %v)", tc.name, err, broken)
			}
		})
	}
}

func TestDeleteExpiredSuccess(t *testing.T) {
	t0 := time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)
	now := t0
	store := &fakeStore{sessions: map[string]fakeSession{}}
	sessions := identity.NewSessions(store, func() time.Time { return now }, nil)

	if err := sessions.DeleteExpired(t.Context()); err != nil {
		t.Errorf("first call: DeleteExpired err = %v, want nil", err)
	}
	if !store.before.Equal(t0) {
		t.Errorf("first call: store.before = %v, want %v", store.before, t0)
	}

	t1 := t0.Add(time.Hour)
	now = t1
	if err := sessions.DeleteExpired(t.Context()); err != nil {
		t.Errorf("second call: DeleteExpired err = %v, want nil", err)
	}
	if !store.before.Equal(t1) {
		t.Errorf("second call: store.before = %v, want %v", store.before, t1)
	}
}
