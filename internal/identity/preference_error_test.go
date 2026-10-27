package identity_test

import (
	"context"
	"errors"
	"testing"

	"github.com/tkakkie/ribbitto/internal/identity"
	"github.com/tkakkie/ribbitto/internal/kernel"
)

type failingPreferenceStore struct {
	err       error
	calls     int
	accountID kernel.ID
	key       identity.SendKey
}

func (f *failingPreferenceStore) SetSendKey(_ context.Context, accountID kernel.ID, key identity.SendKey) error {
	f.calls++
	f.accountID = accountID
	f.key = key
	return f.err
}

func TestSetSendKeyStoreError(t *testing.T) {
	account := kernel.ID{1}

	for _, tc := range []struct {
		name        string
		value       string
		storeErr    error
		wantErr     error
		wantText    string
		wantCalls   int
		wantAccount kernel.ID
		wantKey     identity.SendKey
	}{
		{
			name:        "store error",
			value:       "enter",
			storeErr:    identity.ErrNoAccount,
			wantErr:     identity.ErrNoAccount,
			wantText:    "setting send key: no such account",
			wantCalls:   1,
			wantAccount: account,
			wantKey:     identity.SendKeyEnter,
		},
		{
			name:      "invalid value skips store",
			value:     "Enter",
			storeErr:  identity.ErrNoAccount,
			wantErr:   identity.ErrInvalidSendKey,
			wantText:  "send key is invalid",
			wantCalls: 0,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := &failingPreferenceStore{err: tc.storeErr}
			err := identity.NewPreferences(store).SetSendKey(t.Context(), account, tc.value)
			if err == nil || err.Error() != tc.wantText {
				t.Errorf("%s: err = %v, want exact %q", tc.name, err, tc.wantText)
			}
			if !errors.Is(err, tc.wantErr) {
				t.Errorf("%s: err = %v, want errors.Is(_, %v)", tc.name, err, tc.wantErr)
			}
			if store.calls != tc.wantCalls {
				t.Errorf("%s: store.calls = %d, want %d", tc.name, store.calls, tc.wantCalls)
			}
			if store.accountID != tc.wantAccount {
				t.Errorf("%s: store.accountID = %v, want %v", tc.name, store.accountID, tc.wantAccount)
			}
			if store.key != tc.wantKey {
				t.Errorf("%s: store.key = %q, want %q", tc.name, store.key, tc.wantKey)
			}
		})
	}
}
