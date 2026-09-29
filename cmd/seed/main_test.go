package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"net/url"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/tkakkie/ribbitto/internal/app/auth"
	"github.com/tkakkie/ribbitto/internal/app/setup"
	"github.com/tkakkie/ribbitto/internal/domain"
	"github.com/tkakkie/ribbitto/internal/infra/postgres/pgtest"
)

type snapshot struct {
	Members []struct {
		Name, Handle, Email, Role string
		Seq                       int64
	}
	Channels []struct {
		Name    string
		Default bool
	}
	Messages []struct {
		Channel, Author, Body string
		Seq                   int64
	}
	Seq int64
}

func readSnapshot(t *testing.T, pool *pgxpool.Pool) snapshot {
	t.Helper()
	var raw []byte
	// Exclude generated IDs, times and password hashes; retain semantic identities and order.
	err := pool.QueryRow(t.Context(), `SELECT jsonb_build_object(
		'members', (SELECT jsonb_agg(to_jsonb(r) ORDER BY r.seq) FROM (
			SELECT a.display_name AS name, m.handle, a.email, m.role, m.joined_event_seq AS seq
			FROM member m JOIN account a ON a.id = m.account_id WHERE m.organization_id = o.id) r),
		'channels', (SELECT jsonb_agg(to_jsonb(r) ORDER BY r.name) FROM (
			SELECT name, is_default AS default FROM channel WHERE organization_id = o.id) r),
		'messages', (SELECT jsonb_agg(to_jsonb(r) ORDER BY r.seq) FROM (
			SELECT c.name AS channel, m.handle AS author, p.body, p.event_seq AS seq
			FROM message p JOIN member m ON (m.organization_id, m.id) = (p.organization_id, p.member_id)
			JOIN channel c ON (c.organization_id, c.id) = (p.organization_id, p.channel_id)
			WHERE p.organization_id = o.id) r), 'seq', o.event_seq)
		FROM organization o JOIN setup s ON s.organization_id = o.id`).Scan(&raw)
	if err != nil {
		t.Fatal(err)
	}
	var result snapshot
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatal(err)
	}
	return result
}

func TestSeed(t *testing.T) {
	const count = 23 // Exercises full exchanges, repetition, and partial final exchanges.
	var previous snapshot
	var previousPassword string
	hasher, err := auth.NewHasher()
	if err != nil {
		t.Fatal(err)
	}
	for runNumber := range 2 {
		pool := pgtest.New(t)
		// pgtest changes Database in the config; ConnString still names the admin database.
		address, err := url.Parse(pool.Config().ConnString())
		if err != nil {
			t.Fatal(err)
		}
		address.Path = "/" + pool.Config().ConnConfig.Database
		databaseURL := address.String()
		var out bytes.Buffer
		if err := run(t.Context(), databaseURL, []string{"-messages", "23"}, &out); err != nil {
			t.Fatal(err)
		}
		// The printed password signs the owner in, and each run has its own.
		lines := strings.Split(strings.TrimSpace(out.String()), "\n")
		password := lines[len(lines)-1]
		if !strings.Contains(out.String(), "mira@example.test") || password == previousPassword {
			t.Fatalf("output %q: want the owner's email and a new password", out.String())
		}
		previousPassword = password
		var hash string
		if err := pool.QueryRow(t.Context(), "SELECT password_hash FROM account WHERE email = 'mira@example.test'").Scan(&hash); err != nil {
			t.Fatal(err)
		}
		if ok, err := hasher.Verify(t.Context(), password, hash); err != nil || !ok {
			t.Fatalf("printed password does not sign the owner in: %v", err)
		}
		got := readSnapshot(t, pool)
		if len(got.Members) != 5 || len(got.Channels) != 4 || len(got.Messages) != 4*count || got.Seq != 5+4*count {
			t.Fatalf("counts: members=%d channels=%d messages=%d seq=%d", len(got.Members), len(got.Channels), len(got.Messages), got.Seq)
		}
		counts := map[string]int{}
		long, tabs, combining := false, false, false
		for i, post := range got.Messages {
			body, err := domain.ValidateMessageBody(post.Body)
			if err != nil || body != post.Body || post.Seq != int64(6+i) {
				t.Fatalf("invalid message or sequence at %d: %v", i, err)
			}
			counts[post.Channel]++
			long = long || utf8.RuneCountInString(body) >= 3900
			tabs = tabs || strings.Contains(body, "\n\t")
			combining = combining || strings.Contains(body, "cafe\u0301")
		}
		for _, ch := range got.Channels {
			if counts[ch.Name] != count || ch.Default != (ch.Name == "general") {
				t.Fatalf("channel %s: default=%v count=%d", ch.Name, ch.Default, counts[ch.Name])
			}
		}
		if !long || !tabs || !combining {
			t.Fatal("missing long body, line breaks and tabs, or combining characters")
		}
		if runNumber != 0 && !reflect.DeepEqual(got, previous) {
			t.Fatal("same flags produced different members, channels, bodies, authors or order")
		}
		previous = got
		if err := run(t.Context(), databaseURL, []string{"-messages", "23"}, io.Discard); !errors.Is(err, setup.ErrCompleted) {
			t.Fatalf("second run: want setup.ErrCompleted, got %v", err)
		}
		if !reflect.DeepEqual(got, readSnapshot(t, pool)) {
			t.Fatal("refused run changed the database")
		}
	}
}

