package topic

import (
	"context"
	"errors"
	"fmt"

	"github.com/tkakkie/ribbitto/internal/domain"
	"github.com/tkakkie/ribbitto/internal/org"
)

// MaxBranchMessages bounds how many messages one branch may move, so its
// transaction and its event payload stay small.
const MaxBranchMessages = 100

// ErrConflict means a selected message is no longer in the topic the
// request expected (someone else branched it first): nothing changed.
var ErrConflict = errors.New("a selected message is no longer in the expected topic")

// ErrInvalidBranch means the request itself is unusable: no messages, too
// many, a repeated one, no destination or two, or the source as destination.
var ErrInvalidBranch = errors.New("invalid branch request")

// Branch moves Messages, expected in topic From, to an existing topic To or
// to a new topic named NewName, in the same channel.
type Branch struct {
	Messages []domain.ID
	From     domain.ID
	To       *domain.ID
	NewName  string
}

// BranchStore runs a branch in one transaction (docs/domain/topics.md
// *Branching*): it takes the organisation's next two sequences, the move's
// and then the notice's, creates the destination if it is new, moves the
// messages, posts the notice in the source topic and records both events.
// notice gives the notice's body for the destination. It returns the
// destination and the notice's sequence, the last one committed. A topic
// outside the organisation and channel is ErrNotFound; a message not in
// From is ErrConflict, and then nothing is written.
type BranchStore interface {
	Branch(ctx context.Context, organizationID, channelID, memberID domain.ID, b Branch, notice func(domain.Topic) string) (domain.Topic, int64, error)
}

// Notifier records the latest committed event sequence for an organisation.
type Notifier interface {
	Raise(organizationID domain.ID, seq int64)
}

// Brancher runs branching for a member resolved by org. Any member may
// branch any message of a channel they can read: in the MVP every member
// reads and writes every public channel (docs/domain/topics.md).
type Brancher struct {
	store    BranchStore
	notifier Notifier
}

// NewBrancher returns branching over store, telling notifier (which may be
// nil) the committed sequence so open streams wake.
func NewBrancher(store BranchStore, notifier Notifier) *Brancher {
	return &Brancher{store: store, notifier: notifier}
}

// Branch validates b and runs it in the member's organisation and the
// channel. It returns the destination topic.
func (s *Brancher) Branch(ctx context.Context, m org.Membership, channelID domain.ID, b Branch, notice func(domain.Topic) string) (domain.Topic, error) {
	if err := validBranch(&b); err != nil {
		return domain.Topic{}, err
	}
	destination, seq, err := s.store.Branch(ctx, m.Organization.ID, channelID, m.Member.ID, b, notice)
	if err != nil {
		return domain.Topic{}, fmt.Errorf("branching: %w", err)
	}
	if s.notifier != nil {
		s.notifier.Raise(m.Organization.ID, seq)
	}
	return destination, nil
}

func validBranch(b *Branch) error {
	if len(b.Messages) == 0 || len(b.Messages) > MaxBranchMessages {
		return fmt.Errorf("%w: %d messages, want 1–%d", ErrInvalidBranch, len(b.Messages), MaxBranchMessages)
	}
	seen := make(map[domain.ID]bool, len(b.Messages))
	for _, id := range b.Messages {
		if seen[id] {
			return fmt.Errorf("%w: a message is selected twice", ErrInvalidBranch)
		}
		seen[id] = true
	}
	switch {
	case b.To != nil && b.NewName != "":
		return fmt.Errorf("%w: both an existing and a new destination", ErrInvalidBranch)
	case b.To != nil && *b.To == b.From:
		return fmt.Errorf("%w: the destination is the source", ErrInvalidBranch)
	case b.To == nil:
		name, err := domain.ValidateTopicName(b.NewName)
		if err != nil {
			return fmt.Errorf("%w: %w", ErrInvalidName, err)
		}
		b.NewName = name
	}
	return nil
}
