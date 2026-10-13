package main

import (
	"context"
	"fmt"
	"math/rand/v2"

	"github.com/tkakkie/ribbitto/internal/conversation"
	"github.com/tkakkie/ribbitto/internal/kernel"
	"github.com/tkakkie/ribbitto/internal/org"
)

// Mirror fixedStage2().Fixture: command packages cannot import each other.
// IDs and credentials remain random; only semantic fixture content uses the seed.
type stage2Credentials struct {
	Version         int           `json:"version"`
	FixtureSeed     uint64        `json:"fixture_seed"`
	HotChannelID    string        `json:"hot_channel_id"`
	DefaultTopicIDs []string      `json:"default_topic_ids"`
	NamedTopics     []stage2Topic `json:"named_topics"`
}

type stage2Topic struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

func seedID(id kernel.ID) string {
	return fmt.Sprintf("%x-%x-%x-%x-%x", id[:4], id[4:6], id[6:8], id[8:10], id[10:])
}

func stage2Script(data *script, members int, seed uint64) {
	data.Members = nil
	for i := range members {
		handle := fmt.Sprintf("stage2-%d", i+1)
		data.Members = append(data.Members, struct{ Name, Handle string }{handle, handle})
	}
	rng := rand.New(rand.NewPCG(seed, 1))
	for c := range data.Channels {
		data.Channels[c].Messages = nil
		for i := range 500 {
			data.Channels[c].Messages = append(data.Channels[c].Messages, scriptMessage{
				Author: data.Members[rng.IntN(members)].Handle,
				Body:   fmt.Sprintf("Stage 2 fixture %d channel %d message %d sample %d", seed, c, i, rng.Uint64()),
			})
		}
	}
}

func seedStage2Hot(ctx context.Context, posts *conversation.Posting, branches *conversation.Brancher, members map[string]org.Membership, channel conversation.Channel, lines []scriptMessage, metadata *stage2Credentials) error {
	var topics [2]kernel.ID
	// Branching leaves two notices: 498 script posts + 2 notices = 500 messages.
	for i, line := range lines[:498] {
		topic := channel.DefaultTopicID
		if i >= 250 {
			topic = topics[(i-250)/124]
		}
		member := members[line.Author]
		message, err := posts.PostToTopic(ctx, member, channel.ID, &topic, line.Body)
		if err != nil {
			return fmt.Errorf("posting stage-2 hot message %d: %w", i, err)
		}
		if i < 2 {
			name := fmt.Sprintf("stage2-topic-%d", i+1)
			destination, err := branches.Branch(ctx, member, channel.ID, conversation.Branch{
				Messages: []kernel.ID{message.ID}, From: topic, NewName: name,
			}, func(t conversation.Topic) string { return fmt.Sprintf("Moved 1 message to %s.", t.Name) })
			if err != nil {
				return fmt.Errorf("branching stage-2 topic: %w", err)
			}
			topics[i] = destination.ID
			metadata.NamedTopics = append(metadata.NamedTopics, stage2Topic{seedID(destination.ID), name})
		}
	}
	return nil
}
