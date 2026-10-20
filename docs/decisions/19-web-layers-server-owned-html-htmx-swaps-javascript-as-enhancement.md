# 19. Web layers: server-owned HTML, htmx swaps, JavaScript as enhancement

**Decided:** core functionality works without JavaScript; JavaScript is
progressive enhancement. Handlers convert use-case results into view
models, and templ components never receive `app` types (pure `domain`
values are fine). htmx only requests and swaps; the server and templ own
the HTML. Pages are full pages, and M3's SSE units are explicit templ
fragments. JavaScript does not generate HTML, own application state or
make requests. It stores only non-sensitive UX settings, and does
focus, scroll, keyboard, local time and SSE glue. Details, checks and the
audit: [`web-layers.md`](../architecture/web-layers.md),
[`web-javascript.md`](../architecture/web-javascript.md) and
[`web-layers-checks.md`](../architecture/web-layers-checks.md).

**Current exceptions:**
[decision 34](34-live-unread-counts.md) allows one script to own sidebar badge
numbers and bounded recovery state. It never creates elements or makes
requests; htmx sends resync requests.
[Decision 37](37-composer-javascript-pending-posts-keys-and-drafts.md) allows
bounded composer state: pending own posts in the DOM, an idempotency key and
session-bound drafts in `localStorage`. Requests still go through htmx.
[Decision 40](40-plain-javascript-by-default-library-islands-by-measured-exception.md) is the route for scoped UI-library islands; each
adopted island is listed here as its own exception. None exists today.
[Decision 41](41-client-storage-indexeddb-by-data-class.md) is the exception
route for IndexedDB by data class; Web Storage allowances stay unchanged.

**Why:** M3 adds SSE fragments and more scripts across exactly these
boundaries; without written rules each change would pick its own (#193).
**Considered:** full JavaScript parity (not needed for enhancements such as
real-time updates); a headless-browser test dependency such as go-rod
(deferred until after M3, if needed); a client-side framework (`DECISIONS.md`
2).

**Amended by:** [decision 43](43-typescript-with-a-go-driven-build.md) (typed client state; message list, sidebar and counts remain server-owned subject to decision 34; requests and decisions 34/37 unchanged).
