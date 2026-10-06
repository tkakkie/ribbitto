package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/tkakkie/ribbitto/internal/identity"
	"github.com/tkakkie/ribbitto/internal/identity/identitypg"
	"github.com/tkakkie/ribbitto/internal/org"
	"github.com/tkakkie/ribbitto/internal/platform/postgres/pgtest"
)

func TestLoadTestSizing(t *testing.T) {
	for _, tc := range []struct{ streams, perAccount, want int }{
		{1, 16, 5}, {80, 16, 5}, {81, 16, 6}, {300, 16, 19}, {math.MaxInt, math.MaxInt, 5},
	} {
		got, err := validateRun(1, 4, 5, tc.streams, tc.perAccount, 2, true, "credentials.json")
		if err != nil || got != tc.want {
			t.Fatalf("sizing %+v: accounts=%d error=%v", tc, got, err)
		}
	}
	for _, sessions := range []int{19992, 19993} {
		_, err := validateRun(10, 4, 5, 1, 16, sessions, true, "credentials.json")
		if (err == nil) != (sessions == 19992) {
			t.Fatalf("combined budget boundary: %v", err)
		}
	}
}

func TestCredentialRepositoryRefusal(t *testing.T) {
	for _, path := range []string{"credentials.json", "../../bin/credentials.json"} {
		if f, err := createCredentials(path); err == nil {
			_ = f.Close()
			t.Fatal("accepted a repository path")
		}
	}
	alias := filepath.Join(t.TempDir(), "checkout")
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(cwd, alias); err != nil {
		t.Fatal(err)
	}
	if f, err := createCredentials(filepath.Join(alias, "credentials.json")); err == nil {
		_ = f.Close()
		t.Fatal("accepted a symlink into the repository")
	}
}

func TestLoadTestSeed(t *testing.T) {
	pool := pgtest.New(t)
	address, err := url.Parse(pool.Config().ConnString())
	if err != nil {
		t.Fatal(err)
	}
	address.Path = "/" + pool.Config().ConnConfig.Database
	path := filepath.Join(t.TempDir(), "credentials.json")
	args := []string{"-messages", "1", "-streams", "300", "-sessions-per-account", "2", "-output", path}
	for _, host := range []string{"192.0.2.1", "localhost,192.0.2.1"} {
		err := run(t.Context(), "postgres://u:p@"+host+"/dev", append(append([]string{}, args...), "-messages", "0"), io.Discard)
		if err == nil || !strings.Contains(err.Error(), "local development database") {
			t.Fatalf("host guard must precede bounds: %v", err)
		}
	}
	// Every refusal must leave the database untouched, even with an output path.
	for _, extra := range [][]string{
		{"-messages", "0"}, {"-messages", "-1"}, {"-messages", "25001"},
		{"-messages", strconv.Itoa(math.MaxInt)}, {"-streams", strconv.Itoa(math.MaxInt), "-streams-per-account", "1"},
		{"-sessions-per-account", strconv.Itoa(math.MaxInt)}, {"-messages", "24999"},
		{"-streams", "0"}, {"-streams-per-account", "0"}, {"-sessions-per-account", "-1"}, {"-output", ""},
	} {
		if err := run(t.Context(), address.String(), append(append([]string{}, args...), extra...), io.Discard); err == nil {
			t.Fatalf("accepted invalid arguments %v", extra)
		}
		var accounts int
		if err := pool.QueryRow(t.Context(), "SELECT count(*) FROM account").Scan(&accounts); err != nil || accounts != 0 {
			t.Fatalf("refusal wrote accounts: count=%d error=%v", accounts, err)
		}
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("refusal created credential file: %v", err)
		}
	}
	if err := os.WriteFile(path, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := run(t.Context(), address.String(), args, io.Discard); !errors.Is(err, os.ErrExist) {
		t.Fatalf("existing file: %v", err)
	}
	if raw, err := os.ReadFile(path); err != nil || string(raw) != "keep" {
		t.Fatal("existing file changed")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := run(t.Context(), address.String(), args, &out); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("credential permissions: %v, %v", info, err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var file credentialFile
	if err := json.Unmarshal(raw, &file); err != nil {
		t.Fatal(err)
	}
	got := readSnapshot(t, pool)
	if len(got.Topics) != 4 {
		t.Fatalf("load-test topics: %v", got.Topics)
	}
	for _, topic := range got.Topics {
		if topic.Name != "" {
			t.Fatalf("load-test mode added a named topic: %v", topic)
		}
	}
	if file.OrganizationSlug != "paper-lantern" || len(file.Accounts) != 19 || len(got.Members) != 19 || len(file.ChannelIDs) != 4 || len(got.Messages) != 4 {
		t.Fatal("unexpected manifest or database counts")
	}
	var accounts, sessionRows int
	if err := pool.QueryRow(t.Context(), "SELECT (SELECT count(*) FROM account), (SELECT count(*) FROM session)").Scan(&accounts, &sessionRows); err != nil || accounts != 19 || sessionRows != 38 {
		t.Fatalf("account/session counts: %d/%d: %v", accounts, sessionRows, err)
	}
	for _, id := range file.ChannelIDs {
		var exists bool
		if err := pool.QueryRow(t.Context(), "SELECT EXISTS(SELECT FROM channel c JOIN organization o ON o.id = c.organization_id WHERE c.id = $1 AND o.slug = $2)", id, file.OrganizationSlug).Scan(&exists); err != nil || !exists {
			t.Fatalf("invalid channel: %v", err)
		}
	}
	seen := map[string]bool{}
	sessions := identitypg.NewSessions(pool, time.Now, nil)
	expired := identitypg.NewSessions(pool, func() time.Time { return time.Now().Add(identity.SessionLifetime) }, nil)
	for _, entry := range file.Accounts {
		if len(entry.Tokens) != 2 || seen[entry.Handle] {
			t.Fatal("incorrect account grouping")
		}
		seen[entry.Handle] = true
		for _, token := range entry.Tokens {
			account, _, err := sessions.Resolve(t.Context(), token)
			if err != nil || account.Email != entry.Handle+"@example.test" || seen[token] || strings.Contains(out.String(), token) {
				t.Fatal("token authentication, uniqueness, grouping or secrecy failed")
			}
			seen[token] = true
			if _, _, err := expired.Resolve(t.Context(), token); !errors.Is(err, identity.ErrNoSession) {
				t.Fatal("token exceeded normal lifetime")
			}
		}
	}
	// Setup refusal precedes bounds and file creation, including on load-test reruns.
	if err := run(t.Context(), address.String(), append(args, "-messages", "0"), io.Discard); !errors.Is(err, org.ErrSetupCompleted) {
		t.Fatalf("completed setup: %v", err)
	}
	if !reflect.DeepEqual(got, readSnapshot(t, pool)) {
		t.Fatal("completed-setup refusal changed the database")
	}
}
