package unread

import "github.com/tkakkie/ribbitto/internal/kernel"

// Scope identifies one member's read state in one channel.
type Scope struct {
	OrganizationID, ChannelID, MemberID kernel.ID
}

// TopicScope identifies one topic within a member's channel read state.
type TopicScope struct {
	Scope
	TopicID kernel.ID
}

// Range contains read sequences from Lo inclusive to Hi exclusive.
type Range struct{ Lo, Hi int64 }
