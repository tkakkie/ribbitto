package main

import (
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/tkakkie/ribbitto/internal/platform/postgres/pgtest"
)

// fakeServer records statements and keeps the set of databases, so the
// registry rules run without PostgreSQL.
type fakeServer struct {
	databases  map[string]bool
	statements []string
}

func newFakeServer() *fakeServer { return &fakeServer{databases: map[string]bool{}} }

func (f *fakeServer) query(statement string) (string, error) {
	f.statements = append(f.statements, statement)
	name := between(statement, `"`, `"`)
	switch {
	case strings.HasPrefix(statement, "SELECT count(*)"):
		if f.databases[between(statement, "'", "'")] {
			return "1", nil
		}
		return "0", nil
	case strings.HasPrefix(statement, "CREATE DATABASE"):
		f.databases[name] = true
	case strings.HasPrefix(statement, "DROP DATABASE"):
		delete(f.databases, name)
	}
	return "", nil
}

func (f *fakeServer) migrate(string) error { return nil }

func (f *fakeServer) dropped(name string) bool {
	return slices.Contains(f.statements, `DROP DATABASE IF EXISTS "`+name+`" WITH (FORCE)`)
}

func between(s, open, close string) string {
	_, rest, ok := strings.Cut(s, open)
	if !ok {
		return ""
	}
	value, _, _ := strings.Cut(rest, close)
	return value
}

// worktreeFixture is a main checkout with one linked worktree and no
// reservation yet.
func worktreeFixture(t *testing.T) (mainDir, linked, common string) {
	t.Helper()
	base := resolve(t.TempDir())
	mainDir, linked = filepath.Join(base, "main"), filepath.Join(base, "linked")
	if err := os.Mkdir(mainDir, 0o755); err != nil {
		t.Fatal(err)
	}
	gitIn(t, mainDir, "init", "-q")
	gitIn(t, mainDir, "-c", "user.name=t", "-c", "user.email=t@example.invalid", "-c", "commit.gpgsign=false", "commit", "-q", "--allow-empty", "-m", "base")
	gitIn(t, mainDir, "worktree", "add", "-q", "--detach", linked)
	return mainDir, linked, filepath.Join(mainDir, ".git")
}

func registryOf(t *testing.T, common string) map[string]stored {
	t.Helper()
	entries, err := readRegistry(filepath.Join(common, "ai-env.json"))
	if err != nil {
		t.Fatal(err)
	}
	return entries
}

func mustReserve(t *testing.T, dir string, db *fakeServer) {
	t.Helper()
	if err := reserve(dir, false, db); err != nil {
		t.Fatalf("reserve %s: %v", dir, err)
	}
}

func listen(t *testing.T, port int) net.Listener {
	t.Helper()
	l, err := net.Listen("tcp4", "127.0.0.1:"+strconv.Itoa(port))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = l.Close() })
	return l
}

func TestReserveWritesTodaysFormat(t *testing.T) {
	t.Setenv("RIBBITTO_TEST_DATABASE_URL", admin)
	mainDir, linked, common := worktreeFixture(t)
	db := newFakeServer()
	mustReserve(t, mainDir, db)
	mustReserve(t, linked, db)
	entries := registryOf(t, common)
	m, l := entries["main"], entries["worktree:linked"]
	if m.MetadataToken != nil || m.DatabasePath != mainDir || len(m.Ports) != 2 {
		t.Fatalf("main entry: %+v", m)
	}
	token, found, err := metadataToken(common, "worktree:linked")
	if l.MetadataToken == nil || err != nil || !found || *l.MetadataToken != token || len(token) != 64 {
		t.Fatalf("linked token: entry %v, file %q", l.MetadataToken, token)
	}
	local, err := os.ReadFile(filepath.Join(linked, ".env.local"))
	if err != nil || string(local) != "AI_DATABASE="+databaseName(linked)+"\nAI_APP_PORT="+strconv.Itoa(l.Ports[0])+"\nAI_METRICS_PORT="+strconv.Itoa(l.Ports[1])+"\n" {
		t.Fatalf(".env.local = %q, %v", local, err)
	}
	if !db.databases[databaseName(mainDir)] || !db.databases[databaseName(linked)] {
		t.Fatal("databases not created")
	}
	// The Go command's own run accepts what it reserved.
	if _, err := environment(linked); err != nil {
		t.Fatalf("environment after reserve: %v", err)
	}
	// Four distinct ports, all inside the automatic range.
	seen := map[int]bool{}
	for _, p := range append(m.Ports, l.Ports...) {
		if seen[p] || p < firstPort || p >= firstPort+portRange {
			t.Fatalf("ports %v %v", m.Ports, l.Ports)
		}
		seen[p] = true
	}
}

