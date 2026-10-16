package identity_test

import (
	"context"
	"errors"
	"testing"

	"github.com/tkakkie/ribbitto/internal/identity"
	"github.com/tkakkie/ribbitto/internal/kernel"
)

// fakePreferenceStore records each send key it is asked to store.
type fakePreferenceStore struct {
	stored map[kernel.ID]identity.SendKey
}

func (f *fakePreferenceStore) SetSendKey(_ context.Context, accountID kernel.ID, key identity.SendKey) error {
	f.stored[accountID] = key
	return nil
}

// Only the two exact values are send keys; anything else is refused before
// the store is called.
func TestSetSendKey(t *testing.T) {
	account := kernel.ID{1}
	for _, tc := range []struct {
		value string
		want  identity.SendKey
	}{
		{"enter", identity.SendKeyEnter},
		{"modifier_enter", identity.SendKeyModifierEnter},
		{"", ""},
		{"Enter", ""},
		{" enter", ""},
		{"modifier_enter ", ""},
		{"ctrl_enter", ""},
	} {
		t.Run(tc.value, func(t *testing.T) {
			parsed, parseErr := identity.ParseSendKey(tc.value)
			store := &fakePreferenceStore{stored: map[kernel.ID]identity.SendKey{}}
			err := identity.NewPreferences(store).SetSendKey(t.Context(), account, tc.value)
			if tc.want == "" {
				if !errors.Is(parseErr, identity.ErrInvalidSendKey) || parsed != "" || !errors.Is(err, identity.ErrInvalidSendKey) || len(store.stored) != 0 {
					t.Fatalf("accepted %q: parsed %q, %v; set %v, stored %v", tc.value, parsed, parseErr, err, store.stored)
				}
				return
			}
			if parseErr != nil || parsed != tc.want || err != nil || len(store.stored) != 1 || store.stored[account] != tc.want {
				t.Fatalf("%q: parsed %q, %v; set %v, stored %v", tc.value, parsed, parseErr, err, store.stored)
			}
		})
	}
}
