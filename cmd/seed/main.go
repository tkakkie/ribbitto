// Command seed fills an empty, migrated development database with fictional conversations.
package main

import (
	"context"
	"crypto/rand"
	_ "embed"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/tkakkie/ribbitto/internal/app/authz"
	"github.com/tkakkie/ribbitto/internal/app/channel"
	"github.com/tkakkie/ribbitto/internal/app/message"
	"github.com/tkakkie/ribbitto/internal/app/setup"
	"github.com/tkakkie/ribbitto/internal/app/signup"
	"github.com/tkakkie/ribbitto/internal/app/topic"
	"github.com/tkakkie/ribbitto/internal/domain"
	"github.com/tkakkie/ribbitto/internal/identity"
	"github.com/tkakkie/ribbitto/internal/infra/postgres"
	platform "github.com/tkakkie/ribbitto/internal/platform/postgres"
)

//go:embed conversations.json
var conversations []byte

type scriptMessage struct{ Author, Body string }

type script struct {
	Members  []struct{ Name, Handle string }
	Channels []struct {
		Name     string
		Messages []scriptMessage
	}
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Getenv("RIBBITTO_DATABASE_URL"), os.Args[1:], os.Stdout); err != nil && !errors.Is(err, flag.ErrHelp) {
		slog.Error("seed failed", "err", err)
		os.Exit(1)
	}
}

// localHosts are the only database hosts the command writes to: it creates
// accounts whose credentials it prints, so it guards against being pointed
// at a remote database, even an empty one (the completed-setup check would
// not stop that). It checks the address only; a loopback port could still
// be a tunnel, so the documentation says to use a disposable database.
var localHosts = map[string]bool{"localhost": true, "127.0.0.1": true, "::1": true}

// requireLocal fails closed unless every host the URL may connect to,
// fallbacks included, is a loopback name. It runs before any connection.
func requireLocal(databaseURL string) error {
	if databaseURL == "" {
		return errors.New("RIBBITTO_DATABASE_URL is empty")
	}
	config, err := pgconn.ParseConfig(databaseURL)
	if err != nil {
		return fmt.Errorf("parsing RIBBITTO_DATABASE_URL: %w", err)
	}
	hosts := []string{config.Host}
	for _, fallback := range config.Fallbacks {
		hosts = append(hosts, fallback.Host)
	}
	for _, host := range hosts {
		if !localHosts[host] {
			return fmt.Errorf("seed writes only to a local development database (localhost, 127.0.0.1 or ::1), not %q", host)
		}
	}
	return nil
}

