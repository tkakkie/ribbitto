package main

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/tkakkie/ribbitto/internal/conversation"
	"github.com/tkakkie/ribbitto/internal/conversation/conversationpg"
	"github.com/tkakkie/ribbitto/internal/conversation/conversationtest"
	"github.com/tkakkie/ribbitto/internal/identity"
	"github.com/tkakkie/ribbitto/internal/identity/identitytest"
	"github.com/tkakkie/ribbitto/internal/kernel"
	"github.com/tkakkie/ribbitto/internal/org"
	"github.com/tkakkie/ribbitto/internal/org/orgpg"
	"github.com/tkakkie/ribbitto/internal/org/orgtest"
	"github.com/tkakkie/ribbitto/internal/platform/postgres/pgtest"
	"github.com/tkakkie/ribbitto/internal/web/view"
)

func TestLivePins(t *testing.T) {
	t.Parallel()
	pool := pgtest.New(t)
	f := conversationtest.OrganizationWithOwner(t, pool, "pins", "general")
	selected := conversationtest.Topic(t, pool, f.OrganizationID, f.Channel.ID, "selected")
	account := identitytest.Account(t, pool, "reader@example.org", "reader")
	orgtest.Member(t, pool, f.OrganizationID, account, org.RoleMember, "reader", 1)
	api, streams, hub := streamServersWithHub(t, pool)
	owner, reader := sidebarBrowser(t, pool, api, f.AccountID), sidebarBrowser(t, pool, api, account)
	m, err := orgpg.NewAuthorizer(pool).Member(t.Context(), &identity.Account{ID: f.AccountID}, "pins")
	acceptanceOK(t, err)
	posting := conversationpg.NewPagePosting(pool, postingSequence, postingEvents, postReads, hub)
	message, err := posting.Post(t.Context(), m, f.Channel.ID, "pin across topics")
	acceptanceOK(t, err)
	path := view.ConversationURL("pins", f.Channel.ID, &selected.ID)
	endpoint := strings.Replace(streamEndpoint(t, path), "want=messages", "want=pins,messages", 1)
	frame := func(events <-chan sseEvent, contains string) {
		t.Helper()
		e := nextEvent(t, events)
		if e.name != "pins" || e.id != "" || !strings.Contains(e.data, contains) {
			t.Fatalf("pins frame: %+v", e)
		}
	}
	var tabs []<-chan sseEvent
	for _, b := range []acceptanceBrowser{owner, reader} {
		events, status, _ := openEndpointIn(t.Context(), t, on(b, streams), endpoint, "1", "")
		if status != 200 {
			t.Fatalf("status %d", status)
		}
		frame(events, "No pinned messages yet.")
		tabs = append(tabs, events)
	}
	request := owner.request(t, "POST", view.PinURL("pins", f.Channel.ID, message.ID)+"/pin", nil)
	request.Header.Set("HX-Request", "true")
	response, _, err := owner.send(request)
	acceptanceOK(t, err)
	if response.StatusCode != 200 {
		t.Fatalf("pin status %d", response.StatusCode)
	}
	for _, events := range tabs {
		frame(events, "pin across topics")
	}
	_, err = conversationpg.NewBrancher(pool, postingSequence, postingEvents, branchReads, hub).Branch(t.Context(), m, f.Channel.ID, conversation.Branch{Messages: []kernel.ID{message.ID}, From: f.Channel.DefaultTopicID, NewName: "current label"}, func(conversation.Topic) string { return "branch notice" })
	acceptanceOK(t, err)
	for _, events := range tabs {
		frame(events, "current label")
	}
	// A commit without a raise is recovered by the existing watermark path.
	pinning := conversationpg.NewPinning(pool, postingSequence, postingEvents, nil)
	acceptanceOK(t, pinning.Unpin(t.Context(), m, f.Channel.ID, message.ID))
	hub.Raise(f.OrganizationID, 6)
	for _, events := range tabs {
		frame(events, "No pinned messages yet.")
	}
	// Connect after the last event: a fresh empty snapshot still arrives.
	ctx, cancel := context.WithCancel(t.Context())
	events, _, _ := openEndpointIn(ctx, t, on(reader, streams), endpoint, "6", "")
	frame(events, "No pinned messages yet.")
	cancel()
	streamEndsWithin(t, events, time.Second)
	_, err = posting.PostToTopic(t.Context(), m, f.Channel.ID, &selected.ID, "later message")
	acceptanceOK(t, err)
	for _, events := range tabs {
		e := nextEvent(t, events)
		if e.name != "message" || e.id != "7" {
			t.Fatalf("later message: %+v", e)
		}
	}
}
