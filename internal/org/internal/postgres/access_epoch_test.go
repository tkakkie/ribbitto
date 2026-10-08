package postgres_test

import (
	"context"
	"errors"
	"reflect"
	"regexp"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/tkakkie/ribbitto/internal/identity/identitytest"
	"github.com/tkakkie/ribbitto/internal/kernel"
	"github.com/tkakkie/ribbitto/internal/org"
	"github.com/tkakkie/ribbitto/internal/org/internal/postgres"
	"github.com/tkakkie/ribbitto/internal/org/internal/postgres/sqlcgen"
	"github.com/tkakkie/ribbitto/internal/org/orgtest"
	platform "github.com/tkakkie/ribbitto/internal/platform/postgres"
	"github.com/tkakkie/ribbitto/internal/platform/postgres/pgtest"
	"github.com/tkakkie/ribbitto/internal/platform/postgres/pgxbridge"
)

func accessEpoch(t *testing.T, db sqlcgen.DBTX, id kernel.ID) int64 {
	t.Helper()
	var epoch int64
	requireNoError(t, db.QueryRow(t.Context(), "SELECT access_epoch FROM organization WHERE id=$1", id).Scan(&epoch))
	return epoch
}

func TestAccessEpochWrites(t *testing.T) {
	for _, tc := range []struct {
		name, sql string
		bumps     [2]bool
		refuses   bool
	}{
		{"delete", "DELETE FROM member WHERE id=@member", [2]bool{true, false}, false},
		{"role", "UPDATE member SET role='owner' WHERE id=@member", [2]bool{true, false}, false},
		{"organization", "UPDATE member SET organization_id=@other WHERE id=@member", [2]bool{true, true}, false},
		{"account", "UPDATE member SET account_id=@account WHERE id=@member", [2]bool{true, false}, false},
		{"slug", "UPDATE organization SET slug='renamed' WHERE id=@org", [2]bool{true, false}, false},
		{"truncate", "TRUNCATE member CASCADE", [2]bool{true, true}, false},
		{"cascade", "TRUNCATE account CASCADE", [2]bool{true, true}, false},
		{"member-id", "UPDATE member SET id=uuidv7() WHERE id=@member", [2]bool{}, true},
		{"organization-id", "UPDATE organization SET id=uuidv7() WHERE id=@other", [2]bool{}, true},
		{"manual-bump", "UPDATE organization SET access_epoch=9223372036854775807 WHERE id=@org", [2]bool{true, false}, false},
		{"lower", "UPDATE organization SET access_epoch=access_epoch-1 WHERE id=@org", [2]bool{}, true},
		{"lower-and-rename", "UPDATE organization SET access_epoch=access_epoch-1, slug='renamed' WHERE id=@org", [2]bool{}, true},
		{"same-values", "UPDATE member SET id=id, role=role, organization_id=organization_id, account_id=account_id WHERE id=@member", [2]bool{}, false},
		{"same-slug", "UPDATE organization SET id=id, slug=slug, access_epoch=access_epoch WHERE id=@org", [2]bool{}, false},
		{"posting", "", [2]bool{}, false},
		{"retention", "", [2]bool{}, false},
		{"neither", "UPDATE member SET joined_event_seq=2, created_at=now() WHERE id=@member", [2]bool{}, false},
		{"name", "UPDATE organization SET name='New', created_at=now() WHERE id=@org", [2]bool{}, false},
		{"handle", "", [2]bool{}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pool := pgtest.New(t)
			ctx := t.Context()
			ids := [2]kernel.ID{orgtest.Organization(t, pool, "acme", "Acme", 1), orgtest.Organization(t, pool, "other", "Other", 0)}
			account := identitytest.Account(t, pool, "one@example.org", "One")
			replacement := identitytest.Account(t, pool, "two@example.org", "Two")
			member := orgtest.Member(t, pool, ids[0], account, org.RoleMember, "one", 1)
			before := [2]int64{accessEpoch(t, pool, ids[0]), accessEpoch(t, pool, ids[1])}
			rollback := errors.New("intentional rollback")
			for _, commit := range []bool{false, true} {
				after := before
				err := platform.InTx(ctx, pool, func(handle platform.Tx) error {
					tx := pgxbridge.Tx(handle)
					var err error
					switch tc.name {
					case "posting":
						var seq int64
						seq, err = postgres.SequenceIn(handle).NextEventSeq(ctx, ids[0])
						if err == nil {
							// The log belongs to realtime; complete the deferred sequence constraint here.
							_, err = tx.Exec(ctx, "INSERT INTO event_log (organization_id,seq,kind,data) VALUES ($1,$2,'member.joined','{}')", ids[0], seq)
						}
					case "retention":
						boundary := postgres.RetentionBoundaryIn(handle)
						err = boundary.LockForRetention(ctx, ids[0])
						if err == nil {
							err = boundary.RaiseBoundary(ctx, ids[0], 1)
						}
					case "handle":
						err = postgres.NewMemberStore(tx).UpdateHandle(ctx, ids[0], member, "changed")
					default:
						_, err = tx.Exec(ctx, tc.sql, pgx.NamedArgs{"org": ids[0], "other": ids[1], "member": member, "account": replacement})
					}
					if tc.refuses {
						var pgErr *pgconn.PgError
						if !errors.As(err, &pgErr) || pgErr.Code != "23514" {
							t.Fatalf("want refusal, got %v", err)
						}
					} else {
						requireNoError(t, err)
						for i, id := range ids {
							got := accessEpoch(t, tx, id)
							if tc.bumps[i] && (got <= before[1] || got == 9223372036854775807) || !tc.bumps[i] && got != before[i] {
								t.Fatalf("in transaction: epoch[%d]=%d, before=%v", i, got, before)
							}
						}
					}
					if commit && !tc.refuses {
						after = [2]int64{accessEpoch(t, tx, ids[0]), accessEpoch(t, tx, ids[1])}
						return nil
					}
					return rollback
				})
				if !errors.Is(err, rollback) {
					requireNoError(t, err)
				}
				for i, id := range ids {
					if got := accessEpoch(t, pool, id); got != after[i] {
						t.Fatalf("after commit=%t: epoch=%d, want %d", commit, got, after[i])
					}
				}
			}
		})
	}
}

