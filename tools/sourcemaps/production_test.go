package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestProductionRetention(t *testing.T) {
	now, day := time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC), 24*time.Hour
	retired := func(d time.Duration) string { return productionRetired + now.Add(-d).Format(time.RFC3339) }
	for _, tc := range []struct {
		name, body, concurrent, class, identity string
		age, newer                              int
		failRead, wantErr, del, duplicate       bool
	}{
		{name: "ordinary", age: 200, newer: 10, del: true},
		{name: "absent body", class: "absent", age: 200, newer: 10, del: true},
		{name: "null body", class: "null", age: 200, newer: 10, del: true},
		{name: "identity id", identity: "id", age: 200, newer: 10, wantErr: true},
		{name: "identity name", identity: "name", age: 200, newer: 10, wantErr: true},
		{name: "identity tag_name", identity: "tag_name", age: 200, newer: 10, wantErr: true},
		{name: "identity draft", identity: "draft", age: 200, newer: 10, wantErr: true},
		{name: "identity prerelease", identity: "prerelease", age: 200, newer: 10, wantErr: true},
		{name: "active old outside floor", body: productionActive, age: 500, newer: 11},
		{name: "active older duplicate", body: productionActive, age: 500, newer: 10, duplicate: true},
		{name: "active newest", body: productionActive, age: 500},
		{name: "active young", body: productionActive, age: 1, newer: 10},
		{name: "retired before margin", body: retired(90*day - time.Second), age: 200, newer: 10},
		{name: "retired at margin", body: retired(90 * day), age: 200, newer: 10, del: true},
		{name: "retired after margin", body: retired(91 * day), age: 200, newer: 10, del: true},
		{name: "retired age floor", body: retired(91 * day), age: 90, newer: 10},
		{name: "retired SHA floor", body: retired(91 * day), age: 200, newer: 9},
		{name: "invalid", body: "broken", age: 200, newer: 10, wantErr: true},
		{name: "invalid absent body", body: "broken", class: "absent", age: 200, newer: 10, wantErr: true},
		{name: "invalid null body", body: "broken", class: "null", age: 200, newer: 10, wantErr: true},
		{name: "invalid old draft", body: "broken", class: "draft", age: 200, newer: 10, wantErr: true},
		{name: "invalid young draft", body: "broken", class: "draft", age: 1, newer: 10, wantErr: true},
		{name: "invalid prerelease", body: "broken", class: "prerelease", age: 200, newer: 10, wantErr: true},
		{name: "invalid unparsable", body: "broken", class: "unparsable", age: 200, newer: 10, wantErr: true},
		{name: "invalid mismatch", body: "broken", class: "mismatch", age: 200, newer: 10, wantErr: true},
		{name: "invalid future", body: "broken", age: -10, newer: 10, wantErr: true},
		{name: "invalid young", body: "broken", age: 1, newer: 10, wantErr: true},
		{name: "retirement offset", body: productionRetired + "2026-08-01T09:00:00+09:00", age: 200, newer: 10, wantErr: true},
		{name: "retirement fraction", body: productionRetired + "2026-08-01T00:00:00.5Z", age: 200, newer: 10, wantErr: true},
		{name: "unknown version", body: "sourcemaps-production-v2 active", age: 200, newer: 10, wantErr: true},
		{name: "invalid retirement", body: productionRetired + "yesterday", age: 200, newer: 10, wantErr: true},
		{name: "future retirement", body: retired(-day), age: 200, newer: 10, wantErr: true},
		{name: "unreadable", age: 200, newer: 10, failRead: true, wantErr: true},
		{name: "concurrent marking", concurrent: productionActive, age: 200, newer: 10, wantErr: true},
		{name: "concurrent retirement", concurrent: retired(100 * day), age: 200, newer: 10, wantErr: true},
		{name: "concurrent unreadable", concurrent: "error", age: 200, newer: 10, wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := &mockStore{token: "tok"}
			// Another deletable release, abandoned draft and orphan must survive
			// an invalid/unreadable marker anywhere in the initial population.
			for i := range tc.newer + 4 {
				age := tc.age
				if i > 0 && i <= tc.newer {
					age = 0
				} else if i > tc.newer {
					age += i
				}
				sha := shaOf(i + 1)
				if tc.duplicate && i == tc.newer+1 {
					sha = shaOf(1)
					age = tc.age - 1
				}
				name := relNameAt(now.Add(-time.Duration(age)*day), sha, i+1, 1)
				s.releases = append(s.releases, mockRelease{ID: int64(i + 1), Name: name, TagName: name})
				s.tags = append(s.tags, name)
			}
			s.releases[0].Body = tc.body
			switch tc.class {
			case "draft":
				s.releases[0].Draft = true
			case "prerelease":
				s.releases[0].Prerelease = true
			case "unparsable":
				s.releases[0].Name, s.releases[0].TagName = "hand-made", "hand-made"
			case "mismatch":
				s.releases[0].TagName = "other"
			}
			target := s.releases[0]
			s.releases[len(s.releases)-1].Draft = true
			s.tags = append(s.tags, relNameAt(now.Add(-300*day), shaOf(99), 99, 1))
			s.releases = slices.Concat(s.releases[1:], s.releases[:1])
			reads, releaseGETs, population := 0, 0, len(s.releases)
			s.before = func(w http.ResponseWriter, r *http.Request) bool {
				if r.Method == "GET" && strings.HasPrefix(r.URL.Path, "/repos/"+prodStoreRepo+"/releases/") && !strings.Contains(r.URL.Path, "/tags/") {
					releaseGETs++
				}
				if (tc.failRead || tc.identity != "" || tc.class == "absent" || tc.class == "null") && r.URL.Path == "/repos/"+prodStoreRepo+"/releases" {
					data, _ := json.Marshal(s.releases)
					if tc.class == "null" {
						data = bytes.ReplaceAll(data, []byte(fmt.Sprintf(`"body":%q`, tc.body)), []byte(`"body":null`))
					} else {
						data = bytes.ReplaceAll(data, []byte(fmt.Sprintf(`"body":%q,`, tc.body)), nil)
					}
					_, _ = w.Write(data)
					return true
				}
				if r.Method == "GET" && r.URL.Path == "/repos/"+prodStoreRepo+"/releases/1" {
					reads++
					if reads == 1 && tc.identity != "" {
						data, _ := json.Marshal(target)
						var changed map[string]any
						_ = json.Unmarshal(data, &changed)
						changed[tc.identity] = map[string]any{"id": 999, "name": "other", "tag_name": "other", "draft": !target.Draft, "prerelease": !target.Prerelease}[tc.identity]
						_ = json.NewEncoder(w).Encode(changed)
						return true
					}
					if tc.failRead || (reads == 1 && tc.concurrent == "error") {
						http.Error(w, "unreadable", 500)
						return true
					}
					if reads == 1 && tc.concurrent != "" {
						for i := range s.releases {
							if s.releases[i].ID == 1 {
								s.releases[i].Body = tc.concurrent
							}
						}
					}
				}
				return false
			}
			srv := s.server(t)
			defer srv.Close()
			c := &apiClient{cfg: testCfg(srv.URL, "tok", now, io.Discard)}
			err := runRetention(t.Context(), c, "")
			if (err != nil) != tc.wantErr {
				t.Fatalf("error = %v, want error %v", err, tc.wantErr)
			}
			if tc.wantErr && tc.concurrent == "" && len(s.events) != 0 {
				t.Fatalf("initial marker failure must delete nothing: %v", s.events)
			}
			if tc.wantErr && tc.body != "" {
				reason := "invalid production marker"
				if tc.name == "future retirement" {
					reason = "retirement time is in the future"
				}
				if !strings.Contains(err.Error(), fmt.Sprintf("release 1 tag %s: %s", safeQuote(target.TagName), reason)) || strings.Contains(err.Error(), tc.body) {
					t.Fatalf("marker diagnostic: %v", err)
				}
			}
			if tc.del {
				wantGETs := c.delRels
				if tc.class == "absent" || tc.class == "null" {
					wantGETs += population
				}
				t.Logf("N=%d, D=%d: release GETs=%d", population, c.delRels, releaseGETs)
				if releaseGETs != wantGETs {
					t.Fatalf("N=%d: release GETs=%d, want %d", population, releaseGETs, wantGETs)
				}
			}
			deleted := slices.Contains(s.events, "del-rel:1")
			if deleted != tc.del {
				t.Fatalf("deleted=%v, want %v; events=%v", deleted, tc.del, s.events)
			}
		})
	}
}

