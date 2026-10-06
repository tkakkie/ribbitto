package postgres_test

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/tkakkie/ribbitto/internal/conversation"
	"github.com/tkakkie/ribbitto/internal/conversation/internal/postgres"
	"github.com/tkakkie/ribbitto/internal/kernel"
	"github.com/tkakkie/ribbitto/internal/org/orgtest"
	"github.com/tkakkie/ribbitto/internal/platform/postgres/pgtest"
)

func requireNoError(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func TestChannelStore(t *testing.T) {
	t.Parallel()
	pool := pgtest.New(t)
	ctx := t.Context()
	acme := orgtest.Organization(t, pool, "acme", "acme", 0)
	other := orgtest.Organization(t, pool, "other", "other", 0)
	store := postgres.NewChannelStore(pool)
	// A default under another name proves the lookup uses the flag.
	defaultChannel, err := store.CreateDefaultChannelForTest(ctx, acme, "Welcome")
	requireNoError(t, err)
	channel, err := store.CreateChannel(ctx, acme, "開発")
	requireNoError(t, err)
	foreign, err := store.CreateDefaultChannelForTest(ctx, other, "開発")
	requireNoError(t, err)
	for _, want := range []conversation.Channel{defaultChannel, channel, foreign} {
		if want.ID == (kernel.ID{}) || want.DefaultTopicID == (kernel.ID{}) || want.CreatedAt.IsZero() {
			t.Fatalf("incomplete created channel: %+v", want)
		}
		got, err := store.GetChannel(ctx, want.OrganizationID, want.ID)
		requireNoError(t, err)
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("round trip = %+v, want %+v", got, want)
		}
		// Creation on the pool already committed the deferred FK; verify the
		// topic's scoped relationship as well, without any second write.
		var isDefault bool
		var name *string
		requireNoError(t, pool.QueryRow(ctx, "SELECT is_default, name FROM topic WHERE organization_id = $1 AND channel_id = $2 AND id = $3", want.OrganizationID, want.ID, want.DefaultTopicID).Scan(&isDefault, &name))
		if !isDefault || name != nil {
			t.Fatalf("default topic = %v, %v; want true, NULL", isDefault, name)
		}
	}
	if channel.Name != "開発" || channel.OrganizationID != acme || channel.IsDefault || defaultChannel.Name != "Welcome" || !defaultChannel.IsDefault {
		t.Fatalf("created channels = %+v, %+v", channel, defaultChannel)
	}
	got, err := store.GetDefaultChannel(ctx, acme)
	requireNoError(t, err)
	if !reflect.DeepEqual(got, defaultChannel) {
		t.Fatalf("default = %+v, want %+v", got, defaultChannel)
	}
	list, err := store.ListChannels(ctx, acme)
	requireNoError(t, err)
	if !reflect.DeepEqual(list, []conversation.Channel{defaultChannel, channel}) {
		t.Fatalf("list = %+v; want only acme's channels, ordered by name", list)
	}
	empty := orgtest.Organization(t, pool, "empty", "empty", 0)
	list, err = store.ListChannels(ctx, empty)
	requireNoError(t, err)
	if len(list) != 0 {
		t.Fatalf("empty organisation's list = %+v", list)
	}
	for _, tc := range []struct {
		name string
		read func() (conversation.Channel, error)
	}{
		{"foreign channel", func() (conversation.Channel, error) { return store.GetChannel(ctx, acme, foreign.ID) }},
		{"missing channel", func() (conversation.Channel, error) { return store.GetChannel(ctx, acme, kernel.ID{0xee}) }},
		{"missing default", func() (conversation.Channel, error) { return store.GetDefaultChannel(ctx, empty) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := tc.read(); !errors.Is(err, conversation.ErrChannelNotFound) {
				t.Fatalf("error = %v, want ErrChannelNotFound", err)
			}
		})
	}
}

func TestChannelStoreCreateErrors(t *testing.T) {
	t.Parallel()
	pool := pgtest.New(t)
	ctx := t.Context()
	organizationID := orgtest.Organization(t, pool, "acme", "acme", 0)
	store := postgres.NewChannelStore(pool)
	_, err := store.CreateDefaultChannelForTest(ctx, organizationID, "taken")
	requireNoError(t, err)
	for _, tc := range []struct {
		name string
		org  kernel.ID
		want error
		code string
	}{
		{"", organizationID, conversation.ErrInvalidChannelName, "23514"},
		{strings.Repeat("a", 81), organizationID, conversation.ErrInvalidChannelName, "23514"},
		{"taken", organizationID, conversation.ErrChannelNameTaken, ""},
		{"unknown", kernel.ID{0xee}, nil, "23503"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := store.CreateChannel(ctx, tc.org, tc.name)
			if tc.want != nil && !errors.Is(err, tc.want) {
				t.Fatalf("error = %v, want %v", err, tc.want)
			}
			var pgErr *pgconn.PgError
			if tc.code != "" && (!errors.As(err, &pgErr) || pgErr.Code != tc.code) {
				t.Fatalf("error = %v, want wrapped PostgreSQL %s", err, tc.code)
			}
		})
	}
	var channels, topics int
	requireNoError(t, pool.QueryRow(ctx, "SELECT (SELECT count(*) FROM channel), (SELECT count(*) FROM topic)").Scan(&channels, &topics))
	if channels != 1 || topics != 1 {
		t.Fatalf("rows after failed creates = %d channels, %d topics; want 1 each", channels, topics)
	}
}