func TestAccessEpochRecreationAndMigration(t *testing.T) {
	pool := pgtest.NewEmpty(t)
	ctx := t.Context()
	migrator := pgtest.NewMigrator(t, pool)
	migrator.UpTo(ctx, 9)
	id := orgtest.Organization(t, pool, "acme", "Acme", 0)
	migrator.Up(ctx)
	before := accessEpoch(t, pool, id)
	other := orgtest.Organization(t, pool, "other", "Other", 0)
	latest := accessEpoch(t, pool, other)
	tx, err := pool.Begin(ctx)
	requireNoError(t, err)
	defer func() { _ = tx.Rollback(ctx) }()
	_, err = tx.Exec(ctx, "TRUNCATE organization RESTART IDENTITY CASCADE")
	requireNoError(t, err)
	_, err = tx.Exec(ctx, "INSERT INTO organization (id,slug,name,access_epoch) VALUES ($1,'acme','Acme',$2)", id, before)
	requireNoError(t, err)
	after := accessEpoch(t, tx, id)
	requireNoError(t, tx.Commit(ctx))
	if before <= 0 || after <= latest || after <= before || accessEpoch(t, pool, id) != after {
		t.Fatalf("recreated epoch=%d, old=%d, latest=%d", after, before, latest)
	}
	migrator.Down(ctx)
	var removed bool
	requireNoError(t, pool.QueryRow(ctx, "SELECT to_regclass('organization_access_epoch_seq') IS NULL AND NOT EXISTS (SELECT FROM information_schema.columns WHERE table_name='organization' AND column_name='access_epoch') AND NOT EXISTS (SELECT FROM pg_proc WHERE proname IN ('organization_access_guard','member_access_changed','member_access_truncated'))").Scan(&removed))
	if !removed {
		t.Fatal("down left access epoch objects")
	}
	migrator.Up(ctx)
}