func TestReservePorts(t *testing.T) {
	t.Run("automatic skips a listener", func(t *testing.T) {
		_, linked, common := worktreeFixture(t)
		busy := initialPort(linked)
		listen(t, busy)
		mustReserve(t, linked, newFakeServer())
		if slices.Contains(registryOf(t, common)["worktree:linked"].Ports, busy) {
			t.Fatalf("reserved the listening port %d", busy)
		}
	})
	t.Run("rerun keeps ports", func(t *testing.T) {
		_, linked, common := worktreeFixture(t)
		db := newFakeServer()
		mustReserve(t, linked, db)
		before := registryOf(t, common)["worktree:linked"].Ports
		mustReserve(t, linked, db)
		if after := registryOf(t, common)["worktree:linked"].Ports; !slices.Equal(after, before) {
			t.Fatalf("ports %v, then %v", before, after)
		}
	})
	t.Run("rerun replaces a foreign listener", func(t *testing.T) {
		_, linked, common := worktreeFixture(t)
		db := newFakeServer()
		mustReserve(t, linked, db)
		for i := range 2 {
			before := registryOf(t, common)["worktree:linked"].Ports
			l := listen(t, before[i])
			mustReserve(t, linked, db)
			after := registryOf(t, common)["worktree:linked"].Ports
			if slices.Contains(after, before[i]) {
				t.Fatalf("kept the listening port %d", before[i])
			}
			_ = l.Close()
			for _, p := range after {
				if !free(p) {
					t.Fatalf("reserved a busy port %d", p)
				}
			}
		}
	})
	for _, value := range []string{"0", "70000", "not-a-port"} {
		t.Run("override "+value, func(t *testing.T) {
			_, linked, _ := worktreeFixture(t)
			t.Setenv("AI_APP_PORT", value)
			if err := reserve(linked, false, newFakeServer()); err == nil || err.Error() != "port collision or invalid override: AI_APP_PORT" {
				t.Fatalf("reserve = %v", err)
			}
		})
	}
	t.Run("override reserved by another worktree or listening", func(t *testing.T) {
		mainDir, linked, common := worktreeFixture(t)
		db := newFakeServer()
		mustReserve(t, mainDir, db)
		taken := registryOf(t, common)["main"].Ports[0]
		t.Setenv("AI_APP_PORT", strconv.Itoa(taken))
		if err := reserve(linked, false, db); err == nil || !strings.Contains(err.Error(), "collision") {
			t.Fatalf("override of a reserved port: %v", err)
		}
		l := listen(t, 0)
		t.Setenv("AI_APP_PORT", strconv.Itoa(l.Addr().(*net.TCPAddr).Port))
		if err := reserve(linked, false, db); err == nil || !strings.Contains(err.Error(), "collision") {
			t.Fatalf("override of a listening port: %v", err)
		}
	})
}

// Reservations wait for the exclusive lock, and two that ran together still
// get four distinct ports.
func TestReserveWaitsForTheLock(t *testing.T) {
	mainDir, linked, common := worktreeFixture(t)
	db := newFakeServer()
	lock, err := os.OpenFile(filepath.Join(common, "ai-env.lock"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = lock.Close() }()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 2)
	for _, dir := range []string{mainDir, linked} {
		go func() { done <- reserve(dir, false, &lockedFake{db: db}) }()
	}
	select {
	case err := <-done:
		t.Fatalf("a reservation finished while the lock was held: %v", err)
	case <-time.After(time.Second):
	}
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_UN); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
	entries := registryOf(t, common)
	ports := append(slices.Clone(entries["main"].Ports), entries["worktree:linked"].Ports...)
	slices.Sort(ports)
	if len(slices.Compact(ports)) != 4 {
		t.Fatalf("ports %v", ports)
	}
}

