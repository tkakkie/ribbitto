package web

// Temporary #1444 marker timing, read before the page cleanup on failures.
import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/go-rod/rod"
)

func diagMarkerDelivery(t *testing.T) func(string, ...any) {
	var mu sync.Mutex
	origin := time.Now()
	var ring []string
	t.Cleanup(func() {
		if t.Failed() {
			mu.Lock()
			defer mu.Unlock()
			t.Logf("DIAG-marked delivery origin=%s: %q", origin.UTC().Format(time.RFC3339Nano), ring)
		}
	})
	return func(format string, args ...any) {
		mu.Lock()
		defer mu.Unlock()
		ring = append(ring, fmt.Sprintf("+%s %s", time.Since(origin).Round(time.Microsecond), fmt.Sprintf(format, args...)))
		if len(ring) > 160 {
			ring = ring[len(ring)-160:]
		}
	}
}

func diagMarkerPage(t *testing.T, p *rod.Page, b *rod.Browser) {
	t.Helper()
	p.WithPanic(func(v any) { t.Fatal(v) }).MustEvalOnNewDocument(`(() => {
  window.markerTrace=[];
  const record=(kind)=>{markerTrace.push({at:performance.now(),wall:Date.now(),kind,opens:window.opens,previous:document.querySelector('#sidebar-channels')===window.previousSidebar,markers:[...document.querySelectorAll('[data-draft-marker]')].map(m=>[m.closest('a').id,m.hidden])}); if(markerTrace.length>40) markerTrace.shift();};
  for(const type of ['pageshow','htmx:load','htmx:sseOpen','htmx:sseBeforeMessage','htmx:sseMessage','htmx:beforeRequest','htmx:afterRequest'])
   window.addEventListener(type,e=>{record(type+':'+(e.target.id||'window')+':'+(e.detail?.type||'')); queueMicrotask(()=>record('after:'+type));});
  document.addEventListener('DOMContentLoaded',()=>new MutationObserver(()=>record('mutation')).observe(document.getElementById('organization-stream'),{childList:true,subtree:true,attributes:true,attributeFilter:['hidden']}));
 })()`)
	t.Cleanup(func() {
		if !t.Failed() {
			return
		}
		value, err := p.Context(context.Background()).Timeout(time.Second).Eval(`() => JSON.stringify(window.markerTrace)`)
		t.Logf("MARKER TRACE: %v error=%v", value, err)
		d := b.GetContext().Value(diagKey{}).(*diagCDP)
		d.mu.Lock()
		t.Logf("AX/CDP TRACE origin=%s events=%q", d.origin.UTC().Format(time.RFC3339Nano), d.ring)
		d.mu.Unlock()
	})
}
