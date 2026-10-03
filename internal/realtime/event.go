package realtime

import (
	"errors"

	"github.com/tkakkie/ribbitto/internal/domain"
)

// ErrCursorExpired means the cursor is outside the valid replay range: below
// the replay boundary or above the committed event_seq.
var ErrCursorExpired = errors.New("event cursor expired")

// EventKind identifies a durable change. Readers skip unknown kinds.
type EventKind string

const (
	// EventMessagePosted names a message, its channel and posting-time topic.
	EventMessagePosted EventKind = "message.posted"
	// EventMemberJoined names the member joining an organisation.
	EventMemberJoined EventKind = "member.joined"
	// EventMessagesMoved names the messages branching moved, their channel,
	// and the topics they left and joined (docs/domain/topics.md).
	EventMessagesMoved EventKind = "messages.moved"
)

// Event is a durable change ordered within an organisation. A nil
// AudienceMemberID means organisation-wide; otherwise only that member may
// receive it. Posted events name ChannelID, MessageID and TopicID; moves name
// ChannelID, FromTopicID, ToTopicID and MessageIDs; join events name MemberID.
// IDs unused by the kind are zero. No content or HTML is carried.
type Event struct {
	OrganizationID   domain.ID
	Seq              int64
	Kind             EventKind
	AudienceMemberID *domain.ID
	ChannelID        domain.ID
	MessageID        domain.ID
	// TopicID is the posting-time topic, immutable after reading. Nil means
	// a legacy message.posted payload without topic_id.
	TopicID     *domain.ID
	MemberID    domain.ID
	FromTopicID domain.ID
	ToTopicID   domain.ID
	// MessageIDs is immutable after reading, since event batches are shared.
	MessageIDs []domain.ID
}
