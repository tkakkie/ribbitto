package identity

import (
	"context"
	"errors"
	"fmt"

	"github.com/tkakkie/ribbitto/internal/kernel"
)

// SendKey is which key sends a message from the composer. It belongs to the
// person, not to a membership, so one choice applies in every organisation
// and on every device (#1158).
type SendKey string

const (
	// SendKeyEnter sends on Enter; it is every account's default.
	SendKeyEnter SendKey = "enter"
	// SendKeyModifierEnter sends on Ctrl+Enter, or ⌘+Enter on a Mac.
	SendKeyModifierEnter SendKey = "modifier_enter"
)

// ErrInvalidSendKey means the value names no send key; nothing was stored.
var ErrInvalidSendKey = errors.New("send key is invalid")

// ParseSendKey returns the send key value names exactly, or
// ErrInvalidSendKey. The database's CHECK allows the same two values.
func ParseSendKey(value string) (SendKey, error) {
	switch key := SendKey(value); key {
	case SendKeyEnter, SendKeyModifierEnter:
		return key, nil
	default:
		return "", ErrInvalidSendKey
	}
}

// PreferenceStore writes an account's preferences.
type PreferenceStore interface {
	// SetSendKey stores the send key on the account with this ID, or
	// returns ErrNoAccount when there is none.
	SetSendKey(ctx context.Context, accountID kernel.ID, key SendKey) error
}

// Preferences changes an account's own preferences.
type Preferences struct {
	store PreferenceStore
}

// NewPreferences returns Preferences backed by store.
func NewPreferences(store PreferenceStore) *Preferences {
	return &Preferences{store: store}
}

// SetSendKey validates value and stores it on the account. The caller passes
// the signed-in account's own ID, never one taken from the request: there is
// no other check of who may change it.
func (p *Preferences) SetSendKey(ctx context.Context, accountID kernel.ID, value string) error {
	key, err := ParseSendKey(value)
	if err != nil {
		return err
	}
	if err := p.store.SetSendKey(ctx, accountID, key); err != nil {
		return fmt.Errorf("setting send key: %w", err)
	}
	return nil
}
