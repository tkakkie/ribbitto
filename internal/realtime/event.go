package realtime

import (
	"errors"
	"fmt"

	"github.com/tkakkie/ribbitto/internal/kernel"
)

// ErrCursorExpired means the cursor is outside the valid replay range: below
// the replay boundary or above the committed event_seq.
var ErrCursorExpired = errors.New("event cursor expired")

// EventKind identifies a durable change. The reader keeps an unregistered
// kind's envelope with no channel or topics, so streams skip it.
type EventKind string

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

// MergeKinds combines publisher registries into a fresh map without modifying
// them. A kind registered more than once is an error, even with the same
// router; on error it returns no registry.
func MergeKinds(registries ...Kinds) (Kinds, error) {
	merged := make(Kinds)
	for _, registry := range registries {
		for kind, router := range registry {
			if _, exists := merged[kind]; exists {
				return nil, fmt.Errorf("duplicate event kind %q", kind)
			}
			merged[kind] = router
		}
	}
	return merged, nil
}
