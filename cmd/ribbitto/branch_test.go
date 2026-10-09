package main

import (
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/tkakkie/ribbitto/internal/conversation/conversationtest"
	"github.com/tkakkie/ribbitto/internal/kernel"
)

// Branching through the production wiring (#305): moved messages show their
// new topic in the feed after a reload, the destination's topic view shows
// them, the source keeps the rest and the notice, and a stale selection is
// 409 without changing anything.
func TestBranching(t *testing.T) {
	pool := acceptanceDatabase(t)
	server, _ := streamServers(t, pool)
	owner := newAcceptanceBrowser(t, server, "192.0.2.10")
	owner.visit(t, "POST", "/setup", acceptanceForm("owner"), 303)
	response, _ := owner.visit(t, "GET", "/organizations/owner/", nil, 303)
	channelURL := response.Header.Get("Location")
	for _, body := range []string{"stays here", "moves away", "moves too"} {
		post(t, owner, channelURL, body)
	}
	ids := map[string]string{}
	rows, err := pool.Query(t.Context(), "SELECT body, id::text FROM message")
	acceptanceOK(t, err)
	for rows.Next() {
		var body, id string
		acceptanceOK(t, rows.Scan(&body, &id))
		ids[body] = id
	}
	acceptanceOK(t, rows.Err())
	var source string
	acceptanceOK(t, pool.QueryRow(t.Context(), "SELECT default_topic_id::text FROM channel WHERE is_default").Scan(&source))
	form := url.Values{"message": {ids["moves away"], ids["moves too"]}, "from": {source}, "name": {"design"}}

	topicURL := branch(t, owner, channelURL, form, http.StatusSeeOther)
	if !strings.HasPrefix(topicURL, channelURL+"/topics/") {
		t.Fatalf("branch redirected to %q, want the new topic's view", topicURL)
	}
	_, topicPage := owner.visit(t, "GET", topicURL, nil, 200)
	if !strings.Contains(topicPage, "moves away") || !strings.Contains(topicPage, "moves too") || strings.Contains(topicPage, "stays here") {
		t.Fatal("destination topic view does not show exactly the moved messages")
	}
	_, sourcePage := owner.visit(t, "GET", channelURL+"/topics/"+source, nil, 200)
	if !strings.Contains(sourcePage, "stays here") || strings.Contains(sourcePage, "moves away") || !strings.Contains(sourcePage, "Moved 2 to “design”") {
		t.Fatal("source topic view lacks the remaining message or the notice")
	}
	_, feed := owner.visit(t, "GET", channelURL, nil, 200)
	if strings.Count(feed, ">design</bdi>") != 2 || !strings.Contains(feed, "stays here") {
		t.Fatal("the feed does not label the moved messages with their new topic")
	}

	// Two destinations are refused before anything changes.
	destination := strings.TrimPrefix(topicURL, channelURL+"/topics/")
	branch(t, owner, channelURL, url.Values{"message": {ids["stays here"]}, "from": {source}, "to": {destination}, "name": {"both"}}, http.StatusUnprocessableEntity)
	var stays string
	acceptanceOK(t, pool.QueryRow(t.Context(), "SELECT topic_id::text FROM message WHERE id = $1", ids["stays here"]).Scan(&stays))
	if stays != source {
		t.Fatal("a request with two destinations moved a message")
	}

	// Branching the same messages again from the default topic is stale.
	branch(t, owner, channelURL, url.Values{"message": {ids["moves away"]}, "from": {source}, "name": {"late"}}, http.StatusConflict)
	var topics int
	acceptanceOK(t, pool.QueryRow(t.Context(), "SELECT count(*) FROM topic").Scan(&topics))
	if topics != 2 {
		t.Fatalf("a refused branch left %d topics, want 2", topics)
	}
}

