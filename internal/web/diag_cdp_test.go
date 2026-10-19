package web

// Temporary #1444 instrumentation. Delete this file and the DIAG hooks together.
import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"runtime/debug"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/cdp"
)

type diagKey struct{}
type diagProbeBudget struct{}
type diagCDP struct {
	*cdp.Client
	mu                     sync.Mutex
	origin                 time.Time
	previous               string
	budget                 context.Context
	ring, errors, failures []string
	kinds                  []string
	ignoredContexts        int
}

type diagOutcome struct {
	detail          string
	kinds           []string
	failed          bool
	ignoredContexts int
}

func diagConnect(t *testing.T, url string, ctx context.Context, kill func()) *rod.Browser {
	t.Helper()
	client, err := cdp.StartWithURL(context.Background(), url, nil)
	if err != nil {
		t.Fatal(err)
	}
	d := &diagCDP{Client: client, origin: time.Now()}
	if ctx.Value(diagProbeBudget{}) != nil {
		d.budget = ctx
		// A failed page close must not leave httptest.Close waiting on SSE.
		stop := context.AfterFunc(ctx, kill)
		t.Cleanup(func() { stop() })
	}
	b := rod.New().Client(d)
	if err := b.Connect(); err != nil {
		t.Fatal(err)
	}
	return b.Context(context.WithValue(b.GetContext(), diagKey{}, d))
}

func (d *diagCDP) record(line string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.ring = append(d.ring, fmt.Sprintf("+%s %s", time.Since(d.origin).Round(time.Microsecond), line))
	if len(d.ring) > 64 {
		d.ring = d.ring[len(d.ring)-64:]
	}
}

func (d *diagCDP) Event() <-chan *cdp.Event {
	dst := make(chan *cdp.Event)
	go func() {
		defer close(dst)
		for e := range d.Client.Event() {
			if strings.HasPrefix(e.Method, "Target.") || strings.HasPrefix(e.Method, "Page.") || strings.HasPrefix(e.Method, "Runtime.executionContext") {
				d.record(fmt.Sprintf("event session=%s %s %s", e.SessionID, e.Method, e.Params))
			}
			dst <- e
		}
	}()
	return dst
}

func (d *diagCDP) targets() string {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	data, err := d.Client.Call(ctx, "", "Target.getTargets", struct{}{})
	return fmt.Sprintf("targets=%s error=%v", data, err)
}

func (d *diagCDP) Call(ctx context.Context, session, method string, params interface{}) ([]byte, error) {
	start := time.Since(d.origin)
	// Log navigation commands before sending, to order them against events.
	if strings.HasPrefix(method, "Page.navigate") || method == "Page.reload" || strings.HasPrefix(method, "Target.") || strings.HasPrefix(method, "Accessibility.") {
		data, _ := json.Marshal(params)
		d.record(fmt.Sprintf("call session=%s %s %s", session, method, data))
	}
	res, err := d.Client.Call(ctx, session, method, params)
	if strings.HasPrefix(method, "Accessibility.") {
		d.record(fmt.Sprintf("response session=%s %s %s error=%v", session, method, res, err))
	}
	if errors.Is(err, cdp.ErrCtxNotFound) {
		// Rod's Evaluate polls through this ordinary context-replacement
		// response. Count it without drowning the actionable errors.
		d.mu.Lock()
		d.ignoredContexts++
		d.mu.Unlock()
	} else if err != nil {
		data, _ := json.Marshal(params)
		line := fmt.Sprintf("call +%s..+%s session=%s %s params=%s error=%v", start.Round(time.Microsecond), time.Since(d.origin).Round(time.Microsecond), session, method, data, err)
		if d.budget != nil && d.budget.Err() != nil {
			line += " (PROBE BUDGET: browser kill requested)"
		}
		d.mu.Lock()
		d.errors = append(d.errors, line)
		d.kinds = append(d.kinds, method+": "+err.Error())
		if strings.Contains(err.Error(), "Inspected target navigated or closed") {
			d.failures = append(d.failures, strings.Join(d.ring, "\n")+"\n"+line+"\n"+string(debug.Stack()))
		}
		d.mu.Unlock()
		if strings.Contains(err.Error(), "Inspected target navigated or closed") {
			targets := d.targets()
			d.mu.Lock()
			d.failures = append(d.failures, "AT ERROR "+targets)
			d.mu.Unlock()
		}
	}
	if err == nil && method == "Page.enable" {
		// Lifecycle collection is observational; no waiting or navigation changes.
		_, enableErr := d.Client.Call(ctx, session, "Page.setLifecycleEventsEnabled", map[string]bool{"enabled": true})
		d.record(fmt.Sprintf("lifecycle session=%s enabled error=%v", session, enableErr))
	}
	return res, err
}

func diagScenario(t *testing.T, b *rod.Browser, report func(string)) *rod.Browser {
	t.Helper()
	return diagScenarioResult(t, b, func(out diagOutcome) { report(out.detail) })
}

func diagScenarioResult(t *testing.T, b *rod.Browser, report func(diagOutcome)) *rod.Browser {
	t.Helper()
	d := b.GetContext().Value(diagKey{}).(*diagCDP)
	before := d.targets()
	d.mu.Lock()
	previous := d.previous
	d.errors = nil
	d.failures = nil
	d.kinds = nil
	d.ignoredContexts = 0
	d.mu.Unlock()
	d.record("BEGIN " + t.Name() + " predecessor=" + previous + " " + before)
	// The pool release was registered first, so this runs after page cleanup.
	t.Cleanup(func() {
		after := d.targets()
		d.mu.Lock()
		summary := fmt.Sprintf("%s browser=%p origin=%s predecessor=%q failed=%t ignored ErrCtxNotFound=%d\n  before %s\n  after %s\n  CDP errors: %s", t.Name(), d, d.origin.UTC().Format(time.RFC3339Nano), previous, t.Failed(), d.ignoredContexts, before, after, strings.Join(d.errors, "\n"))
		if len(d.failures) > 0 {
			summary += "\nAT FAILURE:\n" + strings.Join(d.failures, "\n") + "\nAFTER FAILURE:\n" + strings.Join(d.ring, "\n")
		} else if t.Failed() {
			summary += "\nRECENT EVENTS:\n" + strings.Join(d.ring, "\n")
		}
		d.previous = t.Name()
		out := diagOutcome{detail: summary, kinds: append([]string(nil), d.kinds...), failed: t.Failed(), ignoredContexts: d.ignoredContexts}
		d.mu.Unlock()
		report(out)
	})
	return b.WithPanic(func(v any) { t.Fatal(v) })
}
