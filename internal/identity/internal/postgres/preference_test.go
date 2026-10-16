package postgres_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/tkakkie/ribbitto/internal/identity"
	"github.com/tkakkie/ribbitto/internal/identity/identitytest"
	"github.com/tkakkie/ribbitto/internal/identity/internal/postgres"
	"github.com/tkakkie/ribbitto/internal/kernel"
	"github.com/tkakkie/ribbitto/internal/platform/postgres/pgtest"
)

// An account that exists before the send key gets Enter, and the session
// lookup carries it.
func TestSendKeyUpgrade(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(t.Context(), 45*time.Second)
	defer cancel()
	pool := pgtest.NewEmpty(t)
	migrator := pgtest.NewMigrator(t, pool)
	migrator.UpTo(ctx, 16)
	hash := make([]byte, 32)
	_, err := pool.Exec(ctx, `
		WITH a AS (INSERT INTO account (email, display_name, password_hash) VALUES ('old@example.org', 'Old', '$argon2id$x') RETURNING id)
		INSERT INTO session (token_hash, account_id, expires_at) SELECT $1, id, now() + interval '1 hour' FROM a`, hash)
	requireNoError(t, err)
	migrator.UpTo(ctx, 17)
	account, _, err := postgres.NewSessionStore(pool).SessionAccount(ctx, hash, time.Now())
	if err != nil || account.Email != "old@example.org" || account.SendKey != identity.SendKeyEnter {
		t.Fatalf("SessionAccount = %+v, %v; want send key %q", account, err, identity.SendKeyEnter)
	}
}

// Setting the send key changes only the account it names.
func TestSetSendKeyStore(t *testing.T) {
	t.Parallel()
	pool := pgtest.New(t)
	ctx := t.Context()
	named := identitytest.Account(t, pool, "named@example.org", "Named")
	other := identitytest.Account(t, pool, "other@example.org", "Other")
	preferences := identity.NewPreferences(postgres.NewAccountStore(pool))
	sendKey := func(id kernel.ID) (key identity.SendKey) {
		requireNoError(t, pool.QueryRow(ctx, "SELECT send_key FROM account WHERE id = $1", id).Scan(&key))
		return key
	}
	requireNoError(t, preferences.SetSendKey(ctx, named, "modifier_enter"))
	if sendKey(named) != identity.SendKeyModifierEnter || sendKey(other) != identity.SendKeyEnter {
		t.Fatalf("send keys = %q, %q; want only the named account changed", sendKey(named), sendKey(other))
	}
	if err := preferences.SetSendKey(ctx, kernel.ID{1}, "modifier_enter"); !errors.Is(err, identity.ErrNoAccount) || sendKey(other) != identity.SendKeyEnter {
		t.Fatalf("unknown account: %v; other's send key %q", err, sendKey(other))
	}
}
