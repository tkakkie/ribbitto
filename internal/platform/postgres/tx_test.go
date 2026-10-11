package postgres_test

import (
	"context"
	"errors"
	"io"
	"net"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/tkakkie/ribbitto/internal/platform/postgres"
	"github.com/tkakkie/ribbitto/internal/platform/postgres/pgtest"
	"github.com/tkakkie/ribbitto/internal/platform/postgres/pgxbridge"
)

// organizations counts rows with slug, outside any test transaction.
func organizations(t *testing.T, pool *pgxpool.Pool, slug string) int {
	t.Helper()
	var n int
	requireNoError(t, pool.QueryRow(t.Context(), "SELECT count(*) FROM organization WHERE slug = $1", slug).Scan(&n))
	return n
}

func insertOrganization(ctx context.Context, tx postgres.Tx, slug string) error {
	_, err := pgxbridge.Tx(tx).Exec(ctx, "INSERT INTO organization (slug, name) VALUES ($1, 'Test')", slug)
	return err
}

// The runners follow pgx.BeginFunc: commit on success, roll back and return
// the callback's own error, roll back and re-panic with the original value.
func TestInTx(t *testing.T) {
	t.Parallel()
	pool := pgtest.New(t)
	ctx := t.Context()

	t.Run("commits on success", func(t *testing.T) {
		requireNoError(t, postgres.InTx(ctx, pool, func(tx postgres.Tx) error { return insertOrganization(ctx, tx, "committed") }))
		if n := organizations(t, pool, "committed"); n != 1 {
			t.Fatalf("%d rows after commit, want 1", n)
		}
	})
	t.Run("rolls back and returns the callback's error", func(t *testing.T) {
		failure := errors.New("callback failed")
		err := postgres.InTx(ctx, pool, func(tx postgres.Tx) error {
			requireNoError(t, insertOrganization(ctx, tx, "failed"))
			return failure
		})
		if err != failure {
			t.Fatalf("InTx = %v, want the callback's own error", err)
		}
		if n := organizations(t, pool, "failed"); n != 0 {
			t.Fatalf("%d rows after a failed callback, want 0", n)
		}
	})
	t.Run("rolls back and re-panics with the original value", func(t *testing.T) {
		type sentinel struct{ reason string }
		value := &sentinel{"boom"}
		func() {
			defer func() {
				if got := recover(); got != value {
					t.Fatalf("recovered %v, want the original panic value", got)
				}
			}()
			_ = postgres.InTx(ctx, pool, func(tx postgres.Tx) error {
				requireNoError(t, insertOrganization(ctx, tx, "panicked"))
				panic(value)
			})
			t.Fatal("InTx returned instead of panicking")
		}()
		if n := organizations(t, pool, "panicked"); n != 0 {
			t.Fatalf("%d rows after a panic, want 0", n)
		}
	})
	t.Run("one transaction across unwraps", func(t *testing.T) {
		requireNoError(t, postgres.InTx(ctx, pool, func(tx postgres.Tx) error {
			var first, second int64
			requireNoError(t, pgxbridge.Tx(tx).QueryRow(ctx, "SELECT txid_current()").Scan(&first))
			requireNoError(t, pgxbridge.Tx(tx).QueryRow(ctx, "SELECT txid_current()").Scan(&second))
			if first != second {
				t.Errorf("transaction ids %d and %d, want one transaction", first, second)
			}
			return nil
		}))
	})
	t.Run("returns a begin failure", func(t *testing.T) {
		cancelled, cancel := context.WithCancel(ctx)
		cancel()
		called := false
		err := postgres.InTx(cancelled, pool, func(postgres.Tx) error { called = true; return nil })
		if err == nil || called {
			t.Fatalf("InTx on a cancelled context = %v, callback called = %t", err, called)
		}
	})
}

