package postgres_test

import (
	"encoding/base64"
	"encoding/hex"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/tkakkie/ribbitto/internal/app/auth"
	"github.com/tkakkie/ribbitto/internal/domain"
	"github.com/tkakkie/ribbitto/internal/infra/postgres"
	"github.com/tkakkie/ribbitto/internal/infra/postgres/pgtest"
)

func TestSessionStore(t *testing.T) {
	t.Parallel()
	pool := pgtest.New(t)
	ctx := t.Context()
	id := pgtest.Account(t, pool, "a@example.com", "A")
	// Start from the database clock: created_at comes from now() in SQL, and
	// the table requires expires_at > created_at.
	var now time.Time
	if err := pool.QueryRow(ctx, "SELECT now()").Scan(&now); err != nil {
		t.Fatal(err)
	}
	sessions := auth.NewSessions(postgres.NewSessionStore(pool), func() time.Time { return now })

	older, _, err := sessions.Create(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Hour)
	newer, _, err := sessions.Create(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	account, err := sessions.Resolve(ctx, newer)
	if err != nil || account != (domain.Account{ID: id, Email: "a@example.com", DisplayName: "A"}) {
		t.Fatalf("Resolve = %+v, %v", account, err)
	}

	// No column of any session row may hold the token, in any encoding.
	rows, err := pool.Query(ctx, "SELECT row_to_json(session)::text FROM session")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var row string
		if err := rows.Scan(&row); err != nil {
			t.Fatal(err)
		}
		for _, token := range []string{older, newer} {
			raw, _ := base64.RawURLEncoding.DecodeString(token)
			for _, form := range []string{token, hex.EncodeToString(raw), base64.StdEncoding.EncodeToString(raw)} {
				if strings.Contains(row, form) {
					t.Fatalf("session row contains the token: %s", row)
				}
			}
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}

	// A duplicate token hash must not leave the hash reachable from the
	// error: PgError.Detail would carry it, and a caller that unwraps and
	// logs the error would log it.
	hash := make([]byte, 32)
	hash[0] = 0xab
	store := postgres.NewSessionStore(pool)
	if err := store.CreateSession(ctx, hash, id, now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	err = store.CreateSession(ctx, hash, id, now.Add(time.Hour))
	var pgErr *pgconn.PgError
	if err == nil || errors.As(err, &pgErr) || strings.Contains(err.Error(), "ab00") {
		t.Fatalf("duplicate token hash error exposes the database error: %v", err)
	}

	// Replacing is atomic: success swaps the rows; a failure (here, a new
	// hash that already exists) leaves the old session in place.
	oldHash, newHash := make([]byte, 32), make([]byte, 32)
	oldHash[0], newHash[0] = 0x01, 0x02
	if err := store.CreateSession(ctx, oldHash, id, now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := store.ReplaceSession(ctx, oldHash, hash, id, now.Add(time.Hour)); err == nil || errors.As(err, &pgErr) {
		t.Fatalf("replacing into an existing hash: %v", err)
	}
	countHash := func(h []byte) (n int) {
		if err := pool.QueryRow(ctx, "SELECT count(*) FROM session WHERE token_hash = $1", h).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	if countHash(oldHash) != 1 {
		t.Fatal("failed replacement deleted the old session")
	}
	if err := store.ReplaceSession(ctx, oldHash, newHash, id, now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if countHash(oldHash) != 0 || countHash(newHash) != 1 {
		t.Fatal("replacement did not swap the sessions")
	}

	// Past the older session's expiry, clean-up removes only that one.
	now = now.Add(auth.SessionLifetime - 30*time.Minute)
	if err := sessions.DeleteExpired(ctx); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM session").Scan(&count); err != nil || count != 1 {
		t.Fatalf("sessions after clean-up = %d, %v; want 1", count, err)
	}
	if _, err := sessions.Resolve(ctx, older); !errors.Is(err, auth.ErrNoSession) {
		t.Fatalf("expired session: %v", err)
	}
	if _, err := sessions.Resolve(ctx, newer); err != nil {
		t.Fatalf("live session after clean-up: %v", err)
	}
	if err := sessions.Delete(ctx, newer); err != nil {
		t.Fatal(err)
	}
	if _, err := sessions.Resolve(ctx, newer); !errors.Is(err, auth.ErrNoSession) {
		t.Fatalf("deleted session: %v", err)
	}
}
