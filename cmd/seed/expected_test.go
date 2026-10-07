package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/tkakkie/ribbitto/internal/conversation"
	"github.com/tkakkie/ribbitto/internal/conversation/conversationpg"
	"github.com/tkakkie/ribbitto/internal/conversation/conversationtest"
	"github.com/tkakkie/ribbitto/internal/identity"
	"github.com/tkakkie/ribbitto/internal/identity/identitypg"
	"github.com/tkakkie/ribbitto/internal/org"
	"github.com/tkakkie/ribbitto/internal/org/orgpg"
	"github.com/tkakkie/ribbitto/internal/platform/postgres/pgtest"
)

func TestExpectedPreflight(t *testing.T) {
	dir := t.TempDir()
	tokens, output := filepath.Join(dir, "tokens.json"), filepath.Join(dir, "expected.json")
	valid := `{"organization_slug":"test","channel_ids":["00000000-0000-0000-0000-000000000001"],"accounts":[{"handle":"owner","tokens":["unknown"]}]}`
	for _, tc := range []struct{ name, input, database, output, want string }{
		{"remote", valid, "postgres://192.0.2.1/db", output, "local development database"},
		{"json", `{`, "", output, "invalid credential file"},
		{"missing", `{}`, "", output, "invalid credential file"},
		{"trailing", valid + `{}`, "", output, "invalid credential file"},
		{"slug", strings.Replace(valid, `"test"`, `"INVALID"`, 1), "", output, "organization slug"},
		{"channel", strings.Replace(valid, "00000000-0000-0000-0000-000000000001", "bad", 1), "", output, "channel ID"},
		{"tokens", strings.Replace(valid, `["unknown"]`, `[]`, 1), "", output, "missing credential tokens"},
		{"existing", valid, "", tokens, "file exists"},
		{"repository", valid, "", "expected.json", "outside repositories"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := os.WriteFile(tokens, []byte(tc.input), 0600); err != nil {
				t.Fatal(err)
			}
			if tc.database == "" {
				tc.database = "postgres://127.0.0.1:1/unreachable?sslmode=disable"
			}
			args := []string{"-expected", "-tokens", tokens, "-after", "0", "-output", tc.output}
			if err := run(t.Context(), tc.database, args, io.Discard); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("pre-connection refusal: %v", err)
			}
			if raw, err := os.ReadFile(tokens); err != nil || string(raw) != tc.input {
				t.Fatal("input/existing output changed")
			}
		})
	}
}

