package conversation

import (
	"errors"
	"time"

	"github.com/tkakkie/ribbitto/internal/kernel"
)

// Topic is a named conversation inside a channel, identified by ID. The
// default topic has no Name: the UI shows its label from the message files.
type Topic struct {
	ID, OrganizationID, ChannelID kernel.ID
	Name                          string
	IsDefault                     bool
	CreatedAt                     time.Time
}

// ValidateTopicName returns a trimmed NFC name of 1–80 printable Unicode
// code points, the same rule as channel names (docs/domain/topics.md).
func ValidateTopicName(value string) (string, error) {
	return validateText(value, 1, 80, "topic name")
}

// ErrTopicNotFound means the topic does not exist in the given organisation and
// channel, including when it exists in another channel or organisation.
var ErrTopicNotFound = errors.New("topic not found")

// ErrInvalidTopicName wraps a name that breaks ValidateTopicName.
var ErrInvalidTopicName = errors.New("invalid topic name")

// ErrTopicNameTaken means the channel already has a topic with that name,
// ignoring case.
var ErrTopicNameTaken = errors.New("topic name already taken")