// A cancellation after fn succeeds cannot fail the commit: InTx would roll
// back and report it, InTxDetachedCommit commits and reports success.
func TestInTxDetachedCommit(t *testing.T) {
	t.Parallel()
	pool := pgtest.New(t)
	detached := func(ctx context.Context, p *pgxpool.Pool, fn func(postgres.Tx) error) error {
		return postgres.InTxDetachedCommit(ctx, p, time.Minute, fn)
	}
	for _, tc := range []struct {
		name, slug string
		run        func(context.Context, *pgxpool.Pool, func(postgres.Tx) error) error
		commit     bool
	}{
		{"InTx", "cancelled-intx", postgres.InTx, false},
		{"InTxDetachedCommit", "cancelled-detached", detached, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			slug := tc.slug
			err := tc.run(ctx, pool, func(tx postgres.Tx) error {
				requireNoError(t, insertOrganization(ctx, tx, slug))
				cancel() // The request ends after the write, before COMMIT.
				return nil
			})
			if n := organizations(t, pool, slug); (err == nil) != tc.commit || (n == 1) != tc.commit {
				t.Fatalf("error %v with %d rows, want committed=%t reported as such", err, n, tc.commit)
			}
		})
	}
	t.Run("callback error still rolls back", func(t *testing.T) {
		failure := errors.New("callback failed")
		err := postgres.InTxDetachedCommit(t.Context(), pool, time.Minute, func(tx postgres.Tx) error {
			requireNoError(t, insertOrganization(t.Context(), tx, "detached-failed"))
			return failure
		})
		if err != failure || organizations(t, pool, "detached-failed") != 0 {
			t.Fatalf("InTxDetachedCommit = %v, want the callback's error and no row", err)
		}
	})

	// A deferred trigger runs at COMMIT: id 1 stalls it, id 2 makes the server
	// reject it.
	_, err := pool.Exec(t.Context(), `CREATE TABLE commit_probe (id int);
CREATE FUNCTION commit_probe() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
	IF NEW.id = 1 THEN PERFORM pg_sleep(30); END IF;
	IF NEW.id = 2 THEN RAISE EXCEPTION 'rejected at commit'; END IF;
	RETURN NULL;
END $$;
CREATE CONSTRAINT TRIGGER commit_probe AFTER INSERT ON commit_probe
	DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION commit_probe();`)
	requireNoError(t, err)
	probe := func(ctx context.Context, id int, limit time.Duration) error {
		return postgres.InTxDetachedCommit(ctx, pool, limit, func(tx postgres.Tx) error {
			_, err := pgxbridge.Tx(tx).Exec(ctx, "INSERT INTO commit_probe VALUES ($1)", id)
			return err
		})
	}
	rows := func(id int) int {
		var n int
		requireNoError(t, pool.QueryRow(t.Context(), "SELECT count(*) FROM commit_probe WHERE id = $1", id).Scan(&n))
		return n
	}
	released := func(t *testing.T, p *pgxpool.Pool) {
		t.Helper()
		// pgx closes the timed-out connection and the pool destroys it in
		// the background, so it is released shortly after the error.
		for until := time.Now().Add(2 * time.Second); p.Stat().AcquiredConns() != 0; time.Sleep(10 * time.Millisecond) {
			if time.Now().After(until) {
				t.Fatalf("%d connections still acquired after the limit", p.Stat().AcquiredConns())
			}
		}
	}
	t.Run("unknown outcome that did not commit", func(t *testing.T) {
		// COMMIT stalls in the deferred trigger; the limit expires, then the
		// server-side transaction is terminated, so it never commits.
		var pid int32
		started := time.Now()
		err := postgres.InTxDetachedCommit(t.Context(), pool, 200*time.Millisecond, func(tx postgres.Tx) error {
			requireNoError(t, pgxbridge.Tx(tx).QueryRow(t.Context(), "SELECT pg_backend_pid()").Scan(&pid))
			_, err := pgxbridge.Tx(tx).Exec(t.Context(), "INSERT INTO commit_probe VALUES (1)")
			return err
		})
		if !errors.Is(err, postgres.ErrCommitUnknown) || time.Since(started) > time.Second {
			t.Fatalf("stalled commit = %v after %s, want ErrCommitUnknown within the limit", err, time.Since(started))
		}
		released(t, pool)
		_, err = pool.Exec(t.Context(), "SELECT pg_terminate_backend($1)", pid)
		requireNoError(t, err)
		for until := time.Now().Add(5 * time.Second); ; time.Sleep(20 * time.Millisecond) {
			var alive bool
			requireNoError(t, pool.QueryRow(t.Context(), "SELECT EXISTS (SELECT FROM pg_stat_activity WHERE pid = $1)", pid).Scan(&alive))
			if !alive {
				break
			}
			if time.Now().After(until) {
				t.Fatal("the stalled backend did not end")
			}
		}
		if rows(1) != 0 {
			t.Fatal("a terminated transaction committed")
		}
	})
	t.Run("unknown outcome that committed", func(t *testing.T) {
		// The proxy forwards COMMIT to the server but drops its reply, so the
		// server commits while the client sees only the limit expire.
		proxy := newReplyDropper(t, pool)
		started := time.Now()
		err := postgres.InTxDetachedCommit(t.Context(), proxy.pool, 300*time.Millisecond, func(tx postgres.Tx) error {
			_, err := pgxbridge.Tx(tx).Exec(t.Context(), "INSERT INTO commit_probe VALUES (3)")
			proxy.drop.Store(true)
			return err
		})
		if !errors.Is(err, postgres.ErrCommitUnknown) || time.Since(started) > 2*time.Second {
			t.Fatalf("unanswered commit = %v after %s, want ErrCommitUnknown within the limit", err, time.Since(started))
		}
		released(t, proxy.pool)
		for until := time.Now().Add(5 * time.Second); rows(3) == 0; time.Sleep(20 * time.Millisecond) {
			if time.Now().After(until) {
				t.Fatal("the unanswered COMMIT never committed on the server")
			}
		}
	})
	t.Run("a ROLLBACK reply is not unknown", func(t *testing.T) {
		// A failed statement aborts the transaction; COMMIT then gets ROLLBACK.
		err := postgres.InTxDetachedCommit(t.Context(), pool, time.Minute, func(tx postgres.Tx) error {
			_, _ = pgxbridge.Tx(tx).Exec(t.Context(), "SELECT 1/0")
			return nil
		})
		if !errors.Is(err, pgx.ErrTxCommitRollback) || errors.Is(err, postgres.ErrCommitUnknown) {
			t.Fatalf("aborted commit = %v, want pgx.ErrTxCommitRollback, not unknown", err)
		}
	})
	t.Run("a commit the server rejects is not unknown", func(t *testing.T) {
		err := probe(t.Context(), 2, time.Minute)
		var pgErr *pgconn.PgError
		if errors.Is(err, postgres.ErrCommitUnknown) || !errors.As(err, &pgErr) || rows(2) != 0 {
			t.Fatalf("rejected commit = %v, want the server's error and no row", err)
		}
	})
}

