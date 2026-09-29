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

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/tkakkie/ribbitto/internal/app/auth"
	"github.com/tkakkie/ribbitto/internal/app/authz"
	"github.com/tkakkie/ribbitto/internal/app/channel"
	"github.com/tkakkie/ribbitto/internal/app/message"
	"github.com/tkakkie/ribbitto/internal/app/setup"
	"github.com/tkakkie/ribbitto/internal/app/signup"
	"github.com/tkakkie/ribbitto/internal/domain"
	"github.com/tkakkie/ribbitto/internal/infra/postgres"
)

//go:embed conversations.json
var conversations []byte

type script struct {
	Members  []struct{ Name, Handle string }
	Channels []struct {
		Name     string
		Messages []struct{ Author, Body string }
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

// newPassword returns a fresh random secret for this run: 24 random bytes as
// 32 URL-safe characters, well within the 15–128 character password rule.
func newPassword() (string, error) {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generating password: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func run(ctx context.Context, databaseURL string, args []string, out io.Writer) error {
	flags := flag.NewFlagSet("seed (development only)", flag.ContinueOnError)
	count := flags.Int("messages", 100, "messages per channel; repeats each fictional conversation in order")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 || *count < 1 {
		return fmt.Errorf("usage: go run ./cmd/seed [-messages N]; N must be positive")
	}
	var data script
	if err := json.Unmarshal(conversations, &data); err != nil {
		return fmt.Errorf("reading conversations: %w", err)
	}
	if err := requireLocal(databaseURL); err != nil {
		return err
	}
	password, err := newPassword()
	if err != nil {
		return err
	}
	token, err := newPassword() // setup needs a token; nothing outside this run uses it
	if err != nil {
		return err
	}
	pool, err := postgres.OpenPool(ctx, databaseURL)
	if err != nil {
		return fmt.Errorf("opening RIBBITTO_DATABASE_URL: %w", err)
	}
	defer pool.Close()
	hasher, err := auth.NewHasher()
	if err != nil {
		return fmt.Errorf("creating password hasher: %w", err)
	}
	const slug = "paper-lantern"
	store := postgres.NewSetupStore(pool)
	owner := data.Members[0]
	created, err := setup.New(store, hasher, token).Complete(ctx, token, setup.Input{
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
		members[person.Handle], err = authorizer.Member(ctx, &domain.Account{ID: id}, slug)
		if err != nil {
			return fmt.Errorf("resolving %s: %w", person.Handle, err)
		}
	}
	channels := channel.New(postgres.NewChannelStore(pool))
	posts := message.New(postgres.NewPostingStore(pool))
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
	_, err = fmt.Fprintf(out, "Seeded %s. Sign in as %s@example.test (owner) or any other member at example.test with this run's password:\n%s\n", slug, owner.Handle, password)
	return err
}
