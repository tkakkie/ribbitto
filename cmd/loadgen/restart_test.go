package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"os/signal"
	"regexp"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

// Re-execution exercises real signals and Wait without a database or tool binary.
func TestRestartHelper(t *testing.T) {
	mode := os.Getenv("LOADGEN_HELPER")
	if mode == "" {
		return
	}
	if mode == "fail" {
		os.Exit(2)
	}
	state := os.Getenv("LOADGEN_STATE")
	_, err := os.Stat(state)
	restarted := err == nil
	if err := os.WriteFile(state, nil, 0600); err != nil {
		os.Exit(3)
	}
	if restarted && mode == "relaunch-fail" {
		os.Exit(2)
	}
	if (!restarted && mode == "handover") || (restarted && mode == "unavailable") {
		if err := os.WriteFile(state+"commit", []byte("seed\n"), 0600); err != nil {
			os.Exit(4)
		}
	}
	cursor := func() int {
		commits, _ := os.ReadFile(state + "commit")
		return 7 + bytes.Count(commits, []byte("\n"))
	}
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGTERM)
	go func() {
		<-sig
		if mode == "kill" {
			select {}
		}
		time.Sleep(40 * time.Millisecond)
		os.Exit(0)
	}()
	if mode == "never-bind" {
		select {}
	}
	var pages atomic.Int64
	var events [2]atomic.Int64
	err = http.ListenAndServe(os.Getenv("RIBBITTO_ADDR"), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if mode == "never" {
			w.WriteHeader(503)
			return
		}
		if r.URL.Path == "/metrics" {
			queries := 1
			if restarted {
				queries = 2
			}
			_, _ = fmt.Fprintf(w, `{"database":{"queries":%d}}`, queries)
			return
		}
		if r.Method == "POST" {
			f, err := os.OpenFile(state+"commit", os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
			if err != nil {
				os.Exit(4)
			}
			_, err = fmt.Fprintln(f, r.FormValue("body"))
			_ = f.Close()
			if err != nil {
				os.Exit(4)
			}
			if mode == "retry" && restarted {
				return
			}
			conn, _, _ := http.NewResponseController(w).Hijack()
			_ = conn.Close()
			return
		}
		if !strings.HasSuffix(r.URL.Path, "/events") {
			if restarted {
				n := pages.Add(1)
				if n >= 2 {
					_ = os.WriteFile(state+"ready", nil, 0600)
				}
				if n >= 3 {
					_ = os.WriteFile(state+"drain", nil, 0600)
				}
			}
			_, _ = fmt.Fprintf(w, `<div sse-connect="/events?after=%d"></div>`, cursor())
			return
		}
		cookie, _ := r.Cookie("__Host-session")
		if !restarted && mode == "held" && events[0].Add(1) == 1 {
			if err := os.WriteFile(state+"commit", []byte("seed\n"), 0600); err != nil {
				os.Exit(4)
			}
		}
		if mode == "total" || (mode == "partial" && cookie.Value == "bad") {
			w.WriteHeader(403)
			return
		}
		if restarted && (mode == "held" || mode == "lost" || mode == "handover" || mode == "unavailable" || mode == "retry") {
			for {
				if _, err := os.Stat(state + "drain"); err == nil {
					break
				}
				if !waitRetry(r.Context(), time.Millisecond, 0) {
					return
				}
			}
			idx := 0
			if cookie.Value == "bad" {
				idx = 1
			}
			if mode == "unavailable" && events[idx].Add(1) <= 10 {
				w.WriteHeader(503)
				return
			}
			time.Sleep(100 * time.Millisecond)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_ = http.NewResponseController(w).Flush()
		if restarted || mode == "held" {
			commits, _ := os.ReadFile(state + "commit")
			_, _ = fmt.Fprintf(w, "id: %d\nevent: message\ndata: %s\n\n", cursor(), strings.Join(strings.Fields(string(commits)), " "))
			_ = http.NewResponseController(w).Flush()
		}
		if restarted && mode == "handover" {
			_, _ = fmt.Fprint(w, "id: 9\nevent: messages-moved\ndata: control\n\n")
			_ = http.NewResponseController(w).Flush()
		}
		<-r.Context().Done()
	}))
	if err != nil {
		os.Exit(5)
	}
}