func TestInSnapshot(t *testing.T) {
	t.Parallel()
	pool := pgtest.New(t)
	ctx := t.Context()

	t.Run("refuses writes", func(t *testing.T) {
		err := postgres.InSnapshot(ctx, pool, func(s postgres.Snapshot) error {
			_, err := pgxbridge.Snapshot(s).Exec(ctx, "INSERT INTO organization (slug, name) VALUES ('written', 'Test')")
			return err
		})
		var pgErr *pgconn.PgError
		if !errors.As(err, &pgErr) || pgErr.Code != "25006" {
			t.Fatalf("write in a snapshot = %v, want read_only_sql_transaction (25006)", err)
		}
	})
	t.Run("sees one snapshot while others commit", func(t *testing.T) {
		requireNoError(t, postgres.InSnapshot(ctx, pool, func(s postgres.Snapshot) error {
			count := func() int {
				var n int
				requireNoError(t, pgxbridge.Snapshot(s).QueryRow(ctx, "SELECT count(*) FROM organization").Scan(&n))
				return n
			}
			before := count()
			requireNoError(t, postgres.InTx(ctx, pool, func(tx postgres.Tx) error { return insertOrganization(ctx, tx, "concurrent") }))
			if after := count(); after != before {
				t.Errorf("snapshot saw %d then %d organizations, want one snapshot", before, after)
			}
			return nil
		}))
		if n := organizations(t, pool, "concurrent"); n != 1 {
			t.Fatalf("the concurrent commit is missing outside the snapshot: %d rows", n)
		}
	})
}

// replyDropper is a one-pool TCP proxy to the test database that can stop
// delivering the server's replies while still forwarding the client's.
type replyDropper struct {
	pool *pgxpool.Pool
	drop atomic.Bool
}

func newReplyDropper(t *testing.T, direct *pgxpool.Pool) *replyDropper {
	t.Helper()
	config := direct.Config()
	target := net.JoinHostPort(config.ConnConfig.Host, strconv.Itoa(int(config.ConnConfig.Port)))
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	requireNoError(t, err)
	t.Cleanup(func() { _ = listener.Close() })
	d := &replyDropper{}
	go func() {
		for {
			client, err := listener.Accept()
			if err != nil {
				return
			}
			server, err := net.Dial("tcp", target)
			if err != nil {
				_ = client.Close()
				continue
			}
			go func() { _, _ = io.Copy(server, client); _ = server.Close() }()
			go func() {
				defer func() { _ = client.Close() }()
				buf := make([]byte, 32<<10)
				for {
					n, err := server.Read(buf)
					if n > 0 && !d.drop.Load() {
						if _, err := client.Write(buf[:n]); err != nil {
							return
						}
					}
					if err != nil {
						return
					}
				}
			}()
		}
	}()
	address := listener.Addr().(*net.TCPAddr)
	config.ConnConfig.Host, config.ConnConfig.Port = "127.0.0.1", uint16(address.Port)
	config.ConnConfig.Fallbacks = nil
	config.MaxConns = 1
	d.pool, err = pgxpool.NewWithConfig(t.Context(), config)
	requireNoError(t, err)
	t.Cleanup(d.pool.Close)
	return d
}
