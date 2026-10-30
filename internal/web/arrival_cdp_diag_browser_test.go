// TEMPORARY diagnostic for #1987; removed before the final head.
package web

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/cdp"
	"github.com/go-rod/rod/lib/proto"
	"github.com/go-rod/rod/lib/utils"
)

func testArrivalCDPDiag(t *testing.T, browser *rod.Browser) {
	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	defer cancel()
	var report strings.Builder
	defer func() { t.Errorf("DIAGNOSTIC REPORT #1987:\n%s", report.String()) }()
	version, err := (proto.BrowserGetVersion{}).Call(browser.Context(ctx))
	fmt.Fprintf(&report, "Chrome version: %+v; %s\n", version, arrivalCDPDiagError(err))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n, err := strconv.Atoi(strings.TrimPrefix(r.URL.Path, "/p/"))
		if err != nil || !strings.HasPrefix(r.URL.Path, "/p/") {
			http.NotFound(w, r)
			return
		}
		time.Sleep(time.Duration(rand.IntN(26)) * time.Millisecond)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = fmt.Fprintf(w, `<!doctype html><html><head><title>CDP diagnostic</title></head><body><p>Page %d</p><a id="next" href="/p/%d">Next</a><script>window.leaving = false;</script></body></html>`, n, n+1)
	}))
	defer server.Close()
	page := browser.MustPage().Context(ctx)
	defer page.MustClose()
	page.MustActivate()

	func() {
		partCtx, stop := context.WithTimeout(ctx, 30*time.Second)
		defer stop()
		p := page.Context(partCtx)
		completed := 0
		defer func() { fmt.Fprintf(&report, "A: completed=%d/5; budget=30s; context=%v\n", completed, partCtx.Err()) }()
		if err := p.Navigate(server.URL + "/p/0"); err != nil {
			fmt.Fprintf(&report, "A initial navigation: %s\n", arrivalCDPDiagError(err))
			return
		}
		for i := 1; i <= 5 && partCtx.Err() == nil; i++ {
			if err := p.Wait(rod.Eval(`() => document.readyState === 'complete'`)); err != nil {
				fmt.Fprintf(&report, "A %d ready: %s\n", i, arrivalCDPDiagError(err))
				break
			}
			probeCtx, stopProbe := context.WithTimeout(partCtx, 10*time.Second)
			done := make(chan error, 1)
			go func() {
				_, err := p.Context(probeCtx).Evaluate(rod.Eval(`() => new Promise(() => {})`).ByPromise())
				done <- err
			}()
			select {
			case <-time.After(300 * time.Millisecond):
			case <-probeCtx.Done():
			}
			_, returned, clickErr := arrivalCDPDiagClick(p.Context(probeCtx))
			var callErr error
			collected := false
			select {
			case callErr = <-done:
				collected = true
			case <-probeCtx.Done():
				callErr = probeCtx.Err()
			}
			stopProbe()
			completed++
			fmt.Fprintf(&report, "A %d: call_returned=%t %s; click_returned=%t %s\n", i, collected, arrivalCDPDiagError(callErr), returned, arrivalCDPDiagError(clickErr))
			if clickErr != nil {
				break
			}
		}
	}()

	for _, variant := range []string{"default", "tight"} {
		partCtx, stop := context.WithTimeout(ctx, 25*time.Second)
		p := page.Context(partCtx)
		if variant == "tight" {
			p = p.Sleeper(func() utils.Sleeper { return utils.BackoffSleeper(time.Millisecond, 5*time.Millisecond, nil) })
		}
		arrivalCDPDiagStress(p, server.URL, variant, &report)
		stop()
	}
	fmt.Fprintln(&report, "C: omitted to keep this temporary diagnostic small.")
}

