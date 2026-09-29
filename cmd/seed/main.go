// Command seed fills an empty, migrated development database with fictional conversations.
package main

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

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
	if err := run(ctx, os.Getenv("RIBBITTO_DATABASE_URL"), os.Args[1:]); err != nil && !errors.Is(err, flag.ErrHelp) {
		slog.Error("seed failed", "err", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, databaseURL string, args []string) error {
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
	pool, err := postgres.OpenPool(ctx, databaseURL)
	if err != nil {
		return fmt.Errorf("opening RIBBITTO_DATABASE_URL: %w", err)
	}
	defer pool.Close()
	hasher, err := auth.NewHasher()
	if err != nil {
		return fmt.Errorf("creating password hasher: %w", err)
	}
	const token = "development-only-seed-setup-token"
	const password = "development-only-password"
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
	return nil
}
