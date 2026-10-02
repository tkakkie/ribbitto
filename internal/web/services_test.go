package web

import (
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/tkakkie/ribbitto/internal/app/auth"
	"github.com/tkakkie/ribbitto/internal/app/authz"
	"github.com/tkakkie/ribbitto/internal/app/channel"
	"github.com/tkakkie/ribbitto/internal/app/message"
	"github.com/tkakkie/ribbitto/internal/app/setup"
	"github.com/tkakkie/ribbitto/internal/app/signup"
	"github.com/tkakkie/ribbitto/internal/infra/postgres"
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
func postgresServices(t *testing.T, pool *pgxpool.Pool, sessions *auth.Sessions, setupToken string, signUp bool) Services {
	t.Helper()
	hasher, err := auth.NewHasher()
	if err != nil {
		t.Fatal(err)
	}
	s := Services{
		Sessions: sessions,
		SignIn:   auth.NewSignIn(postgres.NewAccountStore(pool), hasher, sessions),
		Authz:    authz.New(postgres.NewAuthzStore(pool)),
		Channels: channel.New(postgres.NewChannelStore(pool)),
		Topics:   postgres.NewTopicStore(pool),
		Messages: postgres.MessageReader{Pool: pool},
		Posting:  message.New(postgres.NewPostingStore(pool)),
	}
	if setupToken != "" {
		s.Setup, s.SetupSessions = setup.New(postgres.NewSetupStore(pool), hasher, setupToken), sessions
	}
	if signUp {
		s.SignUp, s.SetupSessions = signup.New(postgres.NewSetupStore(pool), hasher, true), sessions
	}
	return s
}
