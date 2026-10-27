package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestReadSnapshot(t *testing.T) {
	const fullBody = `{"streams":{"open":7},"runtime":{"goroutines":11,"heap_inuse_bytes":13},"pool":{"total_conns":3,"max_conns":5,"empty_acquire_count":2,"empty_acquire_wait_ns":17},"database":{"queries":19,"transactions_begun":23},"unknown":29}`
	var full snapshot
	full.Streams.Open = 7
	full.Runtime.Goroutines = 11
	full.Runtime.HeapInuseBytes = 13
	full.Pool.TotalConns = 3
	full.Pool.MaxConns = 5
	full.Pool.EmptyAcquireCount = 2
	full.Pool.EmptyAcquireWaitNS = 17
	full.Database.Queries = 19
	full.Database.TransactionsBegun = 23

	for _, tc := range []struct {
		name        string
		handler     http.HandlerFunc
		closeServer bool
		want        snapshot
		errPrefix   string
		errSubstr   string
	}{
		{
			name: "all fields",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				_, _ = fmt.Fprint(w, fullBody)
			},
			want: full,
		},
		{
			name: "status 503",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusServiceUnavailable)
				_, _ = fmt.Fprint(w, fullBody)
			},
			errSubstr: "metrics HTTP status 503",
		},
		{
			name: "not json",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				_, _ = fmt.Fprint(w, "not json")
			},
			errPrefix: "decoding metrics: ",
		},
		{
			name: "oversized body",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				_, _ = fmt.Fprintf(w, `{"padding":%q}`, strings.Repeat("A", 2<<20))
			},
			errPrefix: "decoding metrics: ",
		},
		{
			name: "redirect off origin",
			handler: func(w http.ResponseWriter, r *http.Request) {
				http.Redirect(w, r, "http://192.0.2.1/metrics", http.StatusFound)
			},
			errPrefix: "reading metrics: ",
			errSubstr: "request leaves target origin",
		},
		{
			name: "closed server",
			handler: func(http.ResponseWriter, *http.Request) {
			},
			closeServer: true,
			errPrefix:   "reading metrics: ",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/metrics" {
					t.Errorf("%s: path = %q, want %q", tc.name, r.URL.Path, "/metrics")
				}
				if cookies := r.Cookies(); len(cookies) != 0 {
					t.Errorf("%s: cookies = %v, want []", tc.name, cookies)
				}
				tc.handler(w, r)
			}))
			defer server.Close()
			if tc.closeServer {
				server.Close()
			}

			origin, err := url.Parse(server.URL)
			if err != nil {
				t.Fatalf("%s: url.Parse(%q) = %v, want nil", tc.name, server.URL, err)
			}
			tr := transport{Transport: &http.Transport{}, origin: origin}
			defer tr.CloseIdleConnections()

			var got snapshot
			err = readSnapshot(tr, &got)
			wantErr := tc.errPrefix != "" || tc.errSubstr != ""
			if !wantErr && err != nil {
				t.Errorf("%s: error = %v, want nil", tc.name, err)
			}
			if wantErr && err == nil {
				t.Errorf("%s: error = nil, want prefix %q and substring %q", tc.name, tc.errPrefix, tc.errSubstr)
			}
			if err != nil && tc.errPrefix != "" && !strings.HasPrefix(err.Error(), tc.errPrefix) {
				t.Errorf("%s: error = %q, want prefix %q", tc.name, err.Error(), tc.errPrefix)
			}
			if err != nil && tc.errSubstr != "" && !strings.Contains(err.Error(), tc.errSubstr) {
				t.Errorf("%s: error = %q, want substring %q", tc.name, err.Error(), tc.errSubstr)
			}
			if got != tc.want {
				t.Errorf("%s: snapshot = %+v, want %+v", tc.name, got, tc.want)
			}
		})
	}
}