// A branch must wake already-caught-up streams through the production wiring,
// without a watermark worker or initial replay hiding a disconnected notifier.
func testBranchingWakesOpenStreams(t *testing.T) {
	pool := acceptanceDatabase(t)
	server, streams, hub := streamServersWithHub(t, pool)
	owner := newAcceptanceBrowser(t, server, "192.0.2.10")
	owner.visit(t, "POST", "/setup", acceptanceForm("owner"), http.StatusSeeOther)
	response, _ := owner.visit(t, "GET", "/organizations/owner/", nil, http.StatusSeeOther)
	channelURL := response.Header.Get("Location")
	post(t, owner, channelURL, "moves live")

	var org, channel kernel.ID
	var source, message string
	acceptanceOK(t, pool.QueryRow(t.Context(), "SELECT organization_id, id, default_topic_id::text FROM channel WHERE is_default").Scan(&org, &channel, &source))
	// The destination must exist before its stream can be opened.
	topic := conversationtest.Topic(t, pool, org, channel, "design")
	var destination string
	acceptanceOK(t, pool.QueryRow(t.Context(), "SELECT $1::uuid::text", topic.ID).Scan(&destination))
	acceptanceOK(t, pool.QueryRow(t.Context(), "SELECT id::text FROM message WHERE organization_id = $1 AND body = 'moves live'", org).Scan(&message))

	subs := []struct {
		name, path string
		events     <-chan sseEvent
		want       []string
	}{
		{name: "feed", path: channelURL, want: []string{"messages-moved", "message"}},
		{name: "source", path: channelURL + "/topics/" + source, want: []string{"messages-moved", "message"}},
		{name: "destination", path: channelURL + "/topics/" + destination, want: []string{"messages-moved"}},
	}
	var cursor int64
	for i := range subs {
		sub := &subs[i]
		_, page := owner.visit(t, "GET", sub.path, nil, http.StatusOK)
		match := pageCursor.FindStringSubmatch(page)
		if len(match) != 2 {
			t.Fatalf("%s page has no event cursor", sub.name)
		}
		seq, err := strconv.ParseInt(match[1], 10, 64)
		acceptanceOK(t, err)
		if i > 0 && seq != cursor {
			t.Fatalf("%s cursor = %d, want %d", sub.name, seq, cursor)
		}
		cursor = seq
		var status int
		sub.events, status = openStream(t, on(owner, streams), sub.path, match[1])
		if status != http.StatusOK {
			t.Fatalf("%s stream status = %d, want 200", sub.name, status)
		}
	}
	// All setup writes have returned before opening the streams, and no
	// worker runs, so no raise is in flight while we inspect Waiting.
	for deadline := time.Now().Add(2 * time.Second); hub.Waiting(org) != len(subs); time.Sleep(time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("%d streams waiting on the hub, want %d", hub.Waiting(org), len(subs))
		}
	}

	branch(t, owner, channelURL, url.Values{"message": {message}, "from": {source}, "to": {destination}}, http.StatusSeeOther)
	// One shared deadline bounds delivery to all three streams after the POST
	// returns. Events arriving during the POST wait in openStream's buffered channel.
	deadline := time.NewTimer(2 * time.Second)
	defer deadline.Stop()
	for _, sub := range subs {
		for i, name := range sub.want {
			select {
			case event, ok := <-sub.events:
				if !ok {
					t.Fatalf("%s stream ended before %s", sub.name, name)
				}
				if event.name != name || event.id != strconv.FormatInt(cursor+int64(i)+1, 10) {
					t.Fatalf("%s event = %+v, want %s at sequence %d", sub.name, event, name, cursor+int64(i)+1)
				}
				if name == "messages-moved" {
					if !strings.Contains(event.data, `id="message-`+strings.ReplaceAll(message, "-", "")+`"`) || !strings.Contains(event.data, "moves live") {
						t.Fatalf("%s move lacks the moved message: %s", sub.name, event.data)
					}
				} else if !strings.Contains(event.data, "Moved 1 to “design”") {
					t.Fatalf("%s message is not the branch notice: %s", sub.name, event.data)
				}
			case <-deadline.C:
				t.Fatalf("%s: no %s within 2 s of branch POST", sub.name, name)
			}
		}
	}
	// The notice is in the same committed batch as the move, but belongs
	// only to the source topic. Keep watching the destination for leaks.
	select {
	case event, ok := <-subs[2].events:
		t.Fatalf("destination after move: event %+v, open = %t; want no notice and an open stream", event, ok)
	case <-time.After(200 * time.Millisecond):
	}
}

// branch posts the branching form without JavaScript and returns where a
// success redirects.
func branch(t *testing.T, b acceptanceBrowser, channelURL string, form url.Values, status int) string {
	t.Helper()
	response, err := b.client.Do(b.request(t, "POST", channelURL+"/branch", form))
	acceptanceOK(t, err)
	_, _ = io.Copy(io.Discard, response.Body)
	acceptanceOK(t, response.Body.Close())
	if response.StatusCode != status {
		t.Fatalf("branch: status %d, want %d", response.StatusCode, status)
	}
	return response.Header.Get("Location")
}
