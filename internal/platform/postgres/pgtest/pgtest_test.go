package pgtest

import (
	"context"
	"crypto/rand"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

func TestIsolationAndCleanup(t *testing.T) {
	admin := adminConnection(t)
	names := make(chan string, 2)
	t.Run("parallel databases", func(t *testing.T) {
		for _, name := range []string{"first", "second"} {
			t.Run(name, func(t *testing.T) {
				t.Parallel()
				pool := New(t)
				names <- pool.Config().ConnConfig.Database
				_, err := pool.Exec(t.Context(), "INSERT INTO organization (slug, name) VALUES ('same', 'Test')")
				require(t, err)
				var count int
				require(t, pool.QueryRow(t.Context(), "SELECT count(*) FROM organization").Scan(&count))
				if count != 1 {
					t.Fatalf("row count = %d, want 1", count)
				}
			})
		}
	})
	close(names)
	previous := ""
	for name := range names {
		if name == previous {
			t.Fatalf("tests reused database %s", name)
		}
		previous = name
		var exists bool
		require(t, admin.QueryRow(t.Context(), "SELECT EXISTS (SELECT FROM pg_database WHERE datname = $1)", name).Scan(&exists))
		if exists {
			t.Fatalf("database %s survived cleanup", name)
		}
	}
}

func TestTemplateRecovery(t *testing.T) {
	for _, interrupted := range []bool{false, true} {
		t.Run(map[bool]string{false: "half-built", true: "interrupted publication"}[interrupted], func(t *testing.T) {
			admin, name, ctx := templateFixture(t)
			if interrupted {
				stopped := errors.New("interrupted after rename")
				interruptedAdmin := adminConnection(t)
				err := initialize(ctx, interruptedAdmin, name, func() error {
					require(t, interruptedAdmin.Close(ctx))
					return stopped
				})
				if !errors.Is(err, stopped) {
					t.Fatalf("interruption: %v", err)
				}
			} else {
				// Recovery must also remove leftovers that were marked as templates.
				require(t, execute(ctx, admin, "CREATE DATABASE "+identifier(name+"_building_old")+" TEMPLATE template0 IS_TEMPLATE true"))
			}
			require(t, initialize(ctx, admin, name, nil))
			assertPublished(t, admin, name)
			var leftovers int
			require(t, admin.QueryRow(ctx, "SELECT count(*) FROM pg_database WHERE starts_with(datname, $1)", name+"_building_").Scan(&leftovers))
			if leftovers != 0 {
				t.Fatalf("unfinished templates remaining: %d", leftovers)
			}
		})
	}
}

func TestConcurrentInitializers(t *testing.T) {
	observer, name, ctx := templateFixture(t)
	first, second := adminConnection(t), adminConnection(t)
	publishing, release := make(chan struct{}), make(chan struct{}, 1)
	defer close(release)
	results := make(chan error, 2)
	go func() {
		results <- initialize(ctx, first, name, func() error {
			close(publishing)
			select {
			case <-release:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		})
	}()
	select {
	case <-publishing:
	case err := <-results:
		t.Fatalf("first initializer did not reach publication: %v", err)
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	go func() { results <- initialize(ctx, second, name, nil) }()
	// Observe actual database lock contention before letting publication finish.
	for {
		var waiting bool
		require(t, observer.QueryRow(ctx, "SELECT EXISTS (SELECT FROM pg_locks WHERE pid = $1 AND locktype = 'advisory' AND NOT granted)", second.PgConn().PID()).Scan(&waiting))
		if waiting {
			break
		}
	}
	release <- struct{}{}
	for range 2 {
		require(t, <-results)
	}
	assertPublished(t, observer, name)
}

func templateFixture(t *testing.T) (*pgx.Conn, string, context.Context) {
	t.Helper()
	admin := adminConnection(t)
	name := "ribbitto_tmpl_" + strings.ToLower(rand.Text()[:12])
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	t.Cleanup(cancel)
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		require(t, execute(cleanup, admin, "ALTER DATABASE "+identifier(name)+" WITH IS_TEMPLATE false"))
		require(t, execute(cleanup, admin, "DROP DATABASE "+identifier(name)+" WITH (FORCE)"))
	})
	return admin, name, ctx
}

func assertPublished(t *testing.T, admin *pgx.Conn, name string) {
	t.Helper()
	var template, allow bool
	require(t, admin.QueryRow(t.Context(), "SELECT datistemplate, datallowconn FROM pg_database WHERE datname = $1", name).Scan(&template, &allow))
	if !template || allow {
		t.Fatalf("template flags: datistemplate=%t datallowconn=%t", template, allow)
	}
}
