package web

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/proto"
)

// failAtTestLine preserves the Must call's location, which testing otherwise
// attributes to this shared WithPanic callback.
func failAtTestLine(t *testing.T) func(any) {
	t.Helper()
	return func(v any) {
		t.Helper()
		file, line := browserTestCallSite()
		t.Fatalf("%v (at %s:%d)", v, file, line)
	}
}

func browserTestCallSite() (string, int) {
	var pcs [64]uintptr
	frames := runtime.CallersFrames(pcs[:runtime.Callers(2, pcs[:])])
	for {
		frame, more := frames.Next()
		if strings.HasSuffix(frame.File, "_test.go") && !strings.Contains(frame.Function, ".failAtTestLine.") &&
			!strings.HasSuffix(frame.Function, ".mustWaitAcrossNavigation") {
			return filepath.Base(frame.File), frame.Line
		}
		if !more {
			return "unknown_test.go", 0
		}
	}
}

func TestBrowserTestCallSite(t *testing.T) {
	_, callerFile, callerLine, _ := runtime.Caller(0)
	file, line := browserTestCallSite()
	if file != filepath.Base(callerFile) || line != callerLine+1 {
		t.Fatalf("call site = %s:%d, want %s:%d", file, line, filepath.Base(callerFile), callerLine+1)
	}
}

// runBounded runs fn until ctx ends and fails naming what, with the time since
// started, so a stalled browser action says where it stalled instead of
// exhausting the whole test's deadline (#1060; boundedPage does the same for
// a tab). fn runs in a goroutine abandoned when ctx ends: rod's Mouse,
// Keyboard and Touch use the context the page was created with, not the one
// a caller gives it, and a click into a collapsed list once waited for the
// whole test's deadline (#1056).
func runBounded(t *testing.T, ctx context.Context, started time.Time, what string, fn func()) {
	t.Helper()
	done := make(chan any, 1)
	go func() {
		defer func() { done <- recover() }()
		fn()
	}()
	select {
	case v := <-done:
		if v != nil {
			t.Fatalf("%s: %v (at %s)", what, v, time.Since(started).Round(time.Millisecond))
		}
	case <-ctx.Done():
		t.Fatalf("%s did not finish (at %s)", what, time.Since(started).Round(time.Millisecond))
	}
}

// pageStep returns a step that runs one named browser action and its waits
// on tab under their own 10 s and fails by name.
func pageStep(ctx context.Context, tab *rod.Page) func(t *testing.T, what string, fn func(p *rod.Page)) {
	started := time.Now()
	return func(t *testing.T, what string, fn func(p *rod.Page)) {
		t.Helper()
		stepCtx, stop := context.WithTimeout(ctx, 10*time.Second)
		defer stop()
		runBounded(t, stepCtx, started, what, func() { fn(tab.Context(stepCtx).WithPanic(func(v any) { panic(v) })) })
	}
}

// stepRunner runs a test as a sequence of named steps, for tests whose pages
// and helpers outlive one step: each step gets a context of its own bound
// from the test's ctx, which the test puts on its pages, and the step's name
// labels every failure until the next one starts.
type stepRunner struct {
	t       *testing.T
	ctx     context.Context
	bound   time.Duration
	started time.Time
	// current names the running step; before the first it is "setup".
	current string
	stepCtx context.Context
	cancel  context.CancelFunc
}

// newStepRunner starts the clock; until the first step, work runs on ctx.
func newStepRunner(t *testing.T, ctx context.Context, bound time.Duration) *stepRunner {
	s := &stepRunner{t: t, ctx: ctx, bound: bound, started: time.Now(), current: "setup", stepCtx: ctx, cancel: func() {}}
	t.Cleanup(func() { s.cancel() })
	return s
}

// next ends the running step, starts the one named what and returns its
// context, for the test's pages.
func (s *stepRunner) next(what string) context.Context {
	s.cancel()
	s.current = what
	s.stepCtx, s.cancel = context.WithTimeout(s.ctx, s.bound)
	return s.stepCtx
}

// since is the time since the runner started, for failure messages.
func (s *stepRunner) since() time.Duration {
	return time.Since(s.started).Round(time.Millisecond)
}

// fail is a page's panic handler: it fails naming the running step.
func (s *stepRunner) fail(v any) {
	s.t.Fatalf("%s: %v (at %s)", s.current, v, s.since())
}

// device runs mouse, keyboard or touch work on page under the running step's
// bound, failing with the step's name and what.
func (s *stepRunner) device(page *rod.Page, what string, fn func(p *rod.Page)) {
	s.t.Helper()
	p := page.Context(s.stepCtx).WithPanic(func(v any) { panic(v) })
	runBounded(s.t, s.stepCtx, s.started, s.current+": "+what, func() { fn(p) })
}

// navigateLoaded navigates p to url and waits up to 8 s until the new
// document is complete, for tabs with script execution disabled: rod's
// WaitLoad waits on a load listener in page JavaScript, which never runs
// there, so it hangs if the load is still pending (#1129). The test's own
// evaluation still runs, and once Page.navigate has returned it reads the
// destination document, not the one before.
func navigateLoaded(p *rod.Page, url string) error {
	ctx, stop := context.WithTimeoutCause(p.GetContext(), 8*time.Second, errors.New("not loaded within 8 s"))
	defer stop()
	p = p.Context(ctx)
	if err := p.Navigate(url); err != nil {
		return fmt.Errorf("navigating to %s: %w", url, err)
	}
	if err := p.Wait(rod.Eval(`() => document.readyState === 'complete'`)); err != nil {
		if ctx.Err() != nil {
			err = context.Cause(ctx)
		}
		return fmt.Errorf("loading %s: %w", url, err)
	}
	return nil
}

// deleteLangCookie removes the language cookie a test set, under its own 10 s.
// The cookie is host-wide in the shared browser and outranks Accept-Language,
// so later tests must not inherit it.
func deleteLangCookie(t *testing.T, tab *rod.Page, url string) {
	t.Helper()
	cleanupCtx, stop := context.WithTimeout(context.Background(), 10*time.Second)
	defer stop()
	if err := (proto.NetworkDeleteCookies{Name: "lang", URL: url}).Call(tab.Context(cleanupCtx)); err != nil {
		t.Errorf("deleting the lang cookie: %v", err)
	}
}

// idleStream is an organisation stream that sends its heartbeat and then
// nothing until the request ends, so a page's stream and reading scripts run
// without any event arriving.
func idleStream(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/event-stream")
	sender := sseSender{w: w, rc: http.NewResponseController(w), timeout: time.Second}
	if sender.Heartbeat(r.Context()) == nil {
		<-r.Context().Done()
	}
}
