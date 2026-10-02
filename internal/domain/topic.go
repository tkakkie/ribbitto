package domain

import "time"

// Topic is a named conversation inside a channel, identified by ID. The
// default topic has no Name: the UI shows its label from the message files.
type Topic struct {
	ID, OrganizationID, ChannelID ID
	Name                          string
	IsDefault                     bool
	CreatedAt                     time.Time
}

// ValidateTopicName returns a trimmed NFC name of 1–80 printable Unicode
// code points, the same rule as channel names (docs/domain/topics.md).
func ValidateTopicName(value string) (string, error) {
	return validateText(value, 1, 80, "topic name")
}
