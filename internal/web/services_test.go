package web

import (
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/tkakkie/ribbitto/internal/conversation"
	"github.com/tkakkie/ribbitto/internal/conversation/conversationpg"
	"github.com/tkakkie/ribbitto/internal/identity"
	"github.com/tkakkie/ribbitto/internal/identity/identitypg"
	"github.com/tkakkie/ribbitto/internal/kernel"
	"github.com/tkakkie/ribbitto/internal/org"
	"github.com/tkakkie/ribbitto/internal/org/orgpg"
	platform "github.com/tkakkie/ribbitto/internal/platform/postgres"
	"github.com/tkakkie/ribbitto/internal/realtime/realtimepg"
)

// testServices returns the services NewHandler requires, as DB-free fakes:
// nobody is signed in and nobody belongs to an organisation. Each override
// then changes only what its test is about. Every call builds fresh values,
// so a test that mutates a fake never affects another, and a service that
// becomes required is added here once.
func testServices(overrides ...func(*Services)) Services {
	s := Services{
		Sessions: noSessions{},
		SignIn:   &fakeSignIn{},
		Authz:    noOrganisations{},
		Channels: &fakeChannels{},
		Topics:   fakeTopics{},
		Messages: fakeMessages{},
		Posting:  testPoster(),
	}
	s.Branching = testBrancher(&fakeBranchWriter{})
	for _, override := range overrides {
		override(&s)
	}
	return s
}

// asAlice makes the session cookie "live" sign in Alice, a member of the
// organisation acme.
func asAlice(s *Services) {
	s.Sessions, s.Authz = oneSession{}, oneOrganisation{}
}

// postgresServices wires the PostgreSQL-backed services the way
// cmd/ribbitto's buildHandler does, around the caller's sessions so a suite
// can control their clock. A non-empty setupToken enables setup; signUp adds
// sign-up with the operator's switch on. Rate limits stay off: a suite that
// tests them sets Limits.
func postgresServices(t *testing.T, pool *pgxpool.Pool, sessions *identity.Sessions, setupToken string, signUp bool) Services {
	t.Helper()
	hasher, err := identity.NewHasher()
	if err != nil {
		t.Fatal(err)
	}
	s := Services{
		Sessions:  sessions,
		SignIn:    identitypg.NewSignIn(pool, hasher, sessions),
		Authz:     orgpg.NewAuthorizer(pool),
		Channels:  conversationpg.NewChannels(pool),
		Topics:    conversationpg.NewTopics(pool),
		Messages:  conversationpg.NewReader(pool, lookupMembers, lookupAccounts, eventCursor),
		Posting:   conversationpg.NewPosting(pool, postingSequence, postingEvents, nil),
		Branching: conversationpg.NewBrancher(pool, postingSequence, postingEvents, nil),
	}
	if setupToken != "" {
		s.Setup, s.SetupSessions = orgpg.NewSetup(pool, hasher, setupToken,
			accountCreator,
			memberEvents,
			defaultChannelCreator), sessions
	}
	if signUp {
		s.SignUp, s.SetupSessions = orgpg.NewSignUp(pool, hasher, true,
			accountCreator,
			memberEvents), sessions
	}
	return s
}

// accountCreator binds identity's account creator to setup's and sign-up's transaction.
func accountCreator(tx platform.Tx) org.AccountCreator { return identitypg.AccountCreatorIn(tx) }

// memberEvents binds realtime's appender to setup's and sign-up's transaction.
func memberEvents(tx platform.Tx) org.EventAppender { return realtimepg.AppenderIn(tx) }

// defaultChannelCreator binds conversation's default-channel creator to setup's transaction.
func defaultChannelCreator(tx platform.Tx) org.DefaultChannelCreator {
	return conversationpg.DefaultChannelCreatorIn(tx)
}

// postingEvents binds realtime's appender to conversation's posting and branching transaction.
func postingEvents(tx platform.Tx) conversation.EventAppender { return realtimepg.AppenderIn(tx) }

// postingSequence binds org's sequence to conversation's posting and branching transaction.
func postingSequence(tx platform.Tx) conversation.EventSequence { return orgpg.SequenceIn(tx) }

// lookupMembers adapts org's directory to the reader's consumer interface.
func lookupMembers(s platform.Snapshot) conversation.MemberDirectory { return orgpg.MembersIn(s) }

// lookupAccounts adapts identity's directory to the reader's consumer interface.
func lookupAccounts(s platform.Snapshot) conversation.AccountDirectory {
	return identitypg.AccountsIn(s)
}

// eventCursor adapts org's committed event_seq to the reader's consumer
// interface.
func eventCursor(s platform.Snapshot) conversation.EventCursor { return orgpg.EventCursorIn(s) }

// recordingNotifier captures the notice sequence that Branch publishes after
// commit, keeping replay's stopping point out of the production return value.
type recordingNotifier struct{ seq int64 }

func (n *recordingNotifier) Raise(_ kernel.ID, seq int64) { n.seq = seq }

// raised returns the recorded sequence, failing at once if Branch raised
// nothing: a zero stopping point would make the replay wait until the test
// times out instead of failing.
func (n *recordingNotifier) raised(t *testing.T) int64 {
	t.Helper()
	if n.seq == 0 {
		t.Fatal("branch raised no notice sequence")
	}
	return n.seq
}
