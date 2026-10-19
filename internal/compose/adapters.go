package compose

import (
	"github.com/tkakkie/ribbitto/internal/conversation"
	"github.com/tkakkie/ribbitto/internal/conversation/conversationpg"
	"github.com/tkakkie/ribbitto/internal/identity/identitypg"
	"github.com/tkakkie/ribbitto/internal/org"
	"github.com/tkakkie/ribbitto/internal/org/orgpg"
	platform "github.com/tkakkie/ribbitto/internal/platform/postgres"
	"github.com/tkakkie/ribbitto/internal/realtime/realtimepg"
)

// AccountCreator binds identity's account creator to setup's and sign-up's transaction.
func AccountCreator(tx platform.Tx) org.AccountCreator { return identitypg.AccountCreatorIn(tx) }

// MemberEvents binds realtime's appender to setup's and sign-up's transaction.
func MemberEvents(tx platform.Tx) org.EventAppender { return realtimepg.AppenderIn(tx) }

// DefaultChannelCreator binds conversation's default-channel creator to setup's transaction.
func DefaultChannelCreator(tx platform.Tx) org.DefaultChannelCreator {
	return conversationpg.DefaultChannelCreatorIn(tx)
}

// PostingEvents binds realtime's appender to conversation's posting and branching transaction.
func PostingEvents(tx platform.Tx) conversation.EventAppender { return realtimepg.AppenderIn(tx) }

// PostingSequence binds org's sequence to conversation's posting and branching transaction.
func PostingSequence(tx platform.Tx) conversation.EventSequence { return orgpg.SequenceIn(tx) }

// LookupMembers adapts org's directory to the reader's consumer interface.
func LookupMembers(s platform.Snapshot) conversation.MemberDirectory { return orgpg.MembersIn(s) }

// LookupAccounts adapts identity's directory to the reader's consumer interface.
func LookupAccounts(s platform.Snapshot) conversation.AccountDirectory {
	return identitypg.AccountsIn(s)
}

// EventCursor adapts org's committed event_seq to the reader's consumer
// interface.
func EventCursor(s platform.Snapshot) conversation.EventCursor { return orgpg.EventCursorIn(s) }
