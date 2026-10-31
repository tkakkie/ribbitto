package web

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/cdp"
	"github.com/go-rod/rod/lib/proto"
)

// isTargetNavigated identifies only the protocol error from a replaced document.
func isTargetNavigated(err error) bool {
	var protocolErr *cdp.Error
	return errors.As(err, &protocolErr) && protocolErr != nil && protocolErr.Code == -32000 &&
		protocolErr.Message == "Inspected target navigated or closed"
}

// waitAcrossNavigation handles a poll in flight when its document is replaced.
// Rod's Evaluate retries only cdp.ErrCtxNotFound; Chrome 156 evidence in #1987
// shows that an in-flight poll instead gets the navigated-or-closed error.
// #1251 covered only calls that start a navigation. Repeating Wait lets Evaluate
// discard its cached window on the subsequent context-not-found error.
// The predicate and arguments run unchanged: only this one error is retried,
// with 5 retries in total per wait call (the count is not reset within the call),
// never a wrong answer or a timeout, so no check is loosened.
func waitAcrossNavigation(page *rod.Page, predicate string, args ...any) error {
	for retries := 0; ; retries++ {
		err := page.Wait(rod.Eval(predicate, args...))
		if !isTargetNavigated(err) || retries == 5 {
			return err
		}
	}
}

// mustWaitAcrossNavigation keeps the failing wait's test location visible.
func mustWaitAcrossNavigation(t *testing.T, page *rod.Page, predicate string) {
	t.Helper()
	if err := waitAcrossNavigation(page, predicate); err != nil {
		file, line := browserTestCallSite()
		t.Fatalf("waiting for %s: %v (at %s:%d)", predicate, err, file, line)
	}
}

// TestIsTargetNavigated guards the narrow protocol-error match, including wrapping.
func TestIsTargetNavigated(t *testing.T) {
	navigated := &cdp.Error{Code: -32000, Message: "Inspected target navigated or closed"}
	for _, test := range []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"exact", navigated, true},
		{"wrapped", fmt.Errorf("polling: %w", navigated), true},
		{"context-not-found", cdp.ErrCtxNotFound, false},
		{"another-message", &cdp.Error{Code: -32000, Message: "another error"}, false},
		{"non-cdp", errors.New("Inspected target navigated or closed"), false},
		{"another-code", &cdp.Error{Code: -32001, Message: navigated.Message}, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := isTargetNavigated(test.err); got != test.want {
				t.Fatalf("isTargetNavigated(%v) = %t, want %t", test.err, got, test.want)
			}
		})
	}
}

// testWaitNavigationBrowser holds a poll in the old document across navigation.
func testWaitNavigationBrowser(t *testing.T, browser *rod.Browser) {
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	started := make(chan struct{}, 16)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/poll-started" {
			select {
			case started <- struct{}{}:
			default:
			}
			w.WriteHeader(http.StatusNoContent)
			return
		}
		n, err := strconv.Atoi(strings.TrimPrefix(r.URL.Path, "/p/"))
		if err != nil || !strings.HasPrefix(r.URL.Path, "/p/") {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = fmt.Fprintf(w, `<!doctype html><html><head><title>Wait navigation</title></head><body data-page="%d"></body></html>`, n)
	}))
	defer server.Close()
	page := browser.MustPage().Context(ctx)
	defer page.MustClose()
	page.MustActivate()
	const busy = `(n) => { navigator.sendBeacon('/poll-started'); const end = Date.now() + 400; while (Date.now() < end) {} return document.body.dataset.page === n }`
	var plainOutcomes []string
	plainHits := 0
	for i := range 5 {
		for modeIndex, mode := range []string{"plain", "helper"} {
			k := i*4 + modeIndex*2
			next := strconv.Itoa(k + 1)
			if err := page.Navigate(fmt.Sprintf("%s/p/%d", server.URL, k)); err != nil {
				t.Fatalf("round %d %s initial navigation: %v", i+1, mode, err)
			}
			if err := page.WaitLoad(); err != nil {
				t.Fatalf("round %d %s initial load: %v", i+1, mode, err)
			}
		drainStarted:
			for {
				select {
				case <-started:
				default:
					break drainStarted
				}
			}
			done := make(chan error, 1)
			go func() {
				if mode == "plain" {
					done <- page.Wait(rod.Eval(busy, next))
				} else {
					done <- waitAcrossNavigation(page, busy, next)
				}
			}()
			// A fixed delay can lose the race under CI load; the beacon signals
			// an in-flight poll without needing help from the busy page.
			select {
			case <-started:
			case <-time.After(10 * time.Second):
				t.Fatalf("round %d %s: the poll never announced it was in flight", i+1, mode)
			case <-ctx.Done():
				t.Fatalf("round %d %s: the poll never announced it was in flight", i+1, mode)
			}
			if _, err := (proto.PageNavigate{URL: server.URL + "/p/" + next}).Call(page); err != nil {
				t.Fatalf("round %d %s replacement navigation: %v", i+1, mode, err)
			}
			var err error
			select {
			case err = <-done:
			case <-ctx.Done():
				t.Fatalf("round %d %s wait did not finish: %v", i+1, mode, ctx.Err())
			}
			if mode == "plain" {
				plainOutcomes = append(plainOutcomes, fmt.Sprint(err))
				if isTargetNavigated(err) {
					plainHits++
				} else if err != nil {
					t.Fatalf("round %d plain wait: %v", i+1, err)
				}
			} else {
				if err != nil {
					t.Fatalf("round %d helper wait: %v", i+1, err)
				}
				if got := page.MustEval(`() => document.body.dataset.page`).Str(); got != next {
					t.Fatalf("round %d helper page = %q, want %q", i+1, got, next)
				}
			}
		}
	}
	if plainHits < 1 {
		t.Fatalf("the race is made deterministic: plain Wait fails; got hits=%d/5 outcomes=%q", plainHits, plainOutcomes)
	}
}