func TestAccessEpochColumnClassification(t *testing.T) {
	pool := pgtest.New(t)
	for table, classes := range map[string]map[string]string{
		"member":       {"role": "access", "organization_id": "access", "account_id": "access", "id": "immutable", "handle": "neither", "joined_event_seq": "neither", "created_at": "neither"},
		"organization": {"slug": "access", "access_epoch": "access", "id": "immutable", "name": "neither", "event_seq": "neither", "event_log_boundary_seq": "neither", "created_at": "neither"},
	} {
		rows, err := pool.Query(t.Context(), "SELECT column_name FROM information_schema.columns WHERE table_schema='public' AND table_name=$1", table)
		requireNoError(t, err)
		defer rows.Close()
		got := map[string]string{}
		for rows.Next() {
			var column string
			requireNoError(t, rows.Scan(&column))
			got[column] = classes[column]
		}
		requireNoError(t, rows.Err())
		if !reflect.DeepEqual(got, classes) {
			t.Fatalf("classify every %s column: got %v, want %v", table, got, classes)
		}
		want := map[string]bool{}
		for column, class := range classes {
			if class == "access" || class == "immutable" {
				want[column] = true
			}
		}
		trigger := table + "_access_guard"
		if table == "member" {
			trigger = "member_access_changed"
		}
		var columns []string
		var body string
		requireNoError(t, pool.QueryRow(t.Context(), `
			SELECT ARRAY(SELECT a.attname::text FROM unnest(t.tgattr::smallint[]) AS c(attnum)
				JOIN pg_attribute a ON a.attrelid=t.tgrelid AND a.attnum=c.attnum), p.prosrc
			FROM pg_trigger t JOIN pg_proc p ON p.oid=t.tgfoid
			WHERE t.tgrelid=$1::regclass AND t.tgname=$2`, table, trigger).Scan(&columns, &body))
		covered := map[string]bool{}
		if table == "member" {
			if len(columns) != 0 {
				t.Fatalf("member trigger must cover every UPDATE, got column list %v", columns)
			}
			// All UPDATEs fire; the function's comparisons decide which changes matter.
			comparison := regexp.MustCompile(`NEW\.([a-z_]+) IS (?:NOT )?DISTINCT FROM OLD\.([a-z_]+)`)
			for _, match := range comparison.FindAllStringSubmatch(body, -1) {
				if match[1] != match[2] {
					t.Fatalf("member guard compares different columns: %s", match[0])
				}
				covered[match[1]] = true
			}
		} else {
			for _, column := range columns {
				covered[column] = true
			}
		}
		if !reflect.DeepEqual(covered, want) {
			t.Fatalf("%s trigger coverage: got %v, want access and immutable columns %v", table, covered, want)
		}
	}
}

func TestAccessEpochBeforeUpdate(t *testing.T) {
	pool := pgtest.New(t)
	id := orgtest.Organization(t, pool, "acme", "Acme", 0)
	member := orgtest.Member(t, pool, id, identitytest.Account(t, pool, "one@example.org", "One"), org.RoleMember, "one", 1)
	before := accessEpoch(t, pool, id)
	// A future BEFORE trigger can change access without naming it in the SET list.
	_, err := pool.Exec(t.Context(), `
		CREATE FUNCTION rewrite_member_role() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN NEW.role := 'owner'; RETURN NEW; END $$;
		CREATE TRIGGER rewrite_member_role BEFORE UPDATE OF handle ON member
		FOR EACH ROW EXECUTE FUNCTION rewrite_member_role();`)
	requireNoError(t, err)
	requireNoError(t, postgres.NewMemberStore(pool).UpdateHandle(t.Context(), id, member, "changed"))
	if got := accessEpoch(t, pool, id); got <= before {
		t.Fatalf("BEFORE trigger changed role without bumping epoch: %d -> %d", before, got)
	}
}

