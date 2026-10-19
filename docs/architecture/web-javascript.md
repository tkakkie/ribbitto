# Web JavaScript

## JavaScript

JavaScript generates no HTML and owns no application state. It supplies focus, scroll,
keyboard, local time, selection constraints, clipboard copying and htmx/SSE glue.
Bounded state: [composer](web-composer.md#composer) exception (decision 37). UI-library islands need
scoped exceptions in
[decision 40](../decisions/40-plain-javascript-by-default-library-islands-by-measured-exception.md);
none exists.

**Exception (#1039):**
[decision 34](../decisions/34-live-unread-counts.md) lets one script own
sidebar badge numbers and bounded recovery state: snapshot sequence,
up to 1,000 retained `unread` events with eligibility/application flags,
recovering flag, loss generations and highest possibly lost sequence.
It changes only server-rendered badges' text and visibility, never
creates elements or makes requests; htmx sends resync requests. Markup contract:
[web contracts](web-contracts.md).

- `message-stream-v13.js` observes `checkpoint` to update only `sse-connect`'s
  `after`, monotonically, independent of applied/shown cursors. It emits `read-applied` only after successful DOM
  application is shown while visible, including hidden-tab return.
  `read-visibility-v6.js` initializes on scoped `htmx:load`; automatic
  triggers send the snapshot, then the latest applied cursor.
  One request and timer per page coalesce cursors. A 429 retries
  until success/hide/removal/navigation, with jittered exponential backoff
  initially capped at 8 s; `Retry-After` is a minimum, even above the cap.
  Success resets backoff and flushes newer shown work. Other failures flush only
  strictly newer work; the same cursor awaits another trigger. Cleanup clears
  timers. Composer responses and unapplied events cannot advance it.
- `sidebar-unread-v2.js` attaches to scoped `htmx:load` and SSE open events.
  It increments server-rendered badges only for newer, unseen `unread` frames
  from other members; `message` frames never increment. Snapshots apply in
  stream order, dropping retained events at or below their sequence. Eligible
  retained events above it reapply while not recovering, or when an answered
  resync has no loss since dispatch and its sequence covers every possibly lost
  event. Overflow records the highest dropped sequence; malformed or unapplied
  frames record the highest sequence seen, including a parsable failed frame.
  Retention itself is not loss. Overflow, malformed frames and DOM/request
  failures stop increments; losses during a
  request reserve another, including repeated losses during retries. One pending
  htmx request and one timer merge duplicate triggers, with exponential backoff
  starting at 250 ms and capped at 8 s; each retry chooses uniformly between
  half and all of that backoff. Visibility return and persisted `pageshow`
  request resync after uniform 0–2 s jitter. Recovery clearing is unchanged.
  A 429 counts as a request failure, keeps recovery open and retries
  through that backoff.
  Cleanup clears the timer when its stream container is removed.

  | Attribute / target | Contract |
  |---|---|
  | `data-member` on `#organization-stream` | session-resolved member UUID for own-post exclusion |
  | `data-sidebar-topic` on that container | selected topic UUID, present only on topic pages; routes the unlisted heading badge |
  | `data-snapshot-seq` on `#sidebar-channels` | exact counts' organisation sequence; replaced by every snapshot |
  | `data-unread-count` on every badge | raw numeric count, including hidden zero; only its number, text and visibility change |
  | `#sidebar-resync` | hidden htmx trigger; custom `sidebar-resync` event sends the member-scoped POST, without a swap |

- `pins-sync-v1.js` compares server snapshot sequences before pins/response
  swaps and selects loaded controls' server-rendered actions from the section's
  pin IDs. It restores button focus without touching history, draft or cursors.
- `member-mentions-v1.js` marks resolved references whose `data-member-id`
  matches `#organization-stream`'s existing `data-member`, on scoped
  `htmx:load` (including htmx's initial body event after all deferred scripts).
  Live posts, duplicate replay and moves use `htmx.swap`;
  Load older selects/prepends server-rendered items. Both run htmx's
  inserted-node load tasks on replacement and destination insertion.
  No DOM observer or polling. Token utilities add a 2 px dashed `brand`
  outline without offset, distinct from the solid, 2 px offset focus ring:
  a shape cue with at least 3:1 contrast against the message background
  and `selected` chip in both themes (see [colour contrast](../ui-colours.md#contrast-wcag-22-aa)).
  Server renders are shared unchanged across readers; unresolved and
  other-organisation references have no ID. Quote snapshots stay plain text.
- `mention-picker-v1.js` owns only the active token, dismissal and selection.
  It triggers the server-rendered search form, rejects stale results/picks and captures
  plain Enter/Tab before sending/navigation. Token boundaries mirror posting;
  code checks are conservative. It suppresses IME keys and the 100 ms commit Enter,
  retaining textarea focus with listbox ARIA ([composer](web-composer.md#composer)).
- `message-composer-v17.js` preserves the list's newest position or visible item
  during [composer growth](../ui-composer.md), reveals the end caret after layout,
  and prioritizes successful-post scrolling. After chip swaps it closes Quote's
  popover and focuses the textarea. At `htmx:configRequest` it copies the native
  submitter's name/value for `#quote-form`, since popover focus restoration clears
  htmx's remembered button; the unnamed Remove button adds nothing.
  Composer submits pause during quote-form processing so neither send path uses
  the old chip. Quoted sends wait for pending sends, then use the ordinary path;
  chip markup/requests stay server/htmx-owned.
- Partial quotes share submitter glue (`name="part"`); editable text is server-rendered.
  Page selection is planned (#1161).
- `message-drafts-v5.js` saves after 300 ms or lifecycle flush with monotonic
  input times. Blank/over-limit text writes tombstones; limits: 4,000 code points,
  50 entries. Equal/later stored edits win; eviction is best effort.
  Sign-out/deadline cancel saves and stop writes. Sweep/restore share validity checks.
  Restore seeds edit time without input/typing/focus; server text wins.
  Bubbling `message-sending`: `{key, body, inPlace}` before emptying; snapshots
  at once, cancels debounce; Retry/busy drops emit none.
  `message-sent`: `{key, message}` after posted-message state and ordinary
  prefix removal (after IME). Pending success requires 200; in-place 409
  requires prefix recovery. Pending events keep the snapshot key across 409
  request-key rotation, including Edit/resend.
  Only this tab's unchanged last write becomes current text/tombstone:
  matching send snapshots or typing after in-place sends. Later sends,
  pending-path edits and other tabs' writes survive; failures/unknown keys do nothing.
  Dirty in-place text refreshes after prefix removal, keeping
  edit time/debounce even if storage differs; saves check edit times.
  Bubbling textarea `message-pending-action` (`{key, kind}`):
  `edit` saves now, cancels stale debounce without input/typing;
  `remove` replaces that key's unchanged send snapshot.
  Markers toggle `hidden` for valid non-empty own member/session drafts.
  Hrefs map feed/topics to channels, topics to themselves; hide the open row.
  Refresh: scoped `#sidebar-channels` `htmx:load` (SSE/resync), draft `storage`,
  `message-sent`, persisted `pageshow`, even on list pages.
  Sign-out notice/deadline hide markers.
  [Drafts](web-composer.md#drafts): races/cleanup.
- `message-link-v1.js`: message actions menu's "Copy link" (#1159) copies the
  browser-resolved absolute `href` via Clipboard API, keeping shared live
  renders free of origins. Sets the link's `data-copied` ("Link copied") until menu close;
  appends that label to polite `#message-status`. Modifier keys, missing API
  or copy failure open the link as without JavaScript.
- SSE `reset` closes htmx's event source. A DOM observer holds reload while
  the composer has `htmx-request` or #1179's failure alert, or a pending form
  is queued, sending or failed (including unconfirmed). A localised status
  offers a plain reload link. With nothing at stake, one uniform 0–5 s timer
  reloads for a fresh snapshot; new at-stake state cancels it, and firing
  rechecks. Confirmed items await reload delivery; drafts do not hold it.
- **No requests of its own:** no `fetch`, `XMLHttpRequest`,
  `navigator.sendBeacon`, `WebSocket`, new `EventSource` or `htmx.ajax`.
  Requests use HTML forms, links and htmx. Scripts may set
  form-control values and call `requestSubmit()`, `htmx.trigger` and
  `htmx.process` on server-rendered/template-cloned elements; request
  URLs use server-rendered attributes, never script-built. The sole rewrite:
  `message-stream-v13.js` updates only the `after` parameter of the
  server-rendered `sse-connect` URL (above).
- **Storage:** `sessionStorage`: only non-sensitive UX settings whose loss
  affects no function or server state, e.g. panel-open or display preferences.
  `localStorage`: only the [drafts](web-composer.md#drafts) allowance, in the
  drafts script alone ([decision 37][d37]). Neither holds messages, credentials,
  session tokens or other server-derived application data except that allowance's
  bounded draft text (may quote messages), owner metadata and sign-out notice.
  IndexedDB is allowed only by
  [decision 41](../decisions/41-client-storage-indexeddb-by-data-class.md)'s data classes.
- **Files:** all scripts are external in `web/static`, loaded with the response
  nonce. Application scripts have no globals; file names are versioned (`-vN`),
  since static assets are immutable
  ([`rendering.md`](rendering.md)). Vendored libraries (htmx, idiomorph)
  keep their globals and upstream file names ([a11y]).
- **htmx events:** an `htmx:load` handler works only on the inserted element
  and descendants, never the whole document (#168).

## Composer

[Composer queue](web-composer.md#composer): at most 10 pending items,
FIFO, paused on unknown outcomes. Not sent Retry goes last; unknown Retry stays.
Edit restores failed text/key only into an empty composer outside IME, preserving
drafts; removes the item, frees its slot and resumes the queue.
Otherwise the visible disabled button explains why. Restored keys survive state
swaps, transfer to pending forms on send, renew on emptying. Unused keys post;
[matching bodies](posting.md) return the stored ID; different bodies under used
keys get 409/fresh Retry keys. Topic items settle by confirmed ID, response
topic/applied sequence or later move from the shown topic; no move history
([decision 37][d37]).

[a11y]: ../accessibility.md
[d37]: ../decisions/37-composer-javascript-pending-posts-keys-and-drafts.md
