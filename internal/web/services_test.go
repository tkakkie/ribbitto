package web

import (
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/tkakkie/ribbitto/internal/app/channel"
	"github.com/tkakkie/ribbitto/internal/app/message"
	"github.com/tkakkie/ribbitto/internal/app/signup"
	"github.com/tkakkie/ribbitto/internal/app/topic"
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
	s.Branching = topic.NewBrancher(&fakeBranchStore{}, nil)
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
		Channels:  channel.New(postgres.NewChannelStore(pool)),
		Topics:    postgres.NewTopicStore(pool),
		Messages:  postgres.MessageReader{Pool: pool, Accounts: identitypg.AccountsIn, Members: lookupMembers},
		Posting:   message.New(postgres.NewPostingStore(pool, appendEvents)),
		Branching: topic.NewBrancher(postgres.NewBranchStore(pool, appendEvents), nil),
	}
	if setupToken != "" {
		s.Setup, s.SetupSessions = org.NewSetup(postgres.NewSetupStore(pool, appendEvents), hasher, setupToken), sessions
	}
	if signUp {
		s.SignUp, s.SetupSessions = signup.New(postgres.NewSetupStore(pool, appendEvents), hasher, true), sessions
	}
	return s
}

// appendEvents adapts realtime's appender to the consumer interface the
// event-writing stores declare (decision 26).
func appendEvents(tx platform.Tx) postgres.EventAppender { return realtimepg.AppenderIn(tx) }

// lookupMembers adapts org's directory to the reader's consumer interface.
func lookupMembers(s platform.Snapshot) postgres.MemberDirectory { return orgpg.MembersIn(s) }
