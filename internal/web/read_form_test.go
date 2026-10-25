package web

import (
	"context"
	"fmt"
	"math/rand"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/tkakkie/ribbitto/internal/kernel"
	"github.com/tkakkie/ribbitto/internal/org"
	"github.com/tkakkie/ribbitto/internal/unread"
	"github.com/tkakkie/ribbitto/internal/web/middleware"
	"github.com/tkakkie/ribbitto/internal/web/view"
	"golang.org/x/net/html"
)

func (f readCall) FeedShown(ctx context.Context, _ unread.Scope, _, _, _, _ int64) error {
	return f(ctx)
}
func (f readCall) TopicShown(ctx context.Context, _ unread.TopicScope, _, _, _, _ int64) error {
	return f(ctx)
}
func (v *readControlVisit) FeedShown(ctx context.Context, s unread.Scope, j, c, _, _ int64) error {
	return v.Feed(ctx, s, j, c)
}
func (v *readControlVisit) TopicShown(ctx context.Context, s unread.TopicScope, j, c, _, _ int64) error {
	return v.Topic(ctx, s, j, c)
}
func (v *dividerVisit) FeedShown(ctx context.Context, s unread.Scope, j, c, _, _ int64) error {
	return v.Feed(ctx, s, j, c)
}
func (v *dividerVisit) TopicShown(ctx context.Context, s unread.TopicScope, j, c, _, _ int64) error {
	return v.Topic(ctx, s, j, c)
}

type readFormCall struct {
	scope                         unread.TopicScope
	joined, cursor, from, through int64
	shown                         bool
}
type formReading struct{ calls []readFormCall }

func (f *formReading) Feed(_ context.Context, s unread.Scope, j, c int64) error {
	f.calls = append(f.calls, readFormCall{scope: unread.TopicScope{Scope: s}, joined: j, cursor: c})
	return nil
}
func (f *formReading) Topic(_ context.Context, s unread.TopicScope, j, c int64) error {
	f.calls = append(f.calls, readFormCall{scope: s, joined: j, cursor: c})
	return nil
}
func (f *formReading) FeedShown(_ context.Context, s unread.Scope, j, c, a, b int64) error {
	f.calls = append(f.calls, readFormCall{unread.TopicScope{Scope: s}, j, c, a, b, true})
	return nil
}
func (f *formReading) TopicShown(_ context.Context, s unread.TopicScope, j, c, a, b int64) error {
	f.calls = append(f.calls, readFormCall{s, j, c, a, b, true})
	return nil
}

func checkReadForm(t *testing.T, body, query url.Values, topic, hx bool, want *readFormCall) {
	t.Helper()
	checkReadRequest(t, body.Encode(), query.Encode(), topic, hx, want)
}

func checkReadRequest(t *testing.T, body, query string, topic, hx bool, want *readFormCall) {
	t.Helper()
	f := &formReading{}
	p := channelPages{channels: &fakeChannels{}, topics: fakeTopics{}, reading: f, readLimit: middleware.NewReadRateLimiter(time.Now)}
	r := httptest.NewRequest("POST", "/read?"+query, strings.NewReader(body))
	r.SetPathValue("channelID", "01000000-0000-0000-0000-000000000000")
	if topic {
		r.SetPathValue("topicID", streamTopic)
	}
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if hx {
		r.Header.Set("HX-Request", "true")
	}
	m := org.Membership{Organization: org.Organization{ID: kernel.ID{9}, Slug: "acme"}, Member: org.Member{ID: kernel.ID{8}, JoinedEventSeq: 2}}
	w := httptest.NewRecorder()
	p.read(w, r, m)
	status := 400
	if want != nil {
		status = 303
		if hx {
			status = 204
		}
	}
	if w.Code != status {
		t.Fatalf("body=%v query=%v status=%d want=%d", body, query, w.Code, status)
	}
	if want == nil {
		if len(f.calls) != 0 {
			t.Fatal("refused form called Reading")
		}
		return
	}
	expected := *want
	expected.scope.Scope = unread.Scope{OrganizationID: m.Organization.ID, ChannelID: kernel.ID{1}, MemberID: m.Member.ID}
	expected.joined = 2
	var selected *kernel.ID
	if topic {
		id := kernel.ID{6}
		selected = &id
		expected.scope.TopicID = id
	}
	if len(f.calls) != 1 || f.calls[0] != expected {
		t.Fatalf("calls=%+v want=%+v", f.calls, expected)
	}
	if !hx && w.Header().Get("Location") != view.ConversationURL("acme", kernel.ID{1}, selected) {
		t.Fatal("wrong redirect")
	}
}

