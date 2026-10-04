package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"math"
	"net/url"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/tkakkie/ribbitto/internal/app/message"
	"github.com/tkakkie/ribbitto/internal/domain"
	"github.com/tkakkie/ribbitto/internal/identity"
	"github.com/tkakkie/ribbitto/internal/identity/identitypg"
	"github.com/tkakkie/ribbitto/internal/infra/postgres"
	"github.com/tkakkie/ribbitto/internal/infra/postgres/pgtest"
	"github.com/tkakkie/ribbitto/internal/org"
	"github.com/tkakkie/ribbitto/internal/org/orgpg"
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
		Channel, Author, Body, Topic string
		Seq                          int64
	}
	Topics []struct{ Channel, Name string }
	Seq    int64
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
			SELECT c.name AS channel, m.handle AS author, p.body, p.event_seq AS seq, t.name AS topic
			FROM message p JOIN member m ON (m.organization_id, m.id) = (p.organization_id, p.member_id)
			JOIN channel c ON (c.organization_id, c.id) = (p.organization_id, p.channel_id)
			JOIN topic t ON (t.organization_id, t.id) = (p.organization_id, p.topic_id)
			WHERE p.organization_id = o.id) r),
		'topics', (SELECT jsonb_agg(to_jsonb(r) ORDER BY r.channel, r.name) FROM (
			SELECT c.name AS channel, t.name FROM topic t
			JOIN channel c ON (c.organization_id, c.id) = (t.organization_id, t.channel_id)
			WHERE t.organization_id = o.id) r), 'seq', o.event_seq)
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
	var previous snapshot
	var previousPassword string
	hasher, err := identity.NewHasher()
	if err != nil {
		t.Fatal(err)
	}
	// Repetition and partial exchanges, determinism, and the smallest valid N.
	for runNumber, count := range []int{23, 23, 1} {
		pool := pgtest.New(t)
		// pgtest changes Database in the config; ConnString still names the admin database.
		address, err := url.Parse(pool.Config().ConnString())
		if err != nil {
			t.Fatal(err)
		}
		address.Path = "/" + pool.Config().ConnConfig.Database
		databaseURL := address.String()
		if runNumber == 0 {
			if err := run(t.Context(), databaseURL, []string{"-messages", "24985"}, io.Discard); err == nil || !strings.Contains(err.Error(), "100000") {
				t.Fatalf("accepted development fixture over budget: %v", err)
			}
			var rows int
			if err := pool.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM account) +
				(SELECT count(*) FROM organization) + (SELECT count(*) FROM setup) +
				(SELECT count(*) FROM topic) + (SELECT count(*) FROM message) +
				(SELECT count(*) FROM event_log)`).Scan(&rows); err != nil || rows != 0 {
				t.Fatalf("budget refusal wrote data: rows=%d error=%v", rows, err)
			}
		}
		var out bytes.Buffer
		if err := run(t.Context(), databaseURL, []string{"-messages", strconv.Itoa(count)}, &out); err != nil {
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
		if len(got.Members) != 5 || len(got.Channels) != 4 || len(got.Topics) != 6 || len(got.Messages) != 4*count+topicFixtureMessages || got.Seq != int64(5+4*count+topicFixtureMessages+2) {
			t.Fatalf("counts: members=%d channels=%d messages=%d seq=%d", len(got.Members), len(got.Channels), len(got.Messages), got.Seq)
		}
		counts := map[string]int{}
		long, tabs, combining := false, false, false
		for i, post := range got.Messages[:4*count] {
			body, err := domain.ValidateMessageBody(post.Body)
			if err != nil || body != post.Body || post.Seq != int64(6+i) || post.Topic != "" {
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
		if count == 23 && (!long || !tabs || !combining) {
			t.Fatal("missing long body, line breaks and tabs, or combining characters")
		}
		checkTopicFixtures(t, pool, count)
		if runNumber == 1 && !reflect.DeepEqual(got, previous) {
			t.Fatal("same flags produced different members, channels, bodies, authors or order")
		}
		previous = got
		if err := run(t.Context(), databaseURL, []string{"-messages", "23"}, io.Discard); !errors.Is(err, org.ErrSetupCompleted) {
			t.Fatalf("second run: want org.ErrSetupCompleted, got %v", err)
		}
		if !reflect.DeepEqual(got, readSnapshot(t, pool)) {
			t.Fatal("refused run changed the database")
		}
	}
}

func TestDevelopmentBudget(t *testing.T) {
	for _, tc := range []struct {
		messages int
		ok       bool
	}{{1, true}, {24984, true}, {24985, false}, {25000, false}, {math.MaxInt, false}, {0, false}, {-1, false}} {
		_, err := validateRun(tc.messages, 4, 5, 0, 16, 1, false, "")
		if (err == nil) != tc.ok {
			t.Errorf("messages=%d: %v, want accepted=%v", tc.messages, err, tc.ok)
		}
	}
}

func checkTopicFixtures(t *testing.T, pool *pgxpool.Pool, count int) {
	t.Helper()
	var accountID domain.ID
	if err := pool.QueryRow(t.Context(), "SELECT id FROM account WHERE email = 'mira@example.test'").Scan(&accountID); err != nil {
		t.Fatal(err)
	}
	member, err := orgpg.NewAuthorizer(pool).Member(t.Context(), &identity.Account{ID: accountID}, "paper-lantern")
	if err != nil {
		t.Fatal(err)
	}
	reader := postgres.MessageReader{Pool: pool, Accounts: identitypg.AccountsIn, Members: lookupMembers}
	for i, name := range []string{"rooftop-garden", "garden-time"} {
		var channelID, topicID domain.ID
		var total int
		if err := pool.QueryRow(t.Context(), `SELECT t.channel_id, t.id, count(p.id)
			FROM topic t JOIN channel c ON c.id = t.channel_id JOIN message p ON p.topic_id = t.id
			WHERE t.name = $1 AND c.name = 'general' AND NOT t.is_default
			GROUP BY t.channel_id, t.id`, name).Scan(&channelID, &topicID, &total); err != nil {
			t.Fatal(err)
		}
		want, sourceSeq := 1, int64(6+4*count)
		if i == 0 {
			want += message.PageSize + 10
		} else {
			sourceSeq += 3 + message.PageSize + 10
		}
		if total != want {
			t.Fatalf("%s: %d messages, want %d", name, total, want)
		}
		// A moved post keeps its original sequence and posting event's source;
		// its move is followed by a notice and the notice's durable posting event.
		var valid bool
		if err := pool.QueryRow(t.Context(), `SELECT EXISTS(
			SELECT FROM message p JOIN channel c ON c.id = p.channel_id
			JOIN event_log posted ON posted.organization_id = p.organization_id AND posted.seq = p.event_seq
			JOIN event_log moved ON moved.organization_id = p.organization_id AND moved.seq = p.event_seq + 1
			JOIN message notice ON notice.organization_id = p.organization_id AND notice.event_seq = moved.seq + 1
			JOIN event_log logged ON logged.organization_id = notice.organization_id AND logged.seq = notice.event_seq
			WHERE p.topic_id = $1 AND p.event_seq = $2
			AND posted.kind = 'message.posted' AND posted.data->>'topic_id' = c.default_topic_id::text
			AND moved.kind = 'messages.moved' AND moved.data = jsonb_build_object(
				'channel_id', c.id, 'from_topic_id', c.default_topic_id, 'to_topic_id', p.topic_id,
				'message_ids', jsonb_build_array(p.id))
			AND notice.channel_id = c.id AND notice.topic_id = c.default_topic_id
			AND notice.body = $3 AND logged.kind = 'message.posted'
			AND logged.data->>'message_id' = notice.id::text
			AND logged.data->>'topic_id' = c.default_topic_id::text
		)`, topicID, sourceSeq, "Moved 1 message to "+name+".").Scan(&valid); err != nil || !valid {
			t.Fatalf("%s: invalid branch, notice or events: %v", name, err)
		}
		page, err := reader.Page(t.Context(), member, channelID, &topicID, nil)
		if err != nil || page.Older != (i == 0) || len(page.Entries) != min(want, message.PageSize) {
			t.Fatalf("%s: latest page: %+v, %v", name, page, err)
		}
		if page.Older {
			older, err := reader.Page(t.Context(), member, channelID, &topicID, &page.Entries[0].EventSeq)
			if err != nil || older.Older || len(older.Entries) != 11 || older.Entries[0].EventSeq != sourceSeq {
				t.Fatalf("%s: older page: %+v, %v", name, older, err)
			}
		}
	}
}

func TestArguments(t *testing.T) {
	for _, tc := range []struct {
		args []string
		want []string
	}{
		{[]string{"-messages", "invalid"}, []string{"invalid value"}},
		{[]string{"unexpected"}, []string{
			"usage:", "-messages", "-streams", "-streams-per-account", "-sessions-per-account", "-output",
			"-streams and -output are required together",
		}},
	} {
		err := run(t.Context(), "", tc.args, io.Discard)
		if err == nil {
			t.Fatalf("arguments %v: want an error", tc.args)
		}
		for _, want := range tc.want {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("arguments %v: error %q does not contain %q", tc.args, err, want)
			}
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
	if _, err := identity.ValidatePassword(first); err != nil {
		t.Fatal(err)
	}
}
