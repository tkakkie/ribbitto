package conversation

import "errors"

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
