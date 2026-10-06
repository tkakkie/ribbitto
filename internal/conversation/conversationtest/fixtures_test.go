package conversationtest_test

import (
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/tkakkie/ribbitto/internal/conversation/conversationtest"
	"github.com/tkakkie/ribbitto/internal/identity/identitytest"
	"github.com/tkakkie/ribbitto/internal/kernel"
	"github.com/tkakkie/ribbitto/internal/org"
	"github.com/tkakkie/ribbitto/internal/org/orgtest"
	"github.com/tkakkie/ribbitto/internal/platform/postgres/pgtest"
)

// Later tests rely on these premises, so a fixture that "fixes" a bypassed
// invariant (an event row, a setup row, an extra membership) must fail here.
// Counts are database-wide: each test gets a fresh database.
func TestOrganizationWithOwner(t *testing.T) {
	t.Parallel()
	pool := pgtest.New(t)
	fixture := conversationtest.OrganizationWithOwner(t, pool, "acme", "general")
	eventSeq := "SELECT event_seq FROM organization WHERE id = $1"
	boundary := "SELECT event_log_boundary_seq FROM organization WHERE id = $1"
	wantInt(t, pool, "event_seq", 1, eventSeq, fixture.OrganizationID)
	wantInt(t, pool, "event_log_boundary_seq", 0, boundary, fixture.OrganizationID)
	wantInt(t, pool, "event_log rows", 0, "SELECT count(*) FROM event_log")
	wantInt(t, pool, "setup rows", 0, "SELECT count(*) FROM setup")
	wantInt(t, pool, "members", 1, "SELECT count(*) FROM member")
	wantInt(t, pool, "channels", 1, "SELECT count(*) FROM channel")

	type owner struct {
		member, account                                kernel.ID
		role, handle, email, name, hash, slug, orgName string
		joined                                         int64
	}
	var got owner
	if err := pool.QueryRow(t.Context(), `
SELECT m.id, m.account_id, m.role, m.handle, a.email, a.display_name, a.password_hash, o.slug, o.name, m.joined_event_seq
FROM member m JOIN account a ON a.id = m.account_id JOIN organization o ON o.id = m.organization_id
WHERE m.organization_id = $1`, fixture.OrganizationID).Scan(&got.member, &got.account, &got.role, &got.handle, &got.email, &got.name, &got.hash, &got.slug, &got.orgName, &got.joined); err != nil {
		t.Fatalf("reading owner: %v", err)
	}
	want := owner{fixture.MemberID, fixture.AccountID, "owner", "owner", "acme@example.org", "acme", "$argon2id$x", "acme", "acme", 1}
	if got != want {
		t.Fatalf("owner = %+v, want %+v", got, want)
	}

	channel := fixture.Channel
	if channel.OrganizationID != fixture.OrganizationID || channel.Name != "general" || !channel.IsDefault {
		t.Fatalf("channel = %+v, want the organisation's default channel general", channel)
	}
	wantInt(t, pool, "default channels with their default topic", 1, `
SELECT count(*) FROM channel c JOIN topic d ON d.id = c.default_topic_id AND d.channel_id = c.id AND d.is_default
WHERE c.id = $1 AND c.organization_id = $2 AND c.is_default AND c.default_topic_id = $3`, channel.ID, fixture.OrganizationID, channel.DefaultTopicID)

	other := identitytest.Account(t, pool, "other@example.org", "other")
	wantInt(t, pool, "members after Account", 1, "SELECT count(*) FROM member")
	orgtest.Member(t, pool, fixture.OrganizationID, other, org.RoleMember, "other", 1)
	wantInt(t, pool, "event_seq after Member", 1, eventSeq, fixture.OrganizationID)
	wantInt(t, pool, "event_log_boundary_seq after Member", 0, boundary, fixture.OrganizationID)
	conversationtest.Channel(t, pool, fixture.OrganizationID, "random", false)
	wantInt(t, pool, "event_seq after Channel", 1, eventSeq, fixture.OrganizationID)
	wantInt(t, pool, "event_log_boundary_seq after Channel", 0, boundary, fixture.OrganizationID)
}

func wantInt(t *testing.T, pool *pgxpool.Pool, what string, want int64, query string, args ...any) {
	t.Helper()
	var got int64
	if err := pool.QueryRow(t.Context(), query, args...).Scan(&got); err != nil {
		t.Fatalf("reading %s: %v", what, err)
	}
	if got != want {
		t.Fatalf("%s = %d, want %d", what, got, want)
	}
}