func TestRestart(t *testing.T) {
	for _, tc := range []struct{ mode, kind, failure string }{
		{"exit", "lifecycle", ""}, {"kill", "lifecycle", ""},
		{"fail", "lifecycle", "exited"}, {"never", "lifecycle", "deadline"},
		{"never-bind", "lifecycle", "deadline"}, {"missing", "lifecycle", "starting child"},
		{"occupied", "lifecycle", "occupied"}, {"held", "run", ""},
		{"lost", "run", ""}, {"retry", "run", ""}, {"partial", "run", ""},
		{"total", "run", ""}, {"unavailable", "run", ""}, {"relaunch-fail", "run", "exited before readiness"},
		{"at-readiness", "probe", "exited at readiness"}, {"handover", "handover", ""}, {"no-server", "flags", ""},
	} {
		t.Run(tc.mode, func(t *testing.T) {
			if tc.kind == "probe" {
				child := &childServer{done: make(chan struct{})}
				_, err := child.probe(t.Context(), nil, "", func(context.Context, *http.Client, string) (string, error) {
					close(child.done) // The reaper finishes while the successful probe is in flight.
					return "7", nil
				})
				failRestartIf(t, err == nil || !strings.Contains(err.Error(), tc.failure), err)
				return
			}
			if tc.kind == "flags" {
				err := run([]string{"-restart-after=1ms"}, &bytes.Buffer{})
				failRestartIf(t, err == nil || !strings.Contains(err.Error(), "requires -server"), err)
				for _, arg := range []string{"-server-addr=127.0.0.1:1", "-server-arg=x", "-start-deadline=1s", "-exit-deadline=1s"} {
					err := run([]string{arg}, &bytes.Buffer{})
					failRestartIf(t, err == nil || !strings.Contains(err.Error(), "requires -server"), arg, err)
				}
				return
			}
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			failRestartIf(t, err != nil, err)
			address, state := listener.Addr().String(), t.TempDir()+"/state"
			_ = listener.Close()
			t.Setenv("LOADGEN_HELPER", tc.mode)
			t.Setenv("LOADGEN_STATE", state)
			t.Setenv("RIBBITTO_ADDR", "192.0.2.1:1") // Must be overridden in the child.
			t.Setenv("GORACE", "atexit_sleep_ms=0")
			path, childArg := os.Args[0], "-test.run=^TestRestartHelper$"
			childArgs := []string{childArg, "-test.timeout=5s"}
			assertClosed := func() {
				t.Helper()
				listener, err := net.Listen("tcp", address)
				failRestartIf(t, err != nil, "child not cleaned up", err)
				_ = listener.Close()
			}
			var probes atomic.Int64
			probe := func(ctx context.Context, client *http.Client, endpoint string) (string, error) {
				probes.Add(1)
				r, err := request(ctx, client, "GET", endpoint, "secret", "")
				if err != nil {
					return "", err
				}
				defer func() { _ = r.Body.Close() }()
				if r.StatusCode != 200 {
					return "", fmt.Errorf("not ready")
				}
				return "7", nil
			}
			if tc.mode == "missing" {
				path = t.TempDir() + "/missing"
			}
			if tc.mode == "occupied" {
				listener, err := net.Listen("tcp", address)
				failRestartIf(t, err != nil, err)
				server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					_, _ = fmt.Fprint(w, `<div sse-connect="/events?after=7"></div>`)
				})}
				go func() { _ = server.Serve(listener) }()
				defer func() { _ = server.Close() }()
			}
			if tc.kind != "run" {
				child, err := launchChild(t.Context(), path, childArgs, address, "/channel", probe, 300*time.Millisecond, 150*time.Millisecond)
				if child != nil {
					defer func() { child.stop(time.Second); assertClosed() }()
				}
				if tc.failure != "" {
					failRestartIf(t, err == nil || !strings.Contains(err.Error(), tc.failure), fmt.Sprintf("accepted failed/occupied child: %v", err))
					if tc.mode == "occupied" {
						failRestartIf(t, probes.Load() != 0, "probed an unrelated listener")
						_, err = os.Stat(state)
						failRestartIf(t, !os.IsNotExist(err), "launched on an occupied address", err)
					} else {
						assertClosed()
					}
					return
				}
				failRestartIf(t, err != nil, err)
				if tc.kind == "handover" {
					assertHandover(t, child, address, func() (*childServer, error) {
						return launchChild(t.Context(), path, childArgs, address, "/channel", probe, time.Second, time.Second)
					})
					return
				}
				seconds, overrun := child.stop(150 * time.Millisecond)
				failRestartIf(t, overrun != (tc.mode == "kill") || seconds < .04 || seconds > 1 || child.cmd.ProcessState == nil || (overrun && seconds < .15), fmt.Sprintf("exit/reap: %f %t", seconds, overrun))
				seconds, overrun = child.stop(time.Millisecond)
				failRestartIf(t, seconds != 0 || overrun, "signaled a reaped child")
				return
			}
			tokens, receipts := t.TempDir()+"/tokens", t.TempDir()+"/receipts"
			if err := os.WriteFile(tokens, []byte(`{"organization_slug":"test","channel_ids":["one"],"accounts":[{"tokens":["secret","bad"]}]}`), 0600); err != nil {
				t.Fatal(err)
			}
			u, _ := url.Parse("http://" + address)
			forward := httputil.NewSingleHostReverseProxy(u)
			var postAttempts atomic.Int64
			proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == "POST" && (tc.mode == "lost" || tc.mode == "retry") {
					// Keep the request across the outage: retries must not race child startup.
					for {
						if _, err := os.Stat(state + "ready"); err == nil {
							break
						}
						if !waitRetry(r.Context(), time.Millisecond, 0) {
							return
						}
					}
					if tc.mode == "lost" {
						_ = waitRetry(r.Context(), 200*time.Millisecond, 0)
					}
					if tc.mode == "retry" && postAttempts.Add(1) == 1 {
						w.WriteHeader(503)
						return
					}
				}
				forward.ServeHTTP(w, r)
			}))
			defer proxy.Close()
			args := []string{"-server", path, "-server-addr", address, "-server-arg", childArg, "-server-arg", "-test.timeout=5s", "-target", proxy.URL, "-tokens", tokens, "-receipts", receipts, "-streams=2", "-restart-after=60ms", "-duration=200ms", "-drain=1s", "-start-deadline=1s", "-exit-deadline=1s", "-reconnect-delay=10ms", "-reconnect-jitter=0", "-post-attempts=3", "-metrics", u.String()}
			switch tc.mode {
			case "lost":
				args = append(args, "-rate=1", "-post-attempts=1")
			case "retry":
				args = append(args, "-rate=20")
			}
			var out bytes.Buffer
			err = run(args, &out)
			if tc.failure != "" {
				failRestartIf(t, err == nil || !strings.Contains(err.Error(), "restart: child "+tc.failure), err)
				assertClosed()
				return
			}
			failRestartIf(t, err != nil, err)
			assertClosed()
			var got result
			err = json.Unmarshal(out.Bytes(), &got)
			failRestartIf(t, err != nil, err)
			var file runFile
			raw, err := os.ReadFile(receipts)
			failRestartIf(t, err != nil || json.Unmarshal(raw, &file) != nil, "receipts", err)
			storm, incomplete := got.Restart, tc.mode == "partial" || tc.mode == "total"
			failRestartIf(t, storm == nil || storm.IncompleteSetup != incomplete || storm.Error != "" || storm.SIGTERM.IsZero() != incomplete, fmt.Sprintf("storm: %s", out.String()))
			if incomplete {
				failRestartIf(t, got.Established != map[string]uint64{"partial": 1, "total": 0}[tc.mode] || storm.Old != nil || storm.New != nil || got.DrainSeconds >= got.DrainLimitSeconds, out.String())
				return
			}
			recovery := uint64(7)
			if tc.mode == "held" || tc.mode == "unavailable" {
				recovery = 8
			}
			commits, _ := os.ReadFile(state + "commit")
			final := uint64(7 + bytes.Count(commits, []byte("\n")))
			failRestartIf(t, storm.RecoveryCursor != recovery || !storm.ReadyAt.After(storm.SIGTERM) || storm.ExitSeconds < .04 || storm.ReadySeconds <= storm.ExitSeconds || storm.Old == nil || storm.New == nil || storm.Old.Database.Queries != 1 || storm.New.Database.Queries != 2 || got.Server != nil || file.Header.FinalWatermark != final || got.DrainSeconds >= 1, out.String())
			failRestartIf(t, storm.Recovery == nil || storm.Recovery.Reconnected.IncompleteStreams != 0 || storm.Recovery.FullyCaughtUp.IncompleteStreams != 0, out.String())
			for _, rec := range file.Streams {
				failRestartIf(t, rec.Reconnects.Established < 1 || (file.Header.FinalWatermark > 7 && rec.Sequences[file.Header.FinalWatermark] == nil), "did not reconnect/drain", rec)
				if tc.mode == "unavailable" {
					failRestartIf(t, rec.Reconnects.Unavailable == 0, "no 503 during drain", rec)
				}
			}
			if tc.mode == "lost" || tc.mode == "retry" {
				if _, err := os.Stat(state + "commit"); err != nil || (tc.mode == "lost" && (final != 8 || got.PostFailed != 1 || got.Answered200 != 0)) || (tc.mode == "retry" && (final != 11 || got.Sent != 4 || got.PostAttemptsMade != 5 || got.Answered200 != 4 || got.Received != 8)) {
					t.Fatal("lost response/retry not covered", out.String(), err)
				}
			}
		})
	}
}

