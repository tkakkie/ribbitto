package main

import (
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
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
