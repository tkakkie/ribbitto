package postgres_test

import (
	"errors"
	"math"
	"slices"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/tkakkie/ribbitto/internal/conversation"
	"github.com/tkakkie/ribbitto/internal/domain"
	"github.com/tkakkie/ribbitto/internal/infra/postgres"
	"github.com/tkakkie/ribbitto/internal/infra/postgres/pgtest"
	"github.com/tkakkie/ribbitto/internal/org"
)

func TestChannelMessageSchema(t *testing.T) {
	t.Parallel()
	pool := pgtest.New(t)
	ctx := t.Context()
	// Raw SQL and queries exercise schema constraints directly, including invalid rows.
	channels, messages := postgres.NewChannelStore(pool), postgres.NewMessageStore(pool)
	var nullable int
	requireNoError(t, pool.QueryRow(ctx, "SELECT count(*) FROM information_schema.columns WHERE table_schema = 'public' AND table_name IN ('channel', 'message') AND is_nullable = 'YES'").Scan(&nullable))
	if nullable != 0 {
		t.Fatalf("new tables have %d nullable columns", nullable)
	}
	account := pgtest.Account(t, pool, "a@b", "Author")
	var orgs []domain.ID
	var members []domain.ID
	var defaults []conversation.Channel
	for _, slug := range []string{"team", "other"} {
		organizationID := pgtest.Organization(t, pool, slug, slug, 0)
		member := pgtest.Member(t, pool, organizationID, account, org.RoleMember, "member", 1)
		channel, err := channels.CreateChannel(ctx, organizationID, "雑談", true)
		requireNoError(t, err)
		orgs, members, defaults = append(orgs, organizationID), append(members, member), append(defaults, channel)
		got, err := channels.GetDefaultChannel(ctx, organizationID)
		if err != nil || got != channel {
			t.Fatalf("default: %+v, %v; want %+v", got, err, channel)
		}
	}
	organizationID, other := orgs[0], orgs[1]
	channel, foreign := defaults[0], defaults[1]
	for _, input := range []string{"a", "　 開発 会議　 ", "a　b", " e\u0301 ", strings.Repeat("e\u0301", 80), strings.Repeat("界", 80)} {
		name, err := conversation.ValidateChannelName(input)
		requireNoError(t, err)
		created, err := channels.CreateChannel(ctx, organizationID, name, false)
		requireNoError(t, err)
		got, err := channels.GetChannel(ctx, organizationID, created.ID)
		if err != nil || got != created || got.Name != name || got.IsDefault || got.OrganizationID != organizationID || got.ID[6]>>4 != 7 || got.CreatedAt.IsZero() {
			t.Fatalf("channel round trip: %+v, %v", got, err)
		}
	}
	listed, err := channels.ListChannels(ctx, other)
	if err != nil || !slices.Equal(listed, []conversation.Channel{foreign}) {
		t.Fatalf("channel list leaked: %+v, %v", listed, err)
	}
	_, err = channels.GetChannel(ctx, other, channel.ID)
	if !errors.Is(err, conversation.ErrChannelNotFound) {
		t.Fatalf("cross-organisation channel lookup: %v", err)
	}
	var posted []conversation.Message
	for _, input := range []string{"a", "hello\t", "a\r\nb\rc\nd", "\u00a0\u2002hello\u2003\u3000", "\t\r\n　a \r\n \tb　\n", "a\u00a0b", "e\u0301", "👩\u200d💻", "see \u2067שלום\u2069 now", "\u2066abc\u2069 \u2068x\u2069", "a\u200eb\u200fc\u061cd", strings.Repeat("界", 4000), strings.Repeat("e\u0301", 2000)} {
		body, err := conversation.ValidateMessageBody(input)
		requireNoError(t, err)
		message, err := messages.InsertMessage(ctx, organizationID, channel.ID, channel.DefaultTopicID, members[0], body, int64(len(posted)+1))
		requireNoError(t, err)
		if message.Body != body || message.OrganizationID != organizationID || message.ChannelID != channel.ID || message.TopicID != channel.DefaultTopicID || message.MemberID != members[0] || message.ID[6]>>4 != 7 || message.CreatedAt.IsZero() {
			t.Fatalf("message round trip: %+v", message)
		}
		posted = append(posted, message)
	}
	// Equal sequences in different organisations are valid and must never leak.
	_, err = messages.InsertMessage(ctx, other, foreign.ID, foreign.DefaultTopicID, members[1], "other", 1)
	requireNoError(t, err)
	// A newer message in a sibling channel must not appear in this channel's pages.
	sibling, err := channels.CreateChannel(ctx, organizationID, "sibling", false)
	requireNoError(t, err)
	_, err = messages.InsertMessage(ctx, organizationID, sibling.ID, sibling.DefaultTopicID, members[0], "sibling", 100)
	requireNoError(t, err)
	slices.Reverse(posted)
	for _, tc := range []struct {
		name   string
		org    domain.ID
		before int64
		limit  int32
		want   []conversation.Message
	}{
		{"latest", organizationID, 0, 3, posted[:3]},
		{"max bigint", organizationID, math.MaxInt64, 3, posted[:3]},
		{"next page", organizationID, posted[2].EventSeq, 20, posted[3:]},
		{"oldest boundary", organizationID, 1, 20, nil},
		{"zero limit", organizationID, 0, 0, nil},
		{"other organisation", other, 0, 20, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var before *int64
			if tc.before != 0 {
				before = &tc.before
			}
			got, err := messages.ListMessagesBefore(ctx, tc.org, channel.ID, nil, before, tc.limit)
			if err != nil || !slices.Equal(got, tc.want) {
				t.Fatalf("page: %+v, %v; want %+v", got, err, tc.want)
			}
		})
	}
	for _, tc := range []struct{ name, sql, code string }{
		{"duplicate name", "INSERT INTO channel (organization_id, name) VALUES ($1, '雑談')", "23505"},
		{"second default", "INSERT INTO channel (organization_id, name, is_default) VALUES ($1, 'second', true)", "23505"},
		{"missing organisation", "INSERT INTO channel (organization_id, name) VALUES (uuidv7(), 'missing')", "23503"},
		{"move referenced channel", "UPDATE channel SET organization_id = $2, name = 'moved', is_default = false WHERE organization_id = $1 AND id = $3", "23503"},
		{"foreign channel", "UPDATE message SET channel_id = $4 WHERE organization_id = $1", "23503"},
		{"foreign member", "UPDATE message SET member_id = $5 WHERE organization_id = $1", "23503"},
		{"foreign organisation", "UPDATE message SET organization_id = $2, event_seq = event_seq + 1000 WHERE organization_id = $1", "23503"},
		{"duplicate sequence", "INSERT INTO message (organization_id, channel_id, topic_id, member_id, body, event_seq) SELECT organization_id, channel_id, topic_id, member_id, body, event_seq FROM message WHERE organization_id = $1", "23505"},
		{"zero sequence", "UPDATE message SET event_seq = 0 WHERE organization_id = $1", "23514"},
		{"empty name", "UPDATE channel SET name = '' WHERE organization_id = $1", "23514"},
		{"long name", "UPDATE channel SET name = repeat('界', 81) WHERE organization_id = $1", "23514"},
		{"non-NFC name", "UPDATE channel SET name = 'e\u0301' WHERE organization_id = $1", "23514"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := pool.Exec(ctx, "WITH fixture AS (SELECT $1::uuid, $2::uuid, $3::uuid, $4::uuid, $5::uuid) "+tc.sql, organizationID, other, channel.ID, foreign.ID, members[1])
			var pgErr *pgconn.PgError
			if !errors.As(err, &pgErr) || pgErr.Code != tc.code {
				t.Fatalf("want SQLSTATE %s, got %v", tc.code, err)
			}
		})
	}
	for _, body := range []string{"", strings.Repeat("界", 4001), "a\rb", " hello", "hello ", "\thello", "hello\t", "\nhello", "hello\n", "a\x01b", "a\vb", "a\fb", "a\x1fb", "a\x7fb", "a\u0085b", "a\u009fb", "a\u2028b", "a\u2029b", "a\u202ab", "a\u202bb", "a\u202cb", "a\u202db", "https://evil.example/\u202eexample.com", "a\x00b"} {
		_, err := messages.InsertMessage(ctx, organizationID, channel.ID, channel.DefaultTopicID, members[0], body, 200)
		var pgErr *pgconn.PgError
		code := "23514"
		if strings.ContainsRune(body, 0) {
			code = "22021" // PostgreSQL text rejects NUL before evaluating CHECKs.
		}
		if !errors.As(err, &pgErr) || pgErr.Code != code {
			t.Fatalf("body %q: want SQLSTATE %s, got %v", body, code, err)
		}
	}
}
