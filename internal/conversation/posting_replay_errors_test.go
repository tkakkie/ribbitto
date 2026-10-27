package conversation_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/tkakkie/ribbitto/internal/conversation"
	"github.com/tkakkie/ribbitto/internal/kernel"
	"github.com/tkakkie/ribbitto/internal/org"
	platform "github.com/tkakkie/ribbitto/internal/platform/postgres"
)

type replayDefaultTopic struct {
	*keyedStore
	err error
}

func (s *replayDefaultTopic) GetDefaultTopic(ctx context.Context, organizationID, channelID kernel.ID) (conversation.Topic, error) {
	if s.err != nil {
		return conversation.Topic{}, s.err
	}
	return s.keyedStore.GetDefaultTopic(ctx, organizationID, channelID)
}

type flakyQuoteReads struct {
	quoteRetryReads
	err error
}

func (r *flakyQuoteReads) GetMessages(ctx context.Context, organization, channel kernel.ID, ids []kernel.ID) ([]conversation.Message, error) {
	if r.err != nil {
		return nil, r.err
	}
	return r.quoteRetryReads.GetMessages(ctx, organization, channel, ids)
}

func TestPostingReplayDefaultTopicErrors(t *testing.T) {
	failure := errors.New("topic read failed")
	orgID, memberID, channelID := kernel.ID{1}, kernel.ID{2}, kernel.ID{3}
	m := org.Membership{Organization: org.Organization{ID: orgID}, Member: org.Member{ID: memberID}}

	for _, tt := range []struct {
		name               string
		err                error
		wantErr, forbidErr error
	}{
		{
			name:      "missing default topic becomes ErrChannelNotFound",
			err:       conversation.ErrTopicNotFound,
			wantErr:   conversation.ErrChannelNotFound,
			forbidErr: conversation.ErrTopicNotFound,
		},
		{
			name:      "default topic read failure is preserved",
			err:       failure,
			wantErr:   failure,
			forbidErr: conversation.ErrChannelNotFound,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			s := &replayDefaultTopic{
				keyedStore: &keyedStore{state: keyedState{seq: map[kernel.ID]int64{}, keyed: map[keyScope]conversation.Message{}}},
			}
			d := &referenceDirectory{t: t, members: map[kernel.ID][]kernel.ID{}}
			p := conversation.NewPagePosting(s,
				func(platform.Tx) conversation.Writer { return s },
				func(platform.Tx) conversation.EventSequence { return s },
				func(platform.Tx) conversation.EventAppender { return s },
				nil, d, func(platform.Snapshot) conversation.MemberDirectory { return d }, nil, nil)
			key := conversation.NewIdempotencyKey()
			send := conversation.Send{Body: "hi @unknown", Key: &key}
			if first, err := p.Send(t.Context(), m, channelID, send); err != nil {
				t.Fatalf("%s: first send = %+v, %v; want nil error", tt.name, first, err)
			}
			beforeSeq, beforeEvents := s.state.seq[orgID], s.state.events
			s.err = tt.err
			got, err := p.Send(t.Context(), m, channelID, send)
			if got != (conversation.Sent{}) {
				t.Fatalf("%s: sent = %+v, want %+v", tt.name, got, conversation.Sent{})
			}
			const wantPrefix = "finding repeated message: "
			if err == nil || !strings.HasPrefix(err.Error(), wantPrefix) {
				t.Fatalf("%s: error = %v, want prefix %q", tt.name, err, wantPrefix)
			}
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("%s: errors.Is(%v, %v) = false, want true", tt.name, err, tt.wantErr)
			}
			if errors.Is(err, tt.forbidErr) {
				t.Fatalf("%s: errors.Is(%v, %v) = true, want false", tt.name, err, tt.forbidErr)
			}
			if s.state.seq[orgID] != beforeSeq || s.state.events != beforeEvents || len(s.state.keyed) != 1 || len(s.lookups) != 2 {
				t.Fatalf("%s: state seq=%d events=%d keyed=%d lookups=%d, want seq=%d events=%d keyed=1 lookups=2",
					tt.name, s.state.seq[orgID], s.state.events, len(s.state.keyed), len(s.lookups), beforeSeq, beforeEvents)
			}
		})
	}
}

func TestPostingReplayQuoteReadFails(t *testing.T) {
	failure := errors.New("quote read failed")
	orgID, memberID, channelID := kernel.ID{1}, kernel.ID{2}, kernel.ID{3}
	m := org.Membership{Organization: org.Organization{ID: orgID}, Member: org.Member{ID: memberID}}

	for _, tt := range []struct {
		name        string
		body        string
		wantLookups int
	}{
		// A handle token routes the retry through repeat before quoteReferences.
		{name: "reply @unknown", body: "reply @unknown", wantLookups: 2},
		// Control: without a handle token, Send's own quoteReferences call fails first.
		{name: "reply", body: "reply", wantLookups: 0},
	} {
		t.Run(tt.name, func(t *testing.T) {
			// Format is explicit so a disabled quoteReferences error check in
			// repeat still extracts valid plain text and would return the stored message.
			original := conversation.Message{
				ID:             kernel.ID{4},
				OrganizationID: orgID,
				ChannelID:      channelID,
				MemberID:       memberID,
				Format:         conversation.FormatPlainText,
				Body:           "whole text",
			}
			s := &quoteRetryStore{
				keyedStore: &keyedStore{state: keyedState{seq: map[kernel.ID]int64{}, keyed: map[keyScope]conversation.Message{}}},
				original:   original,
			}
			reads := &flakyQuoteReads{quoteRetryReads: quoteRetryReads{store: s}}
			d := &referenceDirectory{t: t, members: map[kernel.ID][]kernel.ID{}}
			p := conversation.NewPagePosting(s,
				func(platform.Tx) conversation.Writer { return s },
				func(platform.Tx) conversation.EventSequence { return s },
				func(platform.Tx) conversation.EventAppender { return s },
				nil, d, func(platform.Snapshot) conversation.MemberDirectory { return d },
				func(platform.Snapshot) conversation.ReadStore { return reads }, nil)
			key := conversation.NewIdempotencyKey()
			send := conversation.Send{Body: tt.body, Key: &key, Quote: &original.ID}
			if first, err := p.Send(t.Context(), m, channelID, send); err != nil {
				t.Fatalf("%s: first send = %+v, %v; want nil error", tt.name, first, err)
			}
			reads.err = failure
			got, err := p.Send(t.Context(), m, channelID, send)
			if got != (conversation.Sent{}) {
				t.Fatalf("%s: sent = %+v, want %+v", tt.name, got, conversation.Sent{})
			}
			const wantSubstr = "reading quoted message"
			if err == nil || !strings.Contains(err.Error(), wantSubstr) {
				t.Fatalf("%s: error = %v, want substring %q", tt.name, err, wantSubstr)
			}
			if !errors.Is(err, failure) {
				t.Fatalf("%s: errors.Is(%v, %v) = false, want true", tt.name, err, failure)
			}
			if len(s.state.keyed) != 1 || s.state.events != 1 || len(s.lookups) != tt.wantLookups {
				t.Fatalf("%s: state keyed=%d events=%d lookups=%d, want keyed=1 events=1 lookups=%d",
					tt.name, len(s.state.keyed), s.state.events, len(s.lookups), tt.wantLookups)
			}
		})
	}
}
