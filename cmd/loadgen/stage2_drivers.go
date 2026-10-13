package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Mirror the seed file without importing the seed command or application code.
type stage2Credentials struct {
	Slug     string   `json:"organization_slug"`
	Channels []string `json:"channel_ids"`
	Accounts []struct {
		MemberID string   `json:"member_id"`
		Handle   string   `json:"handle"`
		Tokens   []string `json:"tokens"`
	} `json:"accounts"`
	Stage2 struct {
		Version         int                         `json:"version"`
		FixtureSeed     uint64                      `json:"fixture_seed"`
		HotChannelID    string                      `json:"hot_channel_id"`
		DefaultTopicIDs []string                    `json:"default_topic_ids"`
		NamedTopics     []struct{ ID, Name string } `json:"named_topics"`
	} `json:"stage2"`
}

func readStage2Credentials(path string, group stage2Group, seed uint64) (stage2Credentials, error) {
	var data stage2Credentials
	f, err := os.Open(path)
	if err != nil {
		return data, fmt.Errorf("cannot open stage-2 credentials")
	}
	defer func() { _ = f.Close() }()
	raw, err := io.ReadAll(io.LimitReader(f, (16<<20)+1))
	if err != nil || len(raw) > 16<<20 {
		return data, fmt.Errorf("cannot read stage-2 credentials")
	}
	obj, err := requiredObject(raw, "organization_slug", "channel_ids", "accounts", "stage2")
	if err != nil {
		return data, fmt.Errorf("invalid stage-2 credentials")
	}
	if _, err := requiredObject(obj["stage2"], "version", "fixture_seed", "hot_channel_id", "default_topic_ids", "named_topics"); err != nil {
		return data, fmt.Errorf("invalid stage-2 metadata")
	}
	if json.Unmarshal(raw, &data) != nil {
		return data, fmt.Errorf("invalid stage-2 credentials")
	}
	p := fixedStage2()
	m := data.Stage2
	if !slices.Contains(p.Groups[:], group) || m.Version != p.Version || m.FixtureSeed != seed || len(data.Channels) != p.Fixture.Channels || len(m.DefaultTopicIDs) != p.Fixture.Channels || len(m.NamedTopics) != p.Fixture.NamedTopics || len(data.Accounts) != group.Streams || m.HotChannelID != data.Channels[0] {
		return data, fmt.Errorf("stage-2 fixture mismatch")
	}
	if !regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`).MatchString(data.Slug) {
		return data, fmt.Errorf("invalid stage-2 organization slug")
	}
	ids := regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
	seen := map[string]bool{}
	allIDs := append(slices.Clone(data.Channels), m.DefaultTopicIDs...)
	for i, topic := range m.NamedTopics {
		if topic.Name != fmt.Sprintf("stage2-topic-%d", i+1) {
			return data, fmt.Errorf("invalid stage-2 named topic")
		}
		allIDs = append(allIDs, topic.ID)
	}
	for _, account := range data.Accounts {
		allIDs = append(allIDs, account.MemberID)
	}
	for _, id := range allIDs {
		if !ids.MatchString(id) || seen[id] {
			return data, fmt.Errorf("invalid or duplicate stage-2 ID")
		}
		seen[id] = true
	}
	seenTokens := map[string]bool{}
	for _, account := range data.Accounts {
		if len(account.Tokens) != 1 {
			return data, fmt.Errorf("stage-2 member needs one session")
		}
		token := account.Tokens[0]
		if token == "" || seenTokens[token] || (&http.Cookie{Name: "__Host-session", Value: token}).Valid() != nil {
			return data, fmt.Errorf("invalid or duplicate stage-2 session")
		}
		seenTokens[token] = true
	}
	return data, nil
}

type stage2Driver struct {
	transport transport
	data      stage2Credentials
}

func newStage2Driver(target, path string, group stage2Group, seed uint64) (*stage2Driver, error) {
	data, err := readStage2Credentials(path, group, seed)
	if err != nil {
		return nil, err
	}
	u, err := url.Parse(target)
	if err != nil || u.Scheme != "http" {
		return nil, fmt.Errorf("stage-2 target must use loopback HTTP/1.1")
	}
	tr, err := newTransport(target, "", &counts{})
	if err != nil {
		return nil, err
	}
	tr.ForceAttemptHTTP2 = false
	return &stage2Driver{tr, data}, nil
}

// Rotation depends on the scheduled ID, never completion order or missed slots.
// Named topics alternate on successive topic slots of either schedule phase.
// The caller supplies the page cursor for posting's implicit read; this slice
// does not discover cursors or advance/verify explicit reads.
func (d *stage2Driver) request(ctx context.Context, slot stage2Slot, body string, cursor uint64) (*http.Request, error) {
	if (slot.Operation != "post" && slot.Operation != "page") || (slot.Endpoint != "feed" && slot.Endpoint != "topic") {
		return nil, fmt.Errorf("unsupported stage-2 driver slot")
	}
	endpoint := d.transport.origin.Scheme + "://" + d.transport.origin.Host + "/organizations/" + url.PathEscape(d.data.Slug) + "/channels/" + d.data.Stage2.HotChannelID
	if slot.Endpoint == "topic" {
		endpoint += "/topics/" + d.data.Stage2.NamedTopics[(slot.LogicalID/2)%uint64(len(d.data.Stage2.NamedTopics))].ID
	}
	method := http.MethodGet
	var form io.Reader
	if slot.Operation == "post" {
		method = http.MethodPost
		form = strings.NewReader(url.Values{"body": {body}, "cursor": {strconv.FormatUint(cursor, 10)}}.Encode())
	}
	req, err := http.NewRequestWithContext(ctx, method, endpoint, form)
	if err != nil {
		return nil, fmt.Errorf("building stage-2 request: %w", err)
	}
	req.AddCookie(&http.Cookie{Name: "__Host-session", Value: d.data.Accounts[slot.LogicalID%uint64(len(d.data.Accounts))].Tokens[0]})
	if method == http.MethodPost {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("Origin", d.transport.origin.Scheme+"://"+d.transport.origin.Host)
		req.Header.Set("Sec-Fetch-Site", "same-origin")
		if slot.Endpoint == "feed" {
			req.Header.Set("HX-Request", "true")
		}
		// Transport retries replayable requests; a POST must have one attempt.
		req.GetBody = nil
	}
	return req, nil
}

func (d *stage2Driver) drive(ctx context.Context, o stage2Observation, start time.Time, now func() time.Time, body string, cursor uint64) (stage2Observation, error) {
	req, err := d.request(ctx, o.Slot, body, cursor)
	if err != nil {
		return o, err
	}
	return stage2HTTP(ctx, d.transport, req, o, start, now, nil), nil
}
