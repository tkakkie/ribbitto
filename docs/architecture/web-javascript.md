# Web JavaScript

JavaScript responsibilities and constraints for browser enhancements.

## JavaScript

JavaScript generates no HTML and owns no application state. It supplies focus, scroll,
keyboard, local time, selection constraints, and htmx/SSE input glue.

- `message-stream-v9.js` emits `read-applied` only after successful DOM
  application is shown while visible, including on return from a hidden tab.
  `read-visibility-v5.js` initializes on scoped `htmx:load`; automatic
  triggers send the snapshot, then the latest applied cursor.
  One request and one timer per page coalesce cursors. A 429 retries
  until success or hide/removal/navigation, with jittered exponential backoff
  capped initially at 8 s; `Retry-After` is a minimum, even above that cap.
  Success resets backoff and flushes newer shown work. Other failures flush only
  strictly newer work; the same cursor waits for another trigger. Cleanup clears
  timers. Composer responses and unapplied events cannot advance it.
- SSE `reset` closes htmx's event source and reloads the page to obtain a fresh snapshot.
- **No requests of its own:** no `fetch` or `XMLHttpRequest`. Requests go
  through HTML forms, links and htmx.
- **Storage:** `localStorage` and `sessionStorage` may hold only
  non-sensitive UX settings whose loss affects no function and no server
  state, such as whether a panel is open or a display preference. They
  never hold messages, unsent drafts, credentials, session tokens or other
  server-derived application data. IndexedDB is not used; a clear need,
  such as offline support, gets its own issue.
- **Files:** every script is external in `web/static` and loaded with the
  response nonce. Application scripts also define no globals and are
  versioned by file name (`-vN`), since static assets are immutable
  ([`rendering.md`](rendering.md)). Vendored libraries (htmx, idiomorph)
  keep their own globals and upstream file names ([a11y]).
- **htmx events:** an `htmx:load` handler works only on the inserted element
  and descendants, never the whole document (#168).

[a11y]: ../accessibility.md