// newSecret returns a fresh random secret for this run: 24 random bytes as
// 32 URL-safe characters, well within the 15–128 character password rule. The
// run uses it for both the members' password and the setup token.
func newSecret() (string, error) {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generating secret: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func run(ctx context.Context, databaseURL string, args []string, out io.Writer) error {
	flags := flag.NewFlagSet("seed (development only)", flag.ContinueOnError)
	count := flags.Int("messages", 100, "messages per channel before development topic fixtures; repeats each fictional conversation in order")
	streams := flags.Int("streams", 0, "target concurrent streams; enables load-test sessions")
	perAccount := flags.Int("streams-per-account", 16, "load-test allocation cap; does not change production caps")
	sessionCount := flags.Int("sessions-per-account", 1, "sessions per seeded account in load-test mode")
	output := flags.String("output", "", "new JSON credential file outside any repository (required for load tests)")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("usage: go run ./cmd/seed [-messages N] [-streams N -output PATH] [-streams-per-account N] [-sessions-per-account N]; N must be positive; -streams and -output are required together")
	}
	var data script
	if err := json.Unmarshal(conversations, &data); err != nil {
		return fmt.Errorf("reading conversations: %w", err)
	}
	if err := requireLocal(databaseURL); err != nil {
		return err
	}
	password, err := newSecret()
	if err != nil {
		return err
	}
	token, err := newSecret() // setup needs a token; nothing outside this run uses it
	if err != nil {
		return err
	}
	pool, err := platform.OpenPool(ctx, databaseURL, nil)
	if err != nil {
		return fmt.Errorf("opening RIBBITTO_DATABASE_URL: %w", err)
	}
	defer pool.Close()
	hasher, err := identity.NewHasher()
	if err != nil {
		return fmt.Errorf("creating password hasher: %w", err)
	}
	const slug = "paper-lantern"
	store := postgres.NewSetupStore(pool)
	installer := setup.New(store, hasher, token)
	// Preflight is read-only; Complete still arbitrates concurrent setup attempts.
	open, err := installer.Open(ctx)
	if err != nil {
		return err
	}
	if !open {
		return setup.ErrCompleted
	}
	loadTest := false
	flags.Visit(func(f *flag.Flag) { loadTest = loadTest || f.Name != "messages" })
	accounts, err := validateRun(*count, len(data.Channels), len(data.Members), *streams, *perAccount, *sessionCount, loadTest, *output)
	if err != nil {
		return err
	}
	var credentials *os.File
	if loadTest {
		credentials, err = createCredentials(*output)
		if err != nil {
			return fmt.Errorf("creating credential file: %w", err)
		}
		defer func() { _ = credentials.Close() }()
	}
	for len(data.Members) < accounts {
		handle := fmt.Sprintf("loadtest-%d", len(data.Members)+1)
		data.Members = append(data.Members, struct{ Name, Handle string }{handle, handle})
	}
	manifest := credentialFile{OrganizationSlug: slug}
	sessions := identity.NewSessions(postgres.NewSessionStore(pool), time.Now)
	owner := data.Members[0]
	created, err := installer.Complete(ctx, token, setup.Input{
		OrganizationName: "Paper Lantern Studio", Slug: slug,
		Email: owner.Handle + "@example.test", DisplayName: owner.Name, Handle: owner.Handle, Password: password,
	})
	if err != nil {
		return fmt.Errorf("seeding requires an empty, migrated development database: %w", err)
	}
	signups := signup.New(store, hasher, true)
	authorizer := authz.New(postgres.NewAuthzStore(pool))
	members := make(map[string]authz.Membership, len(data.Members))
	for i, person := range data.Members {
		id := created.AccountID
		if i != 0 {
			id, err = signups.SignUp(ctx, person.Name, person.Handle, person.Handle+"@example.test", password)
			if err != nil {
				return fmt.Errorf("signing up %s: %w", person.Handle, err)
			}
		}
		if loadTest {
			entry := accountTokens{Handle: person.Handle}
			for range *sessionCount {
				token, _, err := sessions.Create(ctx, id)
				if err != nil {
					return fmt.Errorf("creating load-test session: %w", err)
				}
				entry.Tokens = append(entry.Tokens, token)
			}
			manifest.Accounts = append(manifest.Accounts, entry)
		}
		members[person.Handle], err = authorizer.Member(ctx, &identity.Account{ID: id}, slug)
		if err != nil {
			return fmt.Errorf("resolving %s: %w", person.Handle, err)
		}
	}
	channels := channel.New(postgres.NewChannelStore(pool))
	posts := message.New(postgres.NewPostingStore(pool))
	var general domain.Channel
	var generalMessages []scriptMessage
	for _, conversation := range data.Channels {
		var destination domain.Channel
		if conversation.Name == channel.DefaultName {
			destination, err = channels.Default(ctx, members[owner.Handle])
		} else {
			destination, err = channels.Create(ctx, members[owner.Handle], conversation.Name)
		}
		if err != nil {
			return fmt.Errorf("preparing channel %s: %w", conversation.Name, err)
		}
		if conversation.Name == channel.DefaultName {
			general, generalMessages = destination, conversation.Messages
		}
		id := destination.ID
		manifest.ChannelIDs = append(manifest.ChannelIDs, fmt.Sprintf("%x-%x-%x-%x-%x", id[:4], id[4:6], id[6:8], id[8:10], id[10:]))
		// Repeat whole exchanges, preserving references and author order without randomness.
		for i := 0; i < *count; i++ {
			line := conversation.Messages[i%len(conversation.Messages)]
			member, ok := members[line.Author]
			if !ok {
				return fmt.Errorf("unknown script author %q", line.Author)
			}
			if _, err := posts.Post(ctx, member, destination.ID, line.Body); err != nil {
				return fmt.Errorf("posting %s message %d: %w", conversation.Name, i+1, err)
			}
		}
	}
	if !loadTest {
		branches := topic.NewBrancher(postgres.NewBranchStore(pool), nil)
		if err := seedTopics(ctx, posts, branches, members, general, generalMessages); err != nil {
			return fmt.Errorf("seeding topics: %w", err)
		}
	}
	if loadTest {
		if err := json.NewEncoder(credentials).Encode(manifest); err != nil {
			return fmt.Errorf("writing credential file: %w", err)
		}
		if err := credentials.Close(); err != nil {
			return fmt.Errorf("closing credential file: %w", err)
		}
	}
	_, err = fmt.Fprintf(out, "Seeded %s. Sign in as %s@example.test (owner) or any other member at example.test with this run's password:\n%s\n", slug, owner.Handle, password)
	return err
}