func TestExpected(t *testing.T) {
	pool := pgtest.New(t)
	address, err := url.Parse(pool.Config().ConnString())
	if err != nil {
		t.Fatal(err)
	}
	address.Path = "/" + pool.Config().ConnConfig.Database
	dir := t.TempDir()
	tokens := filepath.Join(dir, "tokens.json")
	if err := run(t.Context(), address.String(), []string{"-messages", strconv.Itoa(conversation.PageSize + 2), "-streams", "1", "-output", tokens}, io.Discard); err != nil {
		t.Fatal(err)
	}
	data, channel, err := readExpectedCredentials(tokens)
	if err != nil {
		t.Fatal(err)
	}
	account, _, err := identitypg.NewSessions(pool, time.Now, nil).Resolve(t.Context(), data.Accounts[0].Tokens[0])
	if err != nil {
		t.Fatal(err)
	}
	m, err := orgpg.NewAuthorizer(pool).Member(t.Context(), &account, data.OrganizationSlug)
	if err != nil {
		t.Fatal(err)
	}
	marker := "loadgen" + strings.Repeat("A", 26) + "0Z"
	markers := map[uint64]string{}
	posts := conversationpg.NewPosting(pool, postingSequence, postingEvents, nil)
	for _, tc := range []struct{ body, marker string }{
		{marker + "&&", marker}, {"loadgen" + strings.Repeat("2", 26) + "12Zx", "loadgen" + strings.Repeat("2", 26) + "12Z"},
		{"plain", ""}, {"prefix " + marker, ""}, {"loadgen" + strings.Repeat("A", 26) + "01Z", ""},
		{"loadgen" + strings.Repeat("A", 25) + "0Z", ""}, {"loadgen" + strings.Repeat("A", 27) + "0Z", ""}, {"loadgen" + strings.Repeat("a", 26) + "0Z", ""},
	} {
		msg, err := posts.Post(t.Context(), m, channel, tc.body)
		if err != nil {
			t.Fatal(err)
		}
		markers[uint64(msg.EventSeq)] = tc.marker
	}
	extra := conversationtest.Channel(t, pool, m.Organization.ID, "watermark", false)
	if _, err := posts.Post(t.Context(), m, extra.ID, "outside the selected channel"); err != nil {
		t.Fatal(err)
	}
	other := conversationtest.OrganizationWithOwner(t, pool, "other", "general")
	emptyToken, _, err := identitypg.NewSessions(pool, time.Now, nil).Create(t.Context(), other.AccountID)
	if err != nil {
		t.Fatal(err)
	}
	id := other.Channel.ID
	otherChannel := fmt.Sprintf("%x-%x-%x-%x-%x", id[:4], id[4:6], id[6:8], id[8:10], id[10:])
	before := readSnapshot(t, pool)
	var sequences []uint64
	for _, msg := range before.Messages {
		if msg.Channel == "general" {
			sequences = append(sequences, uint64(msg.Seq))
		}
	}
	original := data
	for i, tc := range []struct {
		name                 string
		after                uint64
		slug, channel, token string
		wantErr              error
	}{
		{name: "all"}, {name: "boundary", after: sequences[1]},
		{name: "caught up", after: uint64(before.Seq)},
		{name: "empty", slug: "other", channel: otherChannel, token: emptyToken},
		{name: "unknown", channel: otherChannel, token: strings.Repeat("A", 43), wantErr: identity.ErrNoSession},
		{name: "non-member", slug: "other", wantErr: org.ErrNotFound},
		{name: "foreign channel", channel: otherChannel, wantErr: conversation.ErrChannelNotFound},
		{name: "expired", channel: otherChannel, token: "expired", wantErr: identity.ErrNoSession},
	} {
		t.Run(tc.name, func(t *testing.T) {
			data = original
			data.Accounts = append([]accountTokens(nil), original.Accounts...)
			data.Accounts[0].Tokens = append([]string(nil), original.Accounts[0].Tokens...)
			data.ChannelIDs = append([]string(nil), original.ChannelIDs...)
			if tc.slug != "" {
				data.OrganizationSlug = tc.slug
			}
			if tc.channel != "" {
				data.ChannelIDs[0] = tc.channel
			}
			if tc.token != "" {
				data.Accounts[0].Tokens[0] = tc.token
			}
			if tc.token == "expired" {
				data.Accounts[0].Tokens[0] = original.Accounts[0].Tokens[0]
				if _, err := pool.Exec(t.Context(), "UPDATE session SET created_at = now() - interval '2 seconds', expires_at = now() - interval '1 second'"); err != nil {
					t.Fatal(err)
				}
			}
			raw, _ := json.Marshal(data)
			if err := os.WriteFile(tokens, raw, 0600); err != nil {
				t.Fatal(err)
			}
			output := filepath.Join(dir, fmt.Sprintf("expected-%d.json", i))
			args := []string{"-expected", "-tokens", tokens, "-after", strconv.FormatUint(tc.after, 10), "-output", output}
			err := run(t.Context(), address.String(), args, io.Discard)
			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("refusal: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			wantMessages := []map[string]any{}
			for _, seq := range sequences {
				if seq > tc.after {
					wantMessages = append(wantMessages, map[string]any{"sequence": seq, "marker": markers[seq]})
				}
			}
			want := map[string]any{"header": map[string]any{"version": 1, "kind": "expected", "organization_slug": original.OrganizationSlug, "channel_id": original.ChannelIDs[0], "initial_cursor": tc.after, "final_watermark": before.Seq}, "messages": wantMessages}
			if tc.name == "empty" {
				want["header"] = map[string]any{"version": 1, "kind": "expected", "organization_slug": "other", "channel_id": otherChannel, "initial_cursor": 0, "final_watermark": 1}
				want["messages"] = []map[string]any{}
			}
			wantRaw, _ := json.Marshal(want)
			gotRaw, err := os.ReadFile(output)
			if err != nil {
				t.Fatal(err)
			}
			var gotJSON, wantJSON any
			if json.Unmarshal(gotRaw, &gotJSON) != nil || json.Unmarshal(wantRaw, &wantJSON) != nil || !reflect.DeepEqual(gotJSON, wantJSON) {
				t.Fatalf("expected set: %s; want %s", gotRaw, wantRaw)
			}
			if info, err := os.Stat(output); err != nil || info.Mode().Perm() != 0600 {
				t.Fatalf("permissions: %v", err)
			}
		})
	}
	if !reflect.DeepEqual(before, readSnapshot(t, pool)) {
		t.Fatal("expected mode changed database messages or event_seq")
	}
}