func TestArguments(t *testing.T) {
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"-messages", "0"}, "N must be positive"},
		{[]string{"-messages", "-1"}, "N must be positive"},
		{[]string{"-messages", "invalid"}, "invalid value"},
		{[]string{"unexpected"}, "usage:"},
	} {
		if err := run(t.Context(), "", tc.args, io.Discard); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Fatalf("arguments %v: %v", tc.args, err)
		}
	}
	if err := run(t.Context(), "", []string{"-h"}, io.Discard); !errors.Is(err, flag.ErrHelp) {
		t.Fatalf("help: %v", err)
	}
}

// Only loopback hosts pass, fallbacks included; everything else fails
// before any connection is attempted.
func TestRequireLocal(t *testing.T) {
	for _, tc := range []struct {
		url   string
		local bool
	}{
		{"postgres://u:p@localhost:5432/dev", true},
		{"postgres://u:p@127.0.0.1:55433/dev?sslmode=disable", true},
		{"postgres://u:p@[::1]:5432/dev", true},
		{"postgres://u:p@localhost,127.0.0.1/dev", true},
		{"postgres://u:p@db.example.test/prod", false},
		{"postgres://u:p@10.0.0.5/shared", false},
		{"postgres://u:p@localhost,db.example.test/dev", false},
		{"postgres://u:p@127.0.0.2/dev", false},
		{"postgres:///dev?host=/tmp", false},
		{"", false},
	} {
		if err := requireLocal(tc.url); (err == nil) != tc.local {
			t.Errorf("%q: %v, want local=%t", tc.url, err, tc.local)
		}
	}
	// A remote URL is refused by run itself, before connecting (192.0.2.1 is
	// a documentation address that would otherwise time out).
	if err := run(t.Context(), "postgres://u:p@192.0.2.1:5432/prod?connect_timeout=1", nil, io.Discard); err == nil || !strings.Contains(err.Error(), "local development database") {
		t.Fatalf("remote URL: %v", err)
	}
}

func TestNewSecret(t *testing.T) {
	first, err := newSecret()
	if err != nil {
		t.Fatal(err)
	}
	second, err := newSecret()
	if err != nil {
		t.Fatal(err)
	}
	if first == second || len(first) != 32 {
		t.Fatalf("secrets %q and %q", first, second)
	}
	if _, err := domain.ValidatePassword(first); err != nil {
		t.Fatal(err)
	}
}
