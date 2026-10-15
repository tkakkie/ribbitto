package main

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/launcher"
	"github.com/go-rod/rod/lib/proto"
)

// launchBrowser starts the installed Chrome for one test, its process bound
// to ctx. A missing or unlaunchable browser skips the test locally and fails
// it with RIBBITTO_REQUIRE_BROWSER=1, as CI sets; what names the checks in
// the skip message.
func launchBrowser(t *testing.T, ctx context.Context, what string) *rod.Browser {
	t.Helper()
	unavailable := func(reason string) {
		t.Helper()
		if os.Getenv("RIBBITTO_REQUIRE_BROWSER") == "1" {
			t.Fatal(reason)
		}
		t.Skip(reason)
	}
	bin, found := launcher.LookPath()
	if !found {
		unavailable("no installed browser; Claude runs " + what + " locally")
	}
	launch := launcher.New().Bin(bin).UserDataDir(t.TempDir()).Leakless(false).Context(ctx)
	control, err := launch.Launch()
	if err != nil {
		unavailable(err.Error())
	}
	t.Cleanup(launch.Kill)
	browser := rod.New().ControlURL(control).MustConnect().MustIgnoreCertErrors(true)
	t.Cleanup(func() { browser.Timeout(5 * time.Second).MustClose() })
	return browser
}

// boundedPage creates a page within 15 s, failing by name, and returns it on
// ctx, failing the test by name on any panic, with a close that has its own
// 10 s, so neither creation nor cleanup can wait on the whole test's deadline
// (#1060). Close it before the test server: an open page keeps its stream
// request alive. A copy of internal/web's: test code is not shared between
// packages.
func boundedPage(t *testing.T, browser *rod.Browser, ctx context.Context, what string) (*rod.Page, func()) {
	t.Helper()
	// The page keeps the context it was created with (rod's Mouse and
	// Keyboard use it), so it is created with ctx and the wait is bounded
	// here instead.
	type created struct {
		page *rod.Page
		err  error
	}
	done := make(chan created, 1)
	go func() {
		page, err := browser.Context(ctx).Page(proto.TargetCreateTarget{})
		done <- created{page, err}
	}()
	timer := time.NewTimer(15 * time.Second)
	defer timer.Stop()
	var page *rod.Page
	select {
	case c := <-done:
		if c.err != nil {
			t.Fatalf("%s: creating the page: %v", what, c.err)
		}
		page = c.page
	case <-timer.C:
		// A page created after the timeout would stay open in a browser later
		// subtests share, so it is closed when it arrives.
		go func() {
			if c := <-done; c.err == nil {
				closeCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer cancel()
				_ = c.page.Context(closeCtx).Close()
			}
		}()
		t.Fatalf("%s: creating the page did not finish within 15 s", what)
	}
	return page.WithPanic(func(v any) { t.Fatalf("%s: %v", what, v) }), func() {
		closeCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := page.Context(closeCtx).Close(); err != nil {
			t.Errorf("%s: closing the page: %v", what, err)
		}
	}
}

// inputBound is how long one named input step may take.
const inputBound = 10 * time.Second

// inputStep runs fn, which sends Mouse, Keyboard or Touch input to page,
// within inputBound and fails naming what if it panics or does not return.
// rod dispatches that input through the page's creation context, not the
// context of the page passed in, so the wait is bounded here, outside fn.
func inputStep(t *testing.T, page *rod.Page, what string, fn func(p *rod.Page)) {
	t.Helper()
	ctx, cancel := context.WithTimeout(page.GetContext(), inputBound)
	defer cancel()
	started := time.Now()
	done := make(chan any, 1)
	go func() {
		defer func() { done <- recover() }()
		fn(page.Context(ctx).WithPanic(func(v any) { panic(v) }))
	}()
	select {
	case v := <-done:
		if v != nil {
			t.Fatalf("%s: %v (after %s)", what, v, time.Since(started).Round(time.Millisecond))
		}
	case <-ctx.Done():
		t.Fatalf("%s: did not finish within %s (after %s)", what, inputBound, time.Since(started).Round(time.Millisecond))
	}
}
