package conversation

import "errors"

// ErrTopicNotFound means the topic does not exist in the given organisation and
// channel, including when it exists in another channel or organisation.
var ErrTopicNotFound = errors.New("topic not found")

// ErrInvalidTopicName wraps a name that breaks ValidateTopicName.
var ErrInvalidTopicName = errors.New("invalid topic name")

// ErrTopicNameTaken means the channel already has a topic with that name,
// ignoring case.
var ErrTopicNameTaken = errors.New("topic name already taken")