// Wait for an observed lock dependency, never a timing guess, before advancing A.
func waitForEpochLock(t *testing.T, ctx context.Context, tx pgx.Tx, pid uint32) {
	t.Helper()
	for {
		var blocked bool
		requireNoError(t, tx.QueryRow(ctx, "SELECT pg_backend_pid() = ANY(pg_blocking_pids($1))", pid).Scan(&blocked))
		if blocked {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatal("writer did not wait on posting")
		case <-time.After(time.Millisecond):
		}
	}
}

func TestAccessEpochLockOrder(t *testing.T) {
	for _, protocol := range []bool{true, false} {
		t.Run(map[bool]string{true: "organization-first", false: "direct-sql"}[protocol], func(t *testing.T) {
			pool := pgtest.New(t)
			ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
			defer cancel()
			id := orgtest.Organization(t, pool, "acme", "Acme", 0)
			member := orgtest.Member(t, pool, id, identitytest.Account(t, pool, "one@example.org", "One"), org.RoleMember, "one", 1)
			before := accessEpoch(t, pool, id)
			a, err := pool.Begin(ctx)
			requireNoError(t, err)
			defer func() { _ = a.Rollback(ctx) }()
			b, err := pool.Begin(ctx)
			requireNoError(t, err)
			defer func() { _ = b.Rollback(ctx) }()
			// The same first write and member FK lock as posting, without conversation fixtures.
			_, err = a.Exec(ctx, "UPDATE organization SET event_seq=1 WHERE id=$1", id)
			requireNoError(t, err)
			_, err = a.Exec(ctx, "INSERT INTO event_log (organization_id,seq,kind,data) VALUES ($1,1,'member.joined','{}')", id)
			requireNoError(t, err)
			done := make(chan error, 1)
			go func() {
				var err error
				if protocol {
					_, err = b.Exec(ctx, "SELECT id FROM organization WHERE id=$1 FOR NO KEY UPDATE", id)
				}
				if err == nil {
					_, err = b.Exec(ctx, "DELETE FROM member WHERE organization_id=$1 AND id=$2", id, member)
				}
				if err == nil {
					err = b.Commit(ctx)
				} else {
					_ = b.Rollback(ctx)
				}
				done <- err
			}()
			waitForEpochLock(t, ctx, a, b.Conn().PgConn().PID())
			if accessEpoch(t, a, id) != before {
				t.Fatal("uncommitted deletion leaked its epoch")
			}
			_, aerr := a.Exec(ctx, "SELECT id FROM member WHERE organization_id=$1 AND id=$2 FOR KEY SHARE", id, member)
			if aerr == nil {
				aerr = a.Commit(ctx)
			} else {
				_ = a.Rollback(ctx)
			}
			berr := <-done
			if protocol {
				requireNoError(t, aerr)
				requireNoError(t, berr)
			} else {
				var pgErr *pgconn.PgError
				deadlock := aerr
				if deadlock == nil {
					deadlock = berr
				}
				if !errors.As(deadlock, &pgErr) || pgErr.Code != "40P01" || aerr != nil && berr != nil {
					t.Fatalf("want one deadlock victim: A=%v B=%v", aerr, berr)
				}
			}
			epoch := accessEpoch(t, pool, id)
			var seq, members int64
			requireNoError(t, pool.QueryRow(ctx, "SELECT event_seq,(SELECT count(*) FROM member WHERE organization_id=$1) FROM organization WHERE id=$1", id).Scan(&seq, &members))
			if (seq == 1) != (aerr == nil) || (members == 0) != (berr == nil) || berr == nil && epoch <= before || berr != nil && epoch != before {
				t.Fatalf("committed state: epoch=%d seq=%d members=%d; A=%v B=%v", epoch, seq, members, aerr, berr)
			}
			if protocol && epoch != before+1 {
				t.Fatalf("protocol took more than one bump: %d -> %d", before, epoch)
			}
		})
	}
}