func TestReadFormShown(t *testing.T) {
	valid := url.Values{"cursor": {"10"}, "from": {"3"}, "through": {"7"}}
	type formCase struct {
		name        string
		form, query url.Values
		want        *readFormCall
	}
	cases := []formCase{
		{"run", valid, nil, &readFormCall{cursor: 10, from: 3, through: 7, shown: true}},
		{"cursor missing", url.Values{"from": {"3"}, "through": {"7"}}, nil, nil},
		{"from without through", url.Values{"cursor": {"10"}, "from": {"3"}}, nil, nil},
		{"through without from", url.Values{"cursor": {"10"}, "through": {"7"}}, nil, nil},
		{"scope other than all", url.Values{"cursor": {"10"}, "scope": {"x"}}, nil, nil},
		{"scope all with from", url.Values{"cursor": {"10"}, "scope": {"all"}, "from": {"3"}}, nil, nil},
		{"scope all with through", url.Values{"cursor": {"10"}, "scope": {"all"}, "through": {"7"}}, nil, nil},
		{"scope all with run", url.Values{"cursor": {"10"}, "scope": {"all"}, "from": {"3"}, "through": {"7"}}, nil, nil},
	}
	for _, key := range []string{"organization_id", "channel_id", "topic_id", "unknown"} {
		form, _ := url.ParseQuery(valid.Encode())
		form.Set(key, "foreign")
		cases = append(cases, formCase{key + " ignored; URL scope", form, url.Values{"unknown": {"x"}}, &readFormCall{cursor: 10, from: 3, through: 7, shown: true}})
	}
	for _, key := range []string{"cursor", "from", "through", "scope"} {
		form, _ := url.ParseQuery(valid.Encode())
		if key == "scope" {
			form = url.Values{"cursor": {"10"}, "scope": {"all"}}
		}
		single, _ := url.ParseQuery(form.Encode())
		cases = append(cases, formCase{key + " repeated across query and body", single, url.Values{key: {single.Get(key)}}, nil})
		cases = append(cases, formCase{key + " repeated in query", single, url.Values{key: {"x", "x"}}, nil})
		form[key] = append(form[key], form[key][0])
		cases = append(cases, formCase{key + " repeated", form, nil, nil})
	}
	for _, key := range []string{"cursor", "from", "through"} {
		for _, value := range []string{"", "x", " 3", "3 ", "0x3", "9223372036854775808", "+", "-"} {
			form, _ := url.ParseQuery(valid.Encode())
			form.Set(key, value)
			cases = append(cases, formCase{key + " malformed " + strconv.Quote(value), form, nil, nil})
		}
		for _, value := range []string{"003", "+3", "-3"} {
			form, _ := url.ParseQuery(valid.Encode())
			form.Set(key, value)
			want := readFormCall{cursor: 10, from: 3, through: 7, shown: true}
			n, _ := strconv.ParseInt(value, 10, 64)
			switch key {
			case "cursor":
				want.cursor = n
			case "from":
				want.from = n
			case "through":
				want.through = n
			}
			cases = append(cases, formCase{key + " decimal " + value, form, nil, &want})
		}
	}
	for _, form := range []url.Values{valid, {"cursor": {"10"}, "scope": {"all"}}, {"cursor": {"10"}}} {
		for _, key := range []string{"before", "around"} {
			for _, query := range []bool{false, true} {
				body, _ := url.ParseQuery(form.Encode())
				q := url.Values{}
				if query {
					q.Set(key, "")
				} else {
					body.Set(key, "")
				}
				cases = append(cases, formCase{fmt.Sprintf("%s/%s/query=%t", form.Encode(), key, query), body, q, nil})
			}
		}
	}
	for _, where := range []string{"body", "query"} {
		t.Run("malformed "+where, func(t *testing.T) {
			body, query := "cursor=10&from=3&through=7", ""
			if where == "body" {
				body += "&x=%zz"
			} else {
				query = "x=%zz"
			}
			checkReadRequest(t, body, query, false, true, nil)
		})
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for _, topic := range []bool{false, true} {
				checkReadForm(t, tc.form, tc.query, topic, true, tc.want)
			}
		})
	}
}

