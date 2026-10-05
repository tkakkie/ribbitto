package conversation

import (
	"context"
	"errors"
	"fmt"

	"github.com/tkakkie/ribbitto/internal/kernel"
	"github.com/tkakkie/ribbitto/internal/org"
)

// MaxBranchMessages bounds how many messages one branch may move, so its
// transaction and its event payload stay small.
const MaxBranchMessages = 100

// ErrBranchConflict means a selected message is no longer in the topic the
// request expected (someone else branched it first): nothing changed.
var ErrBranchConflict = errors.New("a selected message is no longer in the expected topic")

// ErrInvalidBranch means the request itself is unusable: no messages, too
// many, a repeated one, no destination or two, or the source as destination.
var ErrInvalidBranch = errors.New("invalid branch request")

// Branch moves Messages, expected in topic From, to an existing topic To or
// to a new topic named NewName, in the same channel.
type Branch struct {
	Messages []kernel.ID
	From     kernel.ID
	To       *kernel.ID
	NewName  string
}

// Brancher runs branching for a member resolved by org. Any member may
// branch any message of a channel they can read: in the MVP every member
// reads and writes every public channel (docs/domain/topics.md).
type Brancher struct {
	runner    TxRunner
	writer    WriterIn
	sequences EventSequenceIn
	events    EventAppenderIn
	notifier  Notifier
}

// NewBrancher builds branching over transaction-bound factories. A nil notifier
// disables commit notifications.
func NewBrancher(runner TxRunner, writer WriterIn, sequences EventSequenceIn, events EventAppenderIn, notifier Notifier) *Brancher {
	return &Brancher{runner: runner, writer: writer, sequences: sequences, events: events, notifier: notifier}
}

// Branch validates b and runs it in the member's organisation and the
// channel. It returns the destination topic.
func (s *Brancher) Branch(ctx context.Context, m org.Membership, channelID kernel.ID, b Branch, notice func(Topic) string) (Topic, error) {
	if err := validBranch(&b); err != nil {
		return Topic{}, err
	}
	destination, seq, err := s.run(ctx, m.Organization.ID, channelID, m.Member.ID, b, notice)
	if err != nil {
		return Topic{}, fmt.Errorf("branching: %w", err)
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
	seen := make(map[kernel.ID]bool, len(b.Messages))
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
		name, err := ValidateTopicName(b.NewName)
		if err != nil {
			return fmt.Errorf("%w: %w", ErrInvalidTopicName, err)
		}
		b.NewName = name
	}
	return nil
}
