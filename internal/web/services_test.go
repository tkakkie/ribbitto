package web

import (
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/tkakkie/ribbitto/internal/compose"
	"github.com/tkakkie/ribbitto/internal/conversation"
	"github.com/tkakkie/ribbitto/internal/conversation/conversationpg"
	"github.com/tkakkie/ribbitto/internal/identity"
	"github.com/tkakkie/ribbitto/internal/identity/identitypg"
	"github.com/tkakkie/ribbitto/internal/kernel"
	"github.com/tkakkie/ribbitto/internal/org/orgpg"
	"github.com/tkakkie/ribbitto/internal/unread"
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
	s.Preferences = identity.NewPreferences(&preferenceMemory{})
	s.MemberSearch = &fakeMemberSearch{}
	s.SidebarResync = &unread.Changes{}
	s.MemberLimits = NewMemberLimits(time.Now)
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

// postgresServices uses production database adapters, including post reads,
// page counts, Preferences and MemberLimits. Caller-supplied sessions allow
// clock control; uncached Authz makes fixture writes visible immediately.
// Setup, SignUp and SetupSessions are opt-in. Limits stays nil so suites choose
// authentication throttling; other limiters and Channels use production defaults.
// Database, ShuttingDown, Stream, Typing, SidebarResync, Reading
// and write notifications stay nil: suites opt into readiness, shutdown, live
// sources and read routes separately. RequestLogger uses the process default.
// No database service is faked.
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
		Channels:  conversationpg.NewChannels(pool, conversation.DefaultMaxChannels),
		Topics:    conversationpg.NewTopics(pool),
		Messages:  conversationpg.NewReader(pool, compose.LookupMembers, compose.LookupAccounts, compose.EventCursor, compose.PageCountsIn),
		Posting:   conversationpg.NewPagePosting(pool, compose.PostingSequence, compose.PostingEvents, compose.PostReads, compose.LookupMembers, nil),
		Branching: conversationpg.NewBrancher(pool, compose.PostingSequence, compose.PostingEvents, compose.BranchReads, nil),
		Pinning:   conversationpg.NewPinning(pool, compose.PostingSequence, compose.PostingEvents, nil),
	}
	s.Preferences = identitypg.NewPreferences(pool)
	s.MemberLimits = NewMemberLimits(time.Now)
	s.MemberSearch = orgpg.NewMemberSearch(pool, identitypg.AccountsIn)
	if setupToken != "" {
		s.Setup, s.SetupSessions = orgpg.NewSetup(pool, hasher, setupToken,
			compose.AccountCreator,
			compose.MemberEvents,
			compose.DefaultChannelCreator), sessions
	}
	if signUp {
		s.SignUp, s.SetupSessions = orgpg.NewSignUp(pool, hasher, true,
			compose.AccountCreator,
			compose.MemberEvents), sessions
	}
	return s
}

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

// memberLimitOverride confines pointer injection to same-package tests.
func memberLimitOverride(limits MemberLimits, set func(*MemberLimits)) MemberLimits {
	return NewMemberLimits(time.Now, MemberLimitsOption{func(l *MemberLimits) {
		*l = limits
		set(l)
	}})
}