// lockedFake lets two goroutines share one fakeServer; the registry lock
// already serialises them, so this only keeps the race detector honest.
type lockedFake struct{ db *fakeServer }

var fakeMu = make(chan struct{}, 1)

func (l *lockedFake) query(s string) (string, error) {
	fakeMu <- struct{}{}
	defer func() { <-fakeMu }()
	return l.db.query(s)
}

func (l *lockedFake) migrate(name string) error { return l.db.migrate(name) }

func TestReserveCleansStaleReservations(t *testing.T) {
	t.Run("an ID Git no longer lists", func(t *testing.T) {
		mainDir, _, common := worktreeFixture(t)
		deleted := filepath.Join(filepath.Dir(mainDir), "deleted")
		if err := writeRegistry(filepath.Join(common, "ai-env.json"), map[string]stored{"worktree:deleted": {Path: deleted, DatabasePath: deleted, Ports: []int{20001, 20002}}}); err != nil {
			t.Fatal(err)
		}
		db := newFakeServer()
		mustReserve(t, mainDir, db)
		if _, ok := registryOf(t, common)["worktree:deleted"]; ok || !db.dropped(databaseName(deleted)) {
			t.Fatalf("stale entry kept or its database not dropped: %v", db.statements)
		}
	})
	t.Run("an ID Git does not list, though its token matches", func(t *testing.T) {
		mainDir, _, common := worktreeFixture(t)
		// A metadata directory named like the entry holds a matching token,
		// but Git lists that worktree as "worktree:legacy", never "legacy".
		legacy := filepath.Join(filepath.Dir(mainDir), "legacy")
		meta := filepath.Join(common, "worktrees", "legacy")
		gitIn(t, mainDir, "worktree", "add", "-q", "--detach", legacy)
		if err := os.WriteFile(filepath.Join(meta, "ribbitto-ai-env"), []byte("token\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		token := "token"
		if err := writeRegistry(filepath.Join(common, "ai-env.json"), map[string]stored{"legacy": {Path: legacy, DatabasePath: legacy, Ports: []int{20001, 20002}, MetadataToken: &token}}); err != nil {
			t.Fatal(err)
		}
		db := newFakeServer()
		mustReserve(t, mainDir, db)
		if _, ok := registryOf(t, common)["legacy"]; ok || !db.dropped(databaseName(legacy)) {
			t.Fatalf("unlisted entry kept: %v", db.statements)
		}
	})
	t.Run("a removed worktree", func(t *testing.T) {
		mainDir, linked, common := worktreeFixture(t)
		db := newFakeServer()
		mustReserve(t, linked, db)
		gitIn(t, mainDir, "worktree", "remove", "--force", linked)
		mustReserve(t, mainDir, db)
		if _, ok := registryOf(t, common)["worktree:linked"]; ok || !db.dropped(databaseName(linked)) {
			t.Fatal("removed worktree's reservation kept")
		}
	})
	t.Run("a prunable worktree is kept until its ID is gone", func(t *testing.T) {
		mainDir, linked, common := worktreeFixture(t)
		db := newFakeServer()
		mustReserve(t, linked, db)
		if err := os.RemoveAll(linked); err != nil {
			t.Fatal(err)
		}
		mustReserve(t, mainDir, db)
		if _, ok := registryOf(t, common)["worktree:linked"]; !ok || db.dropped(databaseName(linked)) {
			t.Fatal("prunable worktree's reservation dropped")
		}
		gitIn(t, mainDir, "worktree", "prune")
		mustReserve(t, mainDir, db)
		if _, ok := registryOf(t, common)["worktree:linked"]; ok || !db.dropped(databaseName(linked)) {
			t.Fatal("pruned worktree's reservation kept")
		}
	})
}

// A worktree keeps its database through a move, a relative backlink, a
// symlinked spelling and a subdirectory, because the entry follows its Git
// ID and keeps the original database path.
func TestReserveKeepsTheDatabase(t *testing.T) {
	for _, tc := range []struct {
		name  string
		after func(t *testing.T, mainDir, linked string) string
	}{
		{"moved", func(t *testing.T, mainDir, linked string) string {
			moved := filepath.Join(filepath.Dir(linked), "moved")
			gitIn(t, mainDir, "worktree", "move", linked, moved)
			return moved
		}},
		{"relative backlink", func(t *testing.T, mainDir, linked string) string {
			gitIn(t, mainDir, "config", "worktree.useRelativePaths", "true")
			gitIn(t, mainDir, "worktree", "repair", linked)
			return linked
		}},
		{"symlinked spelling", func(t *testing.T, _, linked string) string {
			alias := filepath.Join(t.TempDir(), "alias")
			if err := os.Symlink(filepath.Dir(linked), alias); err != nil {
				t.Fatal(err)
			}
			return filepath.Join(alias, filepath.Base(linked))
		}},
		{"subdirectory", func(t *testing.T, _, linked string) string {
			child := filepath.Join(linked, "child")
			if err := os.Mkdir(child, 0o755); err != nil {
				t.Fatal(err)
			}
			return child
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mainDir, linked, common := worktreeFixture(t)
			db := newFakeServer()
			mustReserve(t, linked, db)
			before := registryOf(t, common)["worktree:linked"]
			dir := tc.after(t, mainDir, linked)
			mustReserve(t, mainDir, db)
			mustReserve(t, dir, db)
			after := registryOf(t, common)["worktree:linked"]
			root, _ := worktreeRoot(dir)
			if after.DatabasePath != linked || after.Path != root || !slices.Equal(after.Ports, before.Ports) || *after.MetadataToken != *before.MetadataToken || db.dropped(databaseName(linked)) {
				t.Fatalf("entry %+v (before %+v), drops %v", after, before, db.statements)
			}
		})
	}
}

func TestReserveCopiedRepositoryAfterRepair(t *testing.T) {
	t.Setenv("RIBBITTO_TEST_DATABASE_URL", admin)
	mainDir, linked, common := worktreeFixture(t)
	base := filepath.Dir(mainDir)
	second := filepath.Join(base, "second")
	gitIn(t, mainDir, "worktree", "add", "-q", "--detach", second)
	db := newFakeServer()
	for _, root := range []string{mainDir, linked, second} {
		mustReserve(t, root, db)
	}
	before := registryOf(t, common)
	relocated := filepath.Join(base, "relocated")
	if err := os.Mkdir(relocated, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, root := range []string{mainDir, linked, second} {
		if out, err := exec.Command("cp", "-R", root, filepath.Join(relocated, filepath.Base(root))).CombinedOutput(); err != nil {
			t.Fatalf("copy: %v\n%s", err, out)
		}
		if err := os.Rename(root, filepath.Join(base, "original-"+filepath.Base(root))); err != nil {
			t.Fatal(err)
		}
	}
	newMain := filepath.Join(relocated, "main")
	gitIn(t, newMain, "worktree", "repair", filepath.Join(relocated, "linked"), filepath.Join(relocated, "second"))
	// run accepts the copied tokens before ai-env retargets the paths.
	for _, name := range []string{"main", "linked", "second"} {
		if _, err := environment(filepath.Join(relocated, name)); err != nil {
			t.Fatalf("environment in copied %s: %v", name, err)
		}
	}
	mustReserve(t, newMain, db)
	after := registryOf(t, filepath.Join(newMain, ".git"))
	for id, e := range before {
		got := after[id]
		if got.DatabasePath != e.DatabasePath || !slices.Equal(got.Ports, e.Ports) || (e.MetadataToken == nil) != (got.MetadataToken == nil) || got.Path != filepath.Join(relocated, filepath.Base(e.Path)) {
			t.Fatalf("%s: %+v, before %+v", id, got, e)
		}
		if db.dropped(databaseName(e.DatabasePath)) {
			t.Fatalf("%s's database was dropped", id)
		}
	}
}

// A metadata name Git reuses for a new worktree must not inherit the old
// database: run refuses it, and reserve gives it a fresh one.
func TestReserveReusedWorktreeID(t *testing.T) {
	t.Setenv("RIBBITTO_TEST_DATABASE_URL", admin)
	for _, sameSpot := range []bool{true, false} {
		t.Run("same path "+strconv.FormatBool(sameSpot), func(t *testing.T) {
			mainDir, linked, common := worktreeFixture(t)
			db := newFakeServer()
			mustReserve(t, linked, db)
			old := registryOf(t, common)["worktree:linked"]
			local, err := os.ReadFile(filepath.Join(linked, ".env.local"))
			if err != nil {
				t.Fatal(err)
			}
			replacement := linked
			if !sameSpot {
				replacement = filepath.Join(filepath.Dir(linked), "other", "linked")
			}
			gitIn(t, mainDir, "worktree", "remove", "--force", linked)
			gitIn(t, mainDir, "worktree", "add", "-q", "--detach", replacement)
			if err := os.WriteFile(filepath.Join(replacement, ".env.local"), local, 0o600); err != nil {
				t.Fatal(err)
			}
			tokenFile := filepath.Join(common, "worktrees", "linked", "ribbitto-ai-env")
			for _, token := range []string{"", *old.MetadataToken + "different"} {
				if token != "" {
					if err := os.WriteFile(tokenFile, []byte(token+"\n"), 0o600); err != nil {
						t.Fatal(err)
					}
				}
				if _, err := environment(replacement); err == nil || !strings.HasPrefix(err.Error(), "worktree reservation is stale") {
					t.Fatalf("environment with token %q: %v", token, err)
				}
			}
			mustReserve(t, replacement, db)
			fresh := registryOf(t, common)["worktree:linked"]
			stored, _, _ := metadataToken(common, "worktree:linked")
			if *fresh.MetadataToken == *old.MetadataToken || stored != *fresh.MetadataToken || fresh.DatabasePath != resolve(replacement) {
				t.Fatalf("fresh entry %+v, token file %q", fresh, stored)
			}
			if !db.dropped(databaseName(linked)) || !db.databases[databaseName(resolve(replacement))] {
				t.Fatalf("old database kept or new one missing: %v", db.statements)
			}
		})
	}
}

func TestRelease(t *testing.T) {
	mainDir, linked, common := worktreeFixture(t)
	db := newFakeServer()
	mustReserve(t, mainDir, db)
	mustReserve(t, linked, db)
	child := filepath.Join(linked, "child")
	if err := os.Mkdir(child, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := reserve(child, true, db); err != nil {
		t.Fatal(err)
	}
	entries := registryOf(t, common)
	if _, ok := entries["worktree:linked"]; ok || !db.dropped(databaseName(linked)) {
		t.Fatal("release kept the reservation or its database")
	}
	if _, ok := entries["main"]; !ok || !db.databases[databaseName(mainDir)] {
		t.Fatal("release touched another worktree")
	}
	if _, err := os.Stat(filepath.Join(linked, ".env.local")); !os.IsNotExist(err) {
		t.Fatalf(".env.local after release: %v", err)
	}
}

// The real server: a reservation creates and migrates the database, and a
// release drops only that one.
func TestReserveWithDatabase(t *testing.T) {
	url := pgtest.AdminURL(t)
	_, linked, _ := worktreeFixture(t)
	db := &server{}
	defer db.close()
	name := databaseName(linked)
	t.Cleanup(func() { _, _ = db.query(`DROP DATABASE IF EXISTS "` + name + `" WITH (FORCE)`) })
	if err := reserve(linked, false, db); err != nil {
		t.Fatal(err)
	}
	if count, err := db.query("SELECT count(*) FROM pg_database WHERE datname='" + name + "'"); err != nil || count != "1" {
		t.Fatalf("database after reserve: %q, %v", count, err)
	}
	stdout, stderr, code := command(t, linked, append(without(os.Environ()), "RIBBITTO_TEST_DATABASE_URL="+url), "", "run", "env")
	if code != 0 || !strings.Contains(stdout, "/"+name) {
		t.Fatalf("run after reserve: exit %d, %s", code, stderr)
	}
	if err := reserve(linked, true, db); err != nil {
		t.Fatal(err)
	}
	if count, _ := db.query("SELECT count(*) FROM pg_database WHERE datname='" + name + "'"); count != "0" {
		t.Fatal("database kept after release")
	}
	// Other tests share the server, so check that the templates survive
	// rather than counting databases.
	if count, _ := db.query("SELECT count(*) FROM pg_database WHERE datname IN ('template0', 'template1')"); count != "2" {
		t.Fatal("release dropped a template database")
	}
}

// A token that exists but cannot be read stops the run before anything is
// dropped; treating it as missing would drop a live worktree's database.
func TestReserveStopsOnAnUnreadableToken(t *testing.T) {
	mainDir, linked, common := worktreeFixture(t)
	db := newFakeServer()
	mustReserve(t, linked, db)
	tokenFile := filepath.Join(common, "worktrees", "linked", "ribbitto-ai-env")
	if err := os.Chmod(tokenFile, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(tokenFile, 0o600) })
	if f, err := os.Open(tokenFile); err == nil {
		_ = f.Close()
		t.Skip("running with permission to read it")
	}
	if err := reserve(mainDir, false, db); err == nil {
		t.Fatal("reserve passed with an unreadable token")
	}
	if db.dropped(databaseName(linked)) {
		t.Fatal("the live worktree's database was dropped")
	}
	if _, ok := registryOf(t, common)["worktree:linked"]; !ok {
		t.Fatal("the live worktree's reservation was removed")
	}
}

// A reservation written by the Python ai-env (its JSON layout, token file and
// .env.local) keeps its database, token and ports through a Go reserve.
func TestReserveKeepsTodaysReservation(t *testing.T) {
	mainDir, linked, common := worktreeFixture(t)
	ports := []int{}
	for _, l := range []net.Listener{listen(t, 0), listen(t, 0)} {
		ports = append(ports, l.Addr().(*net.TCPAddr).Port)
		_ = l.Close()
	}
	token := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	registry := `{"worktree:linked": {"path": "` + linked + `", "database_path": "` + linked + `", "ports": [` + strconv.Itoa(ports[0]) + `, ` + strconv.Itoa(ports[1]) + `], "metadata_token": "` + token + `"}}` + "\n"
	if err := os.WriteFile(filepath.Join(common, "ai-env.json"), []byte(registry), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(common, "worktrees", "linked", "ribbitto-ai-env"), []byte(token+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	name := databaseName(linked)
	local := "AI_DATABASE=" + name + "\nAI_APP_PORT=" + strconv.Itoa(ports[0]) + "\nAI_METRICS_PORT=" + strconv.Itoa(ports[1]) + "\n"
	if err := os.WriteFile(filepath.Join(linked, ".env.local"), []byte(local), 0o600); err != nil {
		t.Fatal(err)
	}
	db := newFakeServer()
	db.databases[name] = true
	mustReserve(t, mainDir, db)
	mustReserve(t, linked, db)
	got := registryOf(t, common)["worktree:linked"]
	if got.DatabasePath != linked || !slices.Equal(got.Ports, ports) || got.MetadataToken == nil || *got.MetadataToken != token {
		t.Fatalf("entry %+v", got)
	}
	if stored, _, _ := metadataToken(common, "worktree:linked"); stored != token {
		t.Fatalf("token file rewritten: %q", stored)
	}
	after, err := os.ReadFile(filepath.Join(linked, ".env.local"))
	if err != nil || string(after) != local {
		t.Fatalf(".env.local = %q, %v", after, err)
	}
	if db.dropped(name) || slices.Contains(db.statements, `CREATE DATABASE "`+name+`" TEMPLATE template0`) {
		t.Fatalf("the database was recreated: %v", db.statements)
	}
}

// A registry that is JSON null or not an object is refused instead of
// crashing the run.
func TestReserveRefusesAnUnusableRegistry(t *testing.T) {
	for _, content := range []string{"null\n", "[]\n", "{\n"} {
		t.Run(strings.TrimSpace(content), func(t *testing.T) {
			_, linked, common := worktreeFixture(t)
			if err := os.WriteFile(filepath.Join(common, "ai-env.json"), []byte(content), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := reserve(linked, false, newFakeServer()); err == nil {
				t.Fatal("reserve accepted the registry")
			}
		})
	}
}