func arrivalCDPDiagStress(p *rod.Page, base, variant string, report *strings.Builder) {
	const predicate = `() => !window.leaving && document.readyState === 'complete'`
	counts := map[string]int{"nil": 0, "navigated-or-closed": 0, "ctx-not-found": 0, "other": 0}
	var durations []time.Duration
	recoveries, recovered := 0, 0
	defer func() {
		fmt.Fprintf(report, "B %s: iterations=%d/150; budget=25s; counts=%v; recovery=%d/%d; context=%v\n", variant, len(durations), counts, recovered, recoveries, p.GetContext().Err())
		if len(durations) > 0 {
			slices.Sort(durations)
			fmt.Fprintf(report, "B %s wait duration min/median/max: %s / %s / %s\n", variant, durations[0], durations[len(durations)/2], durations[len(durations)-1])
		}
	}()
	if err := p.Navigate(base + "/p/0"); err != nil {
		fmt.Fprintf(report, "B %s initial navigation: %s\n", variant, arrivalCDPDiagError(err))
		return
	}
	if err := p.Wait(rod.Eval(`() => document.readyState === 'complete'`)); err != nil {
		fmt.Fprintf(report, "B %s initial ready: %s\n", variant, arrivalCDPDiagError(err))
		return
	}
	for i := 1; i <= 150 && p.GetContext().Err() == nil; i++ {
		_, evalErr := p.Eval(`() => { window.leaving = true; }`)
		if evalErr != nil {
			fmt.Fprintf(report, "B %s %d leaving Eval: %s\n", variant, i, arrivalCDPDiagError(evalErr))
		}
		clicked, clickReturned, clickErr := arrivalCDPDiagClick(p)
		if clickErr != nil {
			fmt.Fprintf(report, "B %s %d click: %s\n", variant, i, arrivalCDPDiagError(clickErr))
			if !clickReturned {
				return
			}
			if err := p.Navigate(fmt.Sprintf("%s/p/%d", base, i)); err != nil {
				fmt.Fprintf(report, "B %s %d fallback navigation: %s\n", variant, i, arrivalCDPDiagError(err))
			}
		}
		started := time.Now()
		err := p.Wait(rod.Eval(predicate))
		duration := time.Since(started)
		sinceClick := time.Since(clicked)
		class := arrivalCDPDiagClass(err)
		durations = append(durations, duration)
		counts[class]++
		if class == "navigated-or-closed" {
			// No intervening browser call or sleep: test whether just repeating Wait recovers.
			retryStarted := time.Now()
			retryErr := p.Wait(rod.Eval(predicate))
			retryDuration := time.Since(retryStarted)
			recoveries++
			if retryErr == nil {
				recovered++
			}
			// Click's preparatory Eval calls finish before it dispatches mouse input.
			fmt.Fprintf(report, "B %s %d: since_click=%s click_returned=%t click_eval_returned=%t leaving_eval_returned=true; %s; recovery_nil=%t duration=%s %s\n", variant, i, sinceClick, clickReturned, clickReturned, arrivalCDPDiagError(err), retryErr == nil, retryDuration, arrivalCDPDiagError(retryErr))
			err = retryErr
		} else if err != nil {
			fmt.Fprintf(report, "B %s %d wait: %s\n", variant, i, arrivalCDPDiagError(err))
		}
		if err != nil {
			if readyErr := p.Wait(rod.Eval(`() => document.readyState === 'complete'`)); readyErr != nil {
				fmt.Fprintf(report, "B %s %d ready: %s\n", variant, i, arrivalCDPDiagError(readyErr))
				return
			}
		}
	}
}

func arrivalCDPDiagClick(p *rod.Page) (time.Time, bool, error) {
	el, err := p.Element("#next")
	started := time.Now()
	if err != nil {
		return started, true, err
	}
	// Mouse keeps its original page context; bound the caller even on a context clone.
	done := make(chan error, 1)
	go func() { done <- el.Click(proto.InputMouseButtonLeft, 1) }()
	select {
	case err := <-done:
		return started, true, err
	case <-p.GetContext().Done():
		return started, false, p.GetContext().Err()
	}
}

func arrivalCDPDiagClass(err error) string {
	var protocolErr *cdp.Error
	switch {
	case err == nil:
		return "nil"
	case errors.As(err, &protocolErr) && protocolErr.Message == "Inspected target navigated or closed":
		return "navigated-or-closed"
	case errors.Is(err, cdp.ErrCtxNotFound):
		return "ctx-not-found"
	default:
		return "other"
	}
}

func arrivalCDPDiagError(err error) string {
	detail := fmt.Sprintf("class=%s type=%T text=%q ctx_not_found=%t", arrivalCDPDiagClass(err), err, fmt.Sprint(err), errors.Is(err, cdp.ErrCtxNotFound))
	var protocolErr *cdp.Error
	if errors.As(err, &protocolErr) {
		detail += fmt.Sprintf(" cdp_code=%d cdp_message=%q", protocolErr.Code, protocolErr.Message)
	}
	return detail
}