func TestProductionTransitions(t *testing.T) {
	now := time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)
	name := relNameAt(now.Add(-200*24*time.Hour), shaOf(1), 1, 1)
	for _, tc := range []struct {
		name, initial, state, want string
		wantErr, draft, prerelease bool
	}{
		{"mark", "", "active", productionActive, false, false, false},
		{"retire", productionActive, "retired", productionRetired + now.Format(time.RFC3339), false, false, false},
		{"retire twice", productionRetired + now.Add(-24*time.Hour).Format(time.RFC3339), "retired", productionRetired + now.Add(-24*time.Hour).Format(time.RFC3339), false, false, false},
		{"rollback", productionRetired + now.Add(-24*time.Hour).Format(time.RFC3339), "active", productionActive, false, false, false},
		{"unmarked retirement", "", "retired", "", true, false, false},
		{"invalid marker", "invalid", "active", "invalid", true, false, false},
		{"invalid state", "", "unknown", "", true, false, false},
		{"draft", "", "active", "", true, true, false},
		{"prerelease", "", "active", "", true, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := &mockStore{token: "tok", releases: []mockRelease{{ID: 1, Name: name, TagName: name, Body: tc.initial, Draft: tc.draft, Prerelease: tc.prerelease}}}
			s.before = func(w http.ResponseWriter, r *http.Request) bool {
				if r.Method == "GET" {
					_ = json.NewEncoder(w).Encode(s.releases[0]) // Exercise rejection even if the API returns a draft by tag.
					return true
				}
				var payload map[string]string
				_ = json.NewDecoder(r.Body).Decode(&payload)
				if len(payload) != 1 || payload["body"] != tc.want {
					t.Errorf("PATCH must contain only body: %v", payload)
					http.Error(w, "extra PATCH fields", http.StatusUnprocessableEntity)
					return true
				}
				data, _ := json.Marshal(payload)
				r.Body = io.NopCloser(bytes.NewReader(data))
				return false
			}
			srv := s.server(t)
			defer srv.Close()
			var out bytes.Buffer
			err := runProduction(t.Context(), testCfg(srv.URL, "tok", now, &out), name, tc.state)
			if (err != nil) != tc.wantErr || s.releases[0].Body != tc.want || s.releases[0].Draft != tc.draft || s.releases[0].Name != name {
				t.Fatalf("transition: body=%q err=%v", s.releases[0].Body, err)
			}
			if !tc.wantErr && out.String() != fmt.Sprintf("production release 1 tag %s: %s\n", safeQuote(name), tc.state) {
				t.Fatalf("success log: %q", out.String())
			}
		})
	}
}
