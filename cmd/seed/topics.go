package main

import (
	"context"
	"fmt"

	"github.com/tkakkie/ribbitto/internal/app/message"
	"github.com/tkakkie/ribbitto/internal/app/topic"
	"github.com/tkakkie/ribbitto/internal/conversation"
	"github.com/tkakkie/ribbitto/internal/domain"
	"github.com/tkakkie/ribbitto/internal/org"
)

// Two source posts and their notices, plus a full page and ten direct posts.
const topicFixtureMessages = 4 + message.PageSize + 10

func seedTopics(ctx context.Context, posts *message.Service, branches *topic.Brancher, members map[string]org.Membership, general conversation.Channel, lines []scriptMessage) error {
	for i, name := range []string{"rooftop-garden", "garden-time"} {
		// Extra source posts keep even -messages 1 intact in the default topic.
		line := lines[i*4]
		member := members[line.Author]
		source, err := posts.PostToTopic(ctx, member, general.ID, &general.DefaultTopicID, line.Body)
		if err != nil {
			return fmt.Errorf("posting branch source: %w", err)
		}
		destination, err := branches.Branch(ctx, member, general.ID, topic.Branch{
			Messages: []domain.ID{source.ID}, From: general.DefaultTopicID, NewName: name,
		}, func(t conversation.Topic) string { return fmt.Sprintf("Moved 1 message to %s.", t.Name) })
		if err != nil {
			return fmt.Errorf("branching %s: %w", name, err)
		}
		if i == 0 {
			for j := range message.PageSize + 10 {
				line := lines[j%len(lines)]
				if _, err := posts.PostToTopic(ctx, members[line.Author], general.ID, &destination.ID, line.Body); err != nil {
					return fmt.Errorf("posting %s message %d: %w", name, j+1, err)
				}
			}
		}
	}
	return nil
}
