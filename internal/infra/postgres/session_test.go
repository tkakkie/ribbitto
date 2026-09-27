package postgres_test

import (
	"encoding/base64"
	"encoding/hex"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/tkakkie/ribbitto/internal/app/auth"
	"github.com/tkakkie/ribbitto/internal/domain"
	"github.com/tkakkie/ribbitto/internal/infra/postgres"
	"github.com/tkakkie/ribbitto/internal/infra/postgres/pgtest"
)

func TestSessionStore(t *testing.T) {
	t.Parallel()
	pool := pgtest.New(t)
	ctx := t.Context()
	var id domain.ID
	if err := pool.QueryRow(ctx, "INSERT INTO account (email, display_name, password_hash) VALUES ('a@example.com', 'A', '$argon2id$x') RETURNING id").Scan(&id); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)
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