func assertHandover(t *testing.T, child *childServer, address string, launch func() (*childServer, error)) {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	c := &counts{handover: true, markers: regexp.MustCompile("loadgen")}
	records := []streamRecord{{Sequences: map[uint64]*receipt{}}}
	rec := &records[0]
	slots, done, opened := make(chan struct{}, 1), make(chan struct{}), make(chan struct{})
	slots <- struct{}{}
	go func() {
		stream(ctx, &http.Client{}, "http://"+address+"/events", "secret", "7", c, ctx, slots, func() { <-slots; close(opened) }, &reconnectModel{Delay: time.Millisecond}, rec)
		close(done)
	}()
	<-opened
	terminated := time.Now()
	c.mu.Lock()
	emptyComplete := caughtUp(records, 7, 7, terminated)
	c.mu.Unlock()
	failRestartIf(t, emptyComplete, "empty set completed before reconnect")
	child.stop(time.Second)
	child, err := launch()
	failRestartIf(t, err != nil, err)
	defer func() { child.stop(time.Second) }()
	err = os.WriteFile(os.Getenv("LOADGEN_STATE")+"drain", nil, 0600)
	failRestartIf(t, err != nil, err)
	deadline := time.Now().Add(time.Second)
	for {
		c.mu.Lock()
		complete := caughtUp(records, 7, 9, terminated)
		c.mu.Unlock()
		if complete || time.Now().After(deadline) {
			break
		}
		time.Sleep(time.Millisecond)
	}
	cancel()
	<-done
	failRestartIf(t, len(rec.EstablishedAt) < 2 || !rec.EstablishedAt[1].After(terminated) || len(rec.ArrivedAt[8]) == 0 || !rec.ArrivedAt[8][0].After(child.readyAt) || !caughtUp(records, 7, 9, terminated) || rec.Sequences[9] != nil || len(rec.ArrivedAt[9]) == 0, fmt.Sprintf("handover: %+v", rec))
	restart := restartResult{SIGTERM: terminated, RecoveryCursor: 8}
	restart.measureRecovery(records, 7, 9, time.Now(), time.Hour)
	failRestartIf(t, restart.Recovery.DeliveriesThroughRecoveryCursorAfterReconnect != 1 || restart.Recovery.AllDeliveriesAfterReconnect != 2 || restart.Recovery.OutageRecovered.IncompleteStreams != 0 || restart.Recovery.FullyCaughtUp.IncompleteStreams != 0, restart.Recovery)
	rec.Sequences = nil
	failRestartIf(t, !caughtUp(records, 8, 8, terminated), "empty set did not complete after reconnect")
	rec.Reset++
	rec.EstablishedAt, rec.ArrivedAt = nil, nil
	failRestartIf(t, !caughtUp(records, 8, 8, terminated), "lone reset blocked drain")
	failRestartIf(t, !caughtUp(append(records, streamRecord{EstablishedAt: []time.Time{time.Now()}, ArrivedAt: map[uint64][]time.Time{9: {time.Now()}}}), 8, 9, terminated), "reset blocked caught-up stream")
}

func failRestartIf(t *testing.T, failed bool, details ...any) {
	t.Helper()
	if failed {
		t.Fatal(details...)
	}
}
