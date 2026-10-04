package conversation

import (
	"time"

	"github.com/tkakkie/ribbitto/internal/kernel"
)

// Channel is a conversation identified by ID; Name is only its display name.
// DefaultTopicID is its default topic, created with it (decision 21).
type Channel struct {
	ID, OrganizationID, DefaultTopicID kernel.ID
	Name                               string
	IsDefault                          bool
	CreatedAt                          time.Time
}

// ValidateChannelName returns a trimmed NFC name of 1–80 printable Unicode code points.
func ValidateChannelName(value string) (string, error) {
	return validateText(value, 1, 80, "channel name")
}
