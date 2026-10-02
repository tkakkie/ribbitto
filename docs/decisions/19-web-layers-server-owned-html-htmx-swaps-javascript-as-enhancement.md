# 19. Web layers: server-owned HTML, htmx swaps, JavaScript as enhancement

**Decided:** core functionality works without JavaScript; JavaScript is
progressive enhancement. Handlers convert use-case results into view
models, and templ components never receive `app` types (pure `domain`
values are fine). htmx only requests and swaps; the server and templ own
the HTML. Pages are full pages, and M3's SSE units are explicit templ
fragments. JavaScript does not generate HTML, own application state or
make requests. It stores only non-sensitive UX settings, and does
focus, scroll, keyboard, local time and SSE glue. Details, checks and the
audit: [`web-layers.md`](../architecture/web-layers.md) and
[`web-layers-checks.md`](../architecture/web-layers-checks.md).
**Why:** M3 adds SSE fragments and more scripts across exactly these
boundaries; without written rules each change would pick its own (#193).
**Considered:** full JavaScript parity (not needed for enhancements such as
real-time updates); a headless-browser test dependency such as go-rod
(deferred until after M3, if needed); a client-side framework (`DECISIONS.md`
2).
