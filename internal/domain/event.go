package domain

import "errors"

// ErrCursorExpired means replay cannot cover every sequence after the cursor.
// The cursor is below the replay boundary or above the committed event_seq.
var ErrCursorExpired = errors.New("event cursor expired")

// EventKind identifies a durable change. Readers skip unknown kinds.
type EventKind string

const (
	// EventMessagePosted names a message and its channel.
	EventMessagePosted EventKind = "message.posted"
	// EventMemberJoined names the member joining an organisation.
	EventMemberJoined EventKind = "member.joined"
)

// Event is a durable change ordered within an organisation. A nil
// AudienceMemberID means organisation-wide; otherwise only that member may
// receive it. Message events name ChannelID and MessageID; join events name
// MemberID. IDs unused by the kind are zero. No content or HTML is carried.
type Event struct {
	OrganizationID   ID
	Seq              int64
	Kind             EventKind
	AudienceMemberID *ID
	ChannelID        ID
	MessageID        ID
	MemberID         ID
}
