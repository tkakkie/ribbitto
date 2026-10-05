package web

import (
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/tkakkie/ribbitto/internal/conversation"
	"github.com/tkakkie/ribbitto/internal/conversation/conversationpg"
	"github.com/tkakkie/ribbitto/internal/identity"
	"github.com/tkakkie/ribbitto/internal/identity/identitypg"
	"github.com/tkakkie/ribbitto/internal/infra/postgres"
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
			func(tx platform.Tx) org.AccountCreator { return identitypg.AccountCreatorIn(tx) },
			func(tx platform.Tx) org.EventAppender { return realtimepg.AppenderIn(tx) },
			func(tx platform.Tx) org.DefaultChannelCreator { return conversationpg.DefaultChannelCreatorIn(tx) }), sessions
	}
	if signUp {
		s.SignUp, s.SetupSessions = orgpg.NewSignUp(pool, hasher, true,
			func(tx platform.Tx) org.AccountCreator { return identitypg.AccountCreatorIn(tx) },
			func(tx platform.Tx) org.EventAppender { return realtimepg.AppenderIn(tx) }), sessions
	}
	return s
}

// postingEvents binds realtime's appender to conversation's posting and branching transaction.
func postingEvents(tx platform.Tx) conversation.EventAppender { return realtimepg.AppenderIn(tx) }

// postingSequence binds org's sequence to conversation's posting and branching transaction.
func postingSequence(tx platform.Tx) conversation.EventSequence { return orgpg.SequenceIn(tx) }

// appendEvents adapts realtime's appender to the consumer interface the
// event-writing stores declare (decision 26).
func appendEvents(tx platform.Tx) postgres.EventAppender { return realtimepg.AppenderIn(tx) }

// lookupMembers adapts org's directory to the reader's consumer interface.
func lookupMembers(s platform.Snapshot) conversation.MemberDirectory { return orgpg.MembersIn(s) }

// lookupAccounts adapts identity's directory to the reader's consumer interface.
func lookupAccounts(s platform.Snapshot) conversation.AccountDirectory {
	return identitypg.AccountsIn(s)
}

// eventSequence adapts org's sequence to the posting and branching stores'
// consumer interface.
func eventSequence(tx platform.Tx) postgres.EventSequence { return orgpg.SequenceIn(tx) }

// eventCursor adapts org's committed event_seq to the reader's consumer
// interface.
func eventCursor(s platform.Snapshot) conversation.EventCursor { return orgpg.EventCursorIn(s) }
