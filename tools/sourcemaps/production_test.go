package main

import (
	"io"
	"net/http"
	"slices"
	"testing"
	"time"
)

func TestProductionRetention(t *testing.T) {
	now, day := time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC), 24*time.Hour
	retired := func(d time.Duration) string { return productionRetired + now.Add(-d).Format(time.RFC3339) }
	for _, tc := range []struct {
		name, body, concurrent            string
		age, newer                        int
		failRead, wantErr, del, duplicate bool
	}{
		{name: "ordinary", age: 200, newer: 10, del: true},
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
			s.releases[len(s.releases)-1].Draft = true
			s.tags = append(s.tags, relNameAt(now.Add(-300*day), shaOf(99), 99, 1))
			s.releases = slices.Concat(s.releases[1:], s.releases[:1])
			reads := 0
			s.before = func(w http.ResponseWriter, r *http.Request) bool {
				if r.Method == "GET" && r.URL.Path == "/repos/"+prodStoreRepo+"/releases/1" {
					reads++
					if tc.failRead || (reads == 2 && tc.concurrent == "error") {
						http.Error(w, "unreadable", 500)
						return true
					}
					if reads == 2 && tc.concurrent != "" {
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
			deleted := slices.Contains(s.events, "del-rel:1")
			if deleted != tc.del {
				t.Fatalf("deleted=%v, want %v; events=%v", deleted, tc.del, s.events)
			}
			if tc.wantErr && tc.concurrent == "" && len(s.events) != 0 {
				t.Fatalf("initial marker failure must delete nothing: %v", s.events)
			}
		})
	}
}

func TestProductionTransitions(t *testing.T) {
	now := time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)
	name := relNameAt(now.Add(-200*24*time.Hour), shaOf(1), 1, 1)
	for _, tc := range []struct {
		name, initial, state, want string
		wantErr                    bool
	}{
		{"mark", "", "active", productionActive, false},
		{"retire", productionActive, "retired", productionRetired + now.Format(time.RFC3339), false},
		{"retire twice", productionRetired + now.Add(-24*time.Hour).Format(time.RFC3339), "retired", productionRetired + now.Add(-24*time.Hour).Format(time.RFC3339), false},
		{"rollback", productionRetired + now.Add(-24*time.Hour).Format(time.RFC3339), "active", productionActive, false},
		{"unmarked retirement", "", "retired", "", true},
		{"invalid marker", "invalid", "active", "invalid", true},
		{"invalid state", "", "unknown", "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := &mockStore{token: "tok", releases: []mockRelease{{ID: 1, Name: name, TagName: name, Body: tc.initial}}}
			srv := s.server(t)
			defer srv.Close()
			err := runProduction(t.Context(), testCfg(srv.URL, "tok", now, io.Discard), name, tc.state)
			if (err != nil) != tc.wantErr || s.releases[0].Body != tc.want || s.releases[0].Draft || s.releases[0].Name != name {
				t.Fatalf("transition: body=%q err=%v", s.releases[0].Body, err)
			}
		})
	}
}
