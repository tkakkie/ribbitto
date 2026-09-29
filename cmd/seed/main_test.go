package main

import (
	"encoding/json"
	"errors"
	"flag"
	"net/url"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/jackc/pgx/v5/pgxpool"
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
	for runNumber := range 2 {
		pool := pgtest.New(t)
		// pgtest changes Database in the config; ConnString still names the admin database.
		address, err := url.Parse(pool.Config().ConnString())
		if err != nil {
			t.Fatal(err)
		}
		address.Path = "/" + pool.Config().ConnConfig.Database
		databaseURL := address.String()
		if err := run(t.Context(), databaseURL, []string{"-messages", "23"}); err != nil {
			t.Fatal(err)
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
		if err := run(t.Context(), databaseURL, []string{"-messages", "23"}); !errors.Is(err, setup.ErrCompleted) {
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
		if err := run(t.Context(), "", tc.args); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Fatalf("arguments %v: %v", tc.args, err)
		}
	}
	if err := run(t.Context(), "", []string{"-h"}); !errors.Is(err, flag.ErrHelp) {
		t.Fatalf("help: %v", err)
	}
}