func TestReadLegacyCursorOnly(t *testing.T) {
	for _, topic := range []bool{false, true} {
		for _, hx := range []bool{false, true} {
			checkReadForm(t, url.Values{"cursor": {"10"}}, nil, topic, hx, &readFormCall{cursor: 10})
		}
	}
}
func TestReadFormScopeAll(t *testing.T) {
	for _, topic := range []bool{false, true} {
		for _, hx := range []bool{false, true} {
			checkReadForm(t, url.Values{"cursor": {"10"}, "scope": {"all"}}, nil, topic, hx, &readFormCall{cursor: 10})
		}
		h, _ := newLimitedHandler(t, asAlice, func(s *Services) {
			s.Messages = &readControlVisit{fakeMessages: populatedMessages(), feed: 7, topic: 8}
			s.Reading = &formReading{}
		})
		path := view.ChannelURL("acme", kernel.ID{1})
		if topic {
			path = view.ConversationURL("acme", kernel.ID{1}, &kernel.ID{5})
		}
		r := httptest.NewRequest("GET", path+"/overview", nil)
		r.Header.Set("Cookie", middleware.SessionCookie+"=live")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		doc, err := html.Parse(w.Body)
		if err != nil {
			t.Fatal(err)
		}
		found := 0
		for n := range doc.Descendants() {
			if n.Data == "button" && (attr(n, "form") == "read-form" || n.Parent != nil && attr(n.Parent, "id") == "read-form") {
				found++
				if attr(n, "name") != "scope" || attr(n, "value") != "all" {
					t.Fatal("read submitter lacks scope=all")
				}
			}
		}
		if w.Code != 200 || found != 2 {
			t.Fatalf("status=%d buttons=%d", w.Code, found)
		}
	}
}

// The model deliberately works on the drawn values without the production parser.
func modelReadForm(body, query url.Values) *readFormCall {
	for _, key := range []string{"cursor", "from", "through", "scope"} {
		if len(body[key])+len(query[key]) > 1 {
			return nil
		}
	}
	if len(body["cursor"]) == 0 {
		return nil
	}
	numbers := map[string]int64{}
	for _, key := range []string{"cursor", "from", "through"} {
		if len(body[key]) == 0 {
			continue
		}
		n, err := strconv.ParseInt(body[key][0], 10, 64)
		if err != nil {
			return nil
		}
		numbers[key] = n
	}
	_, from := body["from"]
	_, through := body["through"]
	if from != through {
		return nil
	}
	if scope, ok := body["scope"]; ok && (scope[0] != "all" || from || through) {
		return nil
	}
	for _, key := range []string{"before", "around"} {
		if body.Has(key) || query.Has(key) {
			return nil
		}
	}
	return &readFormCall{cursor: numbers["cursor"], from: numbers["from"], through: numbers["through"], shown: from}
}
func TestReadFormOracle(t *testing.T) {
	const seed int64 = 15681486
	t.Logf("read form oracle seed=%d", seed)
	rng := rand.New(rand.NewSource(seed))
	var outcomes [4]int
	for i := range 2000 {
		body, query := url.Values{}, url.Values{}
		for _, form := range []url.Values{body, query} {
			for _, key := range []string{"cursor", "from", "through", "scope", "before", "around", "unknown"} {
				values := []string{"10", "3", "7", "0003", "+3", "-3", "0", "9223372036854775807", "9223372036854775808", "", "x", "0x3", " 3", "3 ", "+", "-"}
				if key == "scope" {
					values = []string{"all", "x", ""}
				}
				if rng.Intn(4) == 0 {
					form[key] = []string{values[rng.Intn(len(values))]}
					if rng.Intn(4) == 0 {
						form.Add(key, values[rng.Intn(len(values))])
					}
				}
			}
		}
		// Mix complete forms into the independent key draws so successful
		// runs and both whole-scope forms cannot be drowned by syntax refusals.
		if i%4 == 0 {
			body = url.Values{"cursor": {"10"}}
			switch rng.Intn(3) {
			case 0:
				body.Set("from", "3")
				body.Set("through", "7")
			case 1:
				body.Set("scope", "all")
			}
		}
		want, outcome := modelReadForm(body, query), 0
		if want != nil {
			outcome = 1
			if body.Has("scope") {
				outcome = 2
			}
			if want.shown {
				outcome = 3
			}
		}
		outcomes[outcome]++
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			checkReadForm(t, body, query, rng.Intn(2) == 0, rng.Intn(2) == 0, want)
		})
	}
	t.Logf("refused/legacy/all/run=%v", outcomes)
	for _, count := range outcomes {
		if count == 0 {
			t.Fatal("oracle missed a form outcome")
		}
	}
}
