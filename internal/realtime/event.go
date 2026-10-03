package realtime

import (
	"errors"

	"github.com/tkakkie/ribbitto/internal/kernel"
)

// ErrCursorExpired means the cursor is outside the valid replay range: below
// the replay boundary or above the committed event_seq.
var ErrCursorExpired = errors.New("event cursor expired")

// EventKind identifies a durable change. The reader keeps an unregistered
// kind's envelope with no channel or topics, so streams skip it.
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

// Event is a durable change ordered within an organisation: an envelope
// that realtime routes without knowing its kind (decision 26). A nil
// AudienceMemberID means organisation-wide; otherwise only that member may
// receive it. ChannelID is zero for a kind that is not channel-scoped.
// Topics are the routing topics its kind's Router gave; nil when it names
// none (a legacy post, or a kind without topics). Payload is the stored
// data, which consumers decode through the kind's publisher. Topics and Payload are
// read-only once read, since event batches are shared between streams. No
// content or HTML is carried.
type Event struct {
	OrganizationID   kernel.ID
	Seq              int64
	Kind             EventKind
	AudienceMemberID *kernel.ID
	ChannelID        kernel.ID
	Topics           []kernel.ID
	Payload          []byte
}

// Router reads an event's routing from its stored payload: its channel
// (zero when the kind is not channel-scoped) and its routing topics. It
// fails on malformed data. Each kind's publisher provides one (decision 26).
type Router func(payload []byte) (channel kernel.ID, topics []kernel.ID, err error)

// Kinds registers the Router of each kind a reader fills in; wiring builds
// it. A kind it lacks keeps only its envelope, and streams skip it.
type Kinds map[EventKind]Router
