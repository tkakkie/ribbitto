package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/tkakkie/ribbitto/internal/identity/identitypg"
	"github.com/tkakkie/ribbitto/internal/org/orgpg"
	"github.com/tkakkie/ribbitto/internal/platform/postgres/pgtest"
)

func TestStage2Preflight(t *testing.T) {
	for _, args := range [][]string{
		{"-stage2"}, {"-members", "2"}, {"-fixture-seed", "0"},
		{"-stage2", "-members", "2", "-output", "/tmp/unused.json"},
		{"-stage2", "-fixture-seed", "0", "-output", "/tmp/unused.json"},
		{"-stage2", "-members", "0", "-fixture-seed", "0", "-output", "/tmp/unused.json"},
		{"-stage2", "-members", "98001", "-fixture-seed", "0", "-output", "/tmp/unused.json"},
	} {
		if err := run(t.Context(), "postgres://127.0.0.1:1/unreachable", args, io.Discard); err == nil || !strings.Contains(err.Error(), "-stage2") {
			t.Fatalf("stage-2 preflight %v: %v", args, err)
		}
	}
	for _, flag := range []string{"messages", "streams", "streams-per-account", "sessions-per-account", "expected", "after", "tokens"} {
		args := []string{"-stage2", "-members", "2", "-fixture-seed", "0", "-output", "/tmp/unused.json", "-" + flag}
		if flag != "expected" {
			args = append(args, "1")
		}
		if err := run(t.Context(), "postgres://127.0.0.1:1/unreachable", args, io.Discard); err == nil || strings.Contains(err.Error(), "opening RIBBITTO_DATABASE_URL") {
			t.Fatalf("mixed mode %s reached database: %v", flag, err)
		}
	}
}

func TestStage2Seed(t *testing.T) {
	var previous snapshot
	for runNumber, seed := range []uint64{0, 0, 1} {
		pool := pgtest.New(t)
		address, err := url.Parse(pool.Config().ConnString())
		if err != nil {
			t.Fatal(err)
		}
		address.Path = "/" + pool.Config().ConnConfig.Database
		path := filepath.Join(t.TempDir(), "stage2.json")
		var out bytes.Buffer
		if err := run(t.Context(), address.String(), []string{"-stage2", "-members", "3", "-fixture-seed", strconv.FormatUint(seed, 10), "-output", path}, &out); err != nil {
			t.Fatal(err)
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
		if len(got.Members) != 3 || len(got.Channels) != 4 || len(got.Topics) != 6 || len(got.Messages) != 2000 {
			t.Fatalf("fixture counts: members=%d channels=%d topics=%d messages=%d", len(got.Members), len(got.Channels), len(got.Topics), len(got.Messages))
		}
		counts := map[string]int{}
		for _, message := range got.Messages {
			counts[message.Channel]++
			counts[message.Channel+"/"+message.Topic]++
		}
		for _, channel := range got.Channels {
			if counts[channel.Name] != 500 || (channel.Name != "general" && counts[channel.Name+"/"] != 500) {
				t.Fatalf("channel %s: messages=%d default=%d; want 500", channel.Name, counts[channel.Name], counts[channel.Name+"/"])
			}
		}
		if counts["general/"] != 250 || counts["general/stage2-topic-1"] != 125 || counts["general/stage2-topic-2"] != 125 {
			t.Fatalf("hot distribution: default=%d topic1=%d topic2=%d; want 250/125/125", counts["general/"], counts["general/stage2-topic-1"], counts["general/stage2-topic-2"])
		}
		var organizations, sessions int
		if err := pool.QueryRow(t.Context(), "SELECT (SELECT count(*) FROM organization), (SELECT count(*) FROM session)").Scan(&organizations, &sessions); err != nil || organizations != 1 || sessions != 3 {
			t.Fatalf("organization/session counts: %d/%d: %v", organizations, sessions, err)
		}
		if file.Stage2 == nil || file.Stage2.Version != 1 || file.Stage2.FixtureSeed != seed || len(file.Accounts) != 3 || len(file.ChannelIDs) != 4 || len(file.Stage2.DefaultTopicIDs) != 4 || len(file.Stage2.NamedTopics) != 2 || file.Stage2.HotChannelID != file.ChannelIDs[0] {
			t.Fatal("stage-2 metadata counts or recipe differ")
		}
		for i, channel := range file.ChannelIDs {
			var valid bool
			if err := pool.QueryRow(t.Context(), "SELECT EXISTS(SELECT FROM channel c JOIN organization o ON o.id = c.organization_id WHERE c.id = $1 AND c.default_topic_id = $2 AND o.slug = $3)", channel, file.Stage2.DefaultTopicIDs[i], file.OrganizationSlug).Scan(&valid); err != nil || !valid {
				t.Fatalf("invalid channel/default-topic metadata: %v", err)
			}
		}
		for _, topic := range file.Stage2.NamedTopics {
			var valid bool
			if err := pool.QueryRow(t.Context(), "SELECT EXISTS(SELECT FROM topic WHERE id = $1 AND channel_id = $2 AND name = $3 AND NOT is_default)", topic.ID, file.Stage2.HotChannelID, topic.Name).Scan(&valid); err != nil || !valid {
				t.Fatalf("invalid named-topic metadata: %v", err)
			}
		}
		seen := map[string]bool{}
		for _, entry := range file.Accounts {
			if len(entry.Tokens) != 1 || seen[entry.MemberID] {
				t.Fatal("members must be distinct with one session each")
			}
			seen[entry.MemberID] = true
			account, _, err := identitypg.NewSessions(pool, time.Now, nil).Resolve(t.Context(), entry.Tokens[0])
			if err != nil {
				t.Fatal(err)
			}
			member, err := orgpg.NewAuthorizer(pool).Member(t.Context(), &account, file.OrganizationSlug)
			if err != nil || seedID(member.Member.ID) != entry.MemberID || member.Member.Handle != entry.Handle || strings.Contains(out.String(), entry.Tokens[0]) {
				t.Fatalf("member/session metadata or token secrecy: %v", err)
			}
		}
		if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0600 {
			t.Fatalf("stage-2 credential permissions: %v", err)
		}
		if runNumber == 1 && !reflect.DeepEqual(got, previous) {
			t.Fatal("same fixture seed changed members, channels, topics, bodies, authors or order")
		}
		if runNumber == 2 && reflect.DeepEqual(got, previous) {
			t.Fatal("different fixture seed did not change content")
		}
		previous = got
	}
}
