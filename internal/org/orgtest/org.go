package orgtest

import (
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/tkakkie/ribbitto/internal/kernel"
	"github.com/tkakkie/ribbitto/internal/org"
)

// Organization inserts only an organisation, with the given event_seq.
// Compose it with identitytest.Account, Member and conversationtest.Channel
// for a full fixture without setup.
func Organization(t *testing.T, pool *pgxpool.Pool, slug, name string, eventSeq int64) kernel.ID {
	t.Helper()
	var id kernel.ID
	if err := pool.QueryRow(t.Context(), "INSERT INTO organization (slug, name, event_seq) VALUES ($1, $2, $3) RETURNING id", slug, name, eventSeq).Scan(&id); err != nil {
		t.Fatalf("inserting organization: %v", err)
	}
	return id
}

// Member inserts a membership with the given role, handle and joined_event_seq.
// It leaves the organisation's event_seq unchanged, including for partial fixtures.
func Member(t *testing.T, pool *pgxpool.Pool, orgID, account kernel.ID, role org.Role, handle string, joinedEventSeq int64) kernel.ID {
	t.Helper()
	var id kernel.ID
	if err := pool.QueryRow(t.Context(), "INSERT INTO member (organization_id, account_id, role, joined_event_seq, handle) VALUES ($1, $2, $3, $4, $5) RETURNING id", orgID, account, role, joinedEventSeq, handle).Scan(&id); err != nil {
		t.Fatalf("inserting member: %v", err)
	}
	return id
}
