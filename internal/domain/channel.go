package domain

import "time"

// Channel is a conversation identified by ID; Name is only its display name.
type Channel struct {
	ID, OrganizationID ID
	Name               string
	IsDefault          bool
	CreatedAt          time.Time
}

// ValidateChannelName returns a trimmed NFC name of 1–80 printable Unicode code points.
func ValidateChannelName(value string) (string, error) {
	return validateText(value, 1, 80, "channel name")
}
