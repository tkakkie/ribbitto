package postgres_test

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
	"github.com/tkakkie/ribbitto/db/migrations"
	"github.com/tkakkie/ribbitto/internal/domain"
	"github.com/tkakkie/ribbitto/internal/infra/postgres/pgtest"
)

// Members that exist before handles get member-<n>, numbered per
// organisation in id order, and nothing else about them changes.
func TestMemberHandleUpgrade(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 45*time.Second)
	defer cancel()
	pool := pgtest.NewEmpty(t)
	db := stdlib.OpenDBFromPool(pool)
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Error(err)
		}
	})
	provider, err := goose.NewProvider(goose.DialectPostgres, db, migrations.FS)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := provider.UpTo(ctx, 3); err != nil {
		t.Fatal(err)
	}
	// Raw SQL creates pre-handle members with adversarial IDs for the upgrade.
	// Ids sharing a long prefix: a placeholder cut from the id would collide.
	_, err = pool.Exec(ctx, `
		INSERT INTO organization (id, slug, name) VALUES
		  ('00000000-0000-7000-8000-00000000000a', 'acme', 'Acme'),
		  ('00000000-0000-7000-8000-00000000000b', 'globex', 'Globex');
		INSERT INTO account (id, email, display_name, password_hash)
		SELECT ('00000000-0000-7000-8000-0000000001' || n)::uuid, 'user' || n || '@example.org', 'User ' || n, '$argon2id$x'
		FROM generate_series(10, 14) AS n;
		INSERT INTO member (id, organization_id, account_id, role, joined_event_seq) VALUES
		  ('00000000-0000-7000-8000-000000000213', '00000000-0000-7000-8000-00000000000a', '00000000-0000-7000-8000-000000000110', 'owner', 1),
		  ('00000000-0000-7000-8000-000000000211', '00000000-0000-7000-8000-00000000000a', '00000000-0000-7000-8000-000000000111', 'member', 2),
		  ('00000000-0000-7000-8000-000000000212', '00000000-0000-7000-8000-00000000000a', '00000000-0000-7000-8000-000000000112', 'member', 3),
		  ('00000000-0000-7000-8000-000000000215', '00000000-0000-7000-8000-00000000000b', '00000000-0000-7000-8000-000000000113', 'owner', 1),
		  ('00000000-0000-7000-8000-000000000214', '00000000-0000-7000-8000-00000000000b', '00000000-0000-7000-8000-000000000114', 'member', 2)`)
	if err != nil {
		t.Fatal(err)
	}
	const snapshot = `SELECT string_agg(concat_ws(':', m.id, m.organization_id, m.account_id, m.role, m.joined_event_seq, m.created_at, a.email, a.display_name, a.password_hash), ',' ORDER BY m.id) FROM member m JOIN account a ON a.id = m.account_id`
	var before, after string
	if err := pool.QueryRow(ctx, snapshot).Scan(&before); err != nil {
		t.Fatal(err)
	}
	if _, err := provider.UpTo(ctx, 4); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, snapshot).Scan(&after); err != nil || after != before {
		t.Fatalf("members or accounts changed: %v\n%s\n%s", err, before, after)
	}
	rows, err := pool.Query(ctx, "SELECT id::text, handle FROM member ORDER BY id")
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for rows.Next() {
		var id, handle string
		if err := rows.Scan(&id, &handle); err != nil {
			t.Fatal(err)
		}
		if valid, err := domain.ValidateHandle(handle); err != nil || valid != handle {
			t.Errorf("handle %q is not canonical: %v", handle, err)
		}
		got[id[len(id)-3:]] = handle
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"211": "member-1", "212": "member-2", "213": "member-3", "214": "member-1", "215": "member-2"}
	if len(got) != len(want) {
		t.Fatalf("handles: %v", got)
	}
	for id, handle := range want {
		if got[id] != handle {
			t.Fatalf("handles: got %v, want %v", got, want)
		}
	}
}
