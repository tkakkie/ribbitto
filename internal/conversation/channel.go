package conversation

import (
	"errors"
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

// DefaultChannelName is the initial name of an organisation's default channel. It
// is only a name: the default is found by its is_default flag.
const DefaultChannelName = "general"

// ErrChannelNotFound means the channel does not exist in the caller's organisation,
// including when it exists in another one.
var ErrChannelNotFound = errors.New("channel not found")

// ErrInvalidChannelName wraps a name that breaks ValidateChannelName.
var ErrInvalidChannelName = errors.New("invalid channel name")

// ErrChannelNameTaken means the organisation already has a channel with that name.
// Names are unique only because channels sit flat under the organisation;
// the id, not the name, identifies a channel.
var ErrChannelNameTaken = errors.New("channel name already taken")
