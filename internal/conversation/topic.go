package conversation

import (
	"errors"
	"strings"
	"time"
	"unicode/utf8"

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

// NoticeNameLength bounds a topic's name inside a branch notice, in Unicode
// code points, ellipsis included, so the notice stays one line in the message
// list (docs/domain/topics.md).
const NoticeNameLength = 40

// ShortTopicName shortens a topic name for a branch notice to
// NoticeNameLength, ending a shortened one with an ellipsis. It cuts on a
// character boundary, so the result is always valid UTF-8.
func ShortTopicName(name string) string {
	if len(name) <= NoticeNameLength {
		return name
	}
	cut := NoticeNameLength - 1
	for cut > 0 && !utf8.RuneStart(name[cut]) {
		cut--
	}
	return strings.TrimRight(name[:cut], " ") + "…"
}

// ErrTopicNotFound means the topic does not exist in the given organisation and
// channel, including when it exists in another channel or organisation.
var ErrTopicNotFound = errors.New("topic not found")

// ErrInvalidTopicName wraps a name that breaks ValidateTopicName.
var ErrInvalidTopicName = errors.New("invalid topic name")

// ErrTopicNameTaken means the channel already has a topic with that name,
// ignoring case.
var ErrTopicNameTaken = errors.New("topic name already taken")
