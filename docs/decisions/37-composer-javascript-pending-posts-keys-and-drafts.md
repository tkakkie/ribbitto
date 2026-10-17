# 37. Composer JavaScript: pending posts, idempotency keys and drafts on the device

**Decided** (#1151, approved by the maintainer on 2026-10-16). Each use names
the issue that implements it. Use (a) is implemented on the feed; topic
views await #1188. It is the second
narrow exception to
[decision 19](19-web-layers-server-owned-html-htmx-swaps-javascript-as-enhancement.md),
beside [decision 34](34-live-unread-counts.md). The rules apply to any
composer code, plain JavaScript or a Svelte island if #1152 adopts one; #1152
may change them only through its own decision, with its measurements.

## (a) Pending own post (#1156, #1174)

After the person sends, one composer script may show the post as *sending*
before the server answers. Only enhanced composers on pages that receive live
messages get pending posts: the latest channel feed today, and latest topic
views once #1188 implements them. `?before=` and `?around=` pages and every other
plain composer keep plain submission.

The item is cloned from a server-rendered, localised `<template>`
(`content.cloneNode(true)`); the body goes in only through `textContent`, the
key and retry text in form controls' `value`s.
Pending items live in their own list next to `#message-items`, never inside it,
so the stream script's ordering by `data-event-seq` and bounded insertion stay
as they are. They never move `data-event-cursor` or `data-applied-cursor`, send
a reading POST, change unread counts or appear in `#message-status`.

- **State:** per item, its form-control values (text, key, composer
  generation, shown `data-event-cursor`), its state and the message ID its own
  response names in `data-posted-message`, associated through the request's
  element, never by comparing text. The states are *queued* (#1174),
  *sending*, *Not sent*, *Could not confirm sending*, and confirmed but
  awaiting stream delivery.
- **Bound:** at most 10 items per tab, counting every state; an eleventh send
  keeps its text in the composer. #1156 allows one item, #1174 up to 10.
- **Lifetime:** DOM only, never storage. An item is removed when the message
  its response names is in `#message-items`, when that message has moved out
  of the shown topic, or when the member chooses Remove. A failure keeps the
  text and the key, with a retry. A reload drops pending items; their text may
  survive only as a draft (d).

## (b) Idempotency key (#1155)

Each send carries an opaque key in the composer's hidden `idempotency_key`. The
server renders a fresh one on the initial render, after a success and with a
409; a 422 keeps the submitted key exactly, so plain forms are deduplicated
too. The composer script may replace it with `crypto.randomUUID()` when it
empties the composer before the answer (#1156); that needs a secure context
(HTTPS or a potentially trustworthy origin such as localhost). The server scopes the key to the organisation
and member of the authorised membership, which `registerOrgRoutes` gets from
`Authorizer.Member` for the signed-in account and the URL's slug, never to
identifiers in the request body (invariants 3–5). The key grants nothing; a
repeat returns only that member's own message. It never appears in another
member's page or a stream payload: live items are rendered once per language
and shared by every member's stream, and matching uses `data-posted-message`.

## (c) Requests stay with htmx (#1173)

Composer JavaScript sends no requests itself: no `fetch`, `XMLHttpRequest`,
`navigator.sendBeacon`, `WebSocket`, new `EventSource` or `htmx.ajax`. It may
set form-control values and call `requestSubmit()`, `htmx.trigger` and
`htmx.process` on server-rendered or template-cloned elements. Every composer
request URL comes from a server-rendered attribute, never built by JavaScript.
Mention suggestions (#117) come as server-rendered fragments over `hx-get`.

## (d) Drafts on this device (#1178, #1181, #1162, #1177)

One drafts script may keep unsent composer text in `localStorage`: it must
survive reloads and closed tabs, and `sessionStorage` does not. **Bound:** text
up to the message limit (4,000 code points) and at most 50 entries per member
and session, evicting the least recently edited; across tabs the cap and
eviction are best effort (#1186). An entry is removed 7 days
after its last edit. A storage error drops only the stored copy, never the
text on the page. Order: #1178 → #1181 → #1162 → #1177.

- **Each entry is bound to its session.** Organisation pages render on
  `#organization-stream` the member UUID (`data-member`), the session marker
  `data-session`, the session's expiry and the render time (#1181). The marker
  is the session's id, a UUIDv7 that PostgreSQL generates, so it is
  time-ordered. It is not a credential (sessions are found only by token
  hash); the page never carries the token or its hash. Key:
  `ribbitto.draft.v1:<member>:<session>:<channel>:<place>`, where the place
  is the literal `feed` or the topic's canonical UUID; value: JSON
  `{member, session, expires, text, at}` (#1178 fixes the format). No owner
  record is shared between sessions.
- **Restore checks the owner** (#1162). A page reads only its own member and
  session keys and restores an entry only if its stored member, session and
  expiry equal its own and the expiry has not passed. However tabs interleave
  (storage has no transactions), an earlier session's text is never shown
  under a later session or another member.
- **Cleanup compares milliseconds.** Every cleanup has a horizon: on an
  organisation page its own session marker (#1181); on the sign-in page a
  `uuidv7()` read from PostgreSQL in the request (`identity.SessionHorizon`,
  #1178), the same clock as session ids, never the web server's. Cleanup
  compares the 48-bit millisecond prefix and removes only entries **strictly
  older** than the horizon, plus malformed ones; other sessions'
  same-millisecond entries stay until their expiry or a later cleanup. So a
  cleanup never removes a newer session's entry or a valid own one, and reads
  only its own session's values (#1181). The sign-in page renders a horizon
  and cleans up only for signed-out requests: `GET /signin` and the error renders of `POST /signin` without a
  signed-in account. If reading the horizon fails, that render does no cleanup.
- **Writes:** a page writes only its own session's keys, and only before its
  session's deadline (expiry minus render time, measured with
  `performance.now()` so the device clock cannot move it). At the deadline an
  open page stops writing and removes its own keys.
- **Sign-out** (#1178): a successful `POST /signout` answers 303 to `/signin`
  with `Clear-Site-Data: "storage"` where the browser supports it; a failure
  answers 500 with neither. The sign-in page then cleans up. The sign-out
  form has no submit handler: it would clear drafts even when sign-out fails. After its cleanup the sign-in page writes a
  content-free sign-out notice key whose value is its horizon, so each write
  fires a `storage` event; other open pages stop writing on it (#1162). The
  notice is best effort: what a stale tab still writes is bound to its ended
  session, never restored and removed by a later cleanup.
- **Cleanup is guaranteed** at sign-out in this browser (the header and the
  sign-in page); at the next load of a later organisation page or of a
  signed-out sign-in page with a horizon; and in an open page at its
  session's deadline. Signed-out organisation requests get a plain 404, so
  after an expiry cleanup waits for one of those pages.
- **Limits:** with no ribbitto page open nothing runs: leftover entries stay
  on disk until one loads in that browser (on a shared computer, sign out). If the database clock steps backwards between a render
  and a later sign-in, a newer session's marker can sort before an older
  horizon.
- **Cost:** signing in again drops the earlier session's drafts; rare, since sessions last 30 days and drafts 7.
- **Sidebar** (#1177): the script may only show or hide a server-rendered,
  hidden draft marker, reapplying it after each sidebar swap.
- **Privacy:** stored text is plaintext in the browser profile, readable by
  anyone using it, and may include quoted text from others' messages (#1160,
  #1161) that the member chose to insert. That is the accepted cost.

## (e) The send-key preference is server-side (#1158, #1175)

It applies on all of a member's devices and changes what Enter does, so it is
not a disposable UX setting. The server stores it (#1175 chooses the owning
module) and renders it as an attribute the composer script reads; it never
lives in browser storage.

## What stays forbidden

- HTML built from strings (what the pattern check rejects) and `templ.Raw`.
- Composer JavaScript building request URLs, and requests outside htmx.
- Browser storage of messages or server-derived data, except (d)'s bounded
  drafts allowance or IndexedDB data under
  [decision 41](41-client-storage-indexeddb-by-data-class.md)'s classes 1–2.
  Class 3 needs its own decision; credentials, session tokens and their hashes
  stay forbidden.
- Client state for the message list, sidebar, unread counts or presence beyond
  decision 34, except toggling (d)'s draft marker. These stay server-rendered;
  a pending item is never turned into a real message in place.

**Kept:** `message-stream-v11.js` rewrites only the `after` parameter of the
server-rendered `sse-connect` URL
([decision 30](30-one-organisation-wide-stream-per-tab-filtered-by-its-interests.md#amendment-transport-progress-1103));
it is not composer code.

**Why:** the M7 composer needs a post to feel sent at once, a retry that never
posts twice and text that survives a reload, without the client owning
messages. [The vision](../vision.md) asks a
client-side change that alters decision 19 to come with measurements: #1149's
[own-post baseline](https://github.com/tkakkie/ribbitto/issues/1149#issuecomment-6199142777)
is 30.9 ms median send → visible on the desktop feed and 75.5 ms on the phone
feed (45.5 and 124.5 ms in topic views). #1156 reports against it, to the text
being visible.

**Considered:** `fetch` in one composer script (direct control of queueing and
retries, but a second request path beside htmx's `hx-sync`, error handling and
422 swaps; no measurement shows htmx is the bottleneck); one owner record per
member (a stale tab's write could be restored under a new session); a
separate public time-ordered marker (the id is not a credential); the
send-key preference in browser storage (it must follow the member); and, for
the reasons (d) gives, `sessionStorage`, the web server's clock as the
horizon and a submit handler on the sign-out form.
