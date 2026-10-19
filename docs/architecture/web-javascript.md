# Web JavaScript

## JavaScript

JavaScript generates no HTML and owns no application state. It supplies focus, scroll,
keyboard, local time, selection constraints, clipboard copying and htmx/SSE glue.
Bounded composer state is the
[composer](web-composer.md#composer) exception (decision 37). UI-library islands require
scoped exceptions under
[decision 40](../decisions/40-plain-javascript-by-default-library-islands-by-measured-exception.md);
none exists today.

**Exception (#1039):**
[decision 34](../decisions/34-live-unread-counts.md) lets one script own
sidebar badge numbers and bounded recovery state: the snapshot sequence,
at most 1,000 retained `unread` events with eligibility and application flags,
a recovering flag, loss generations and the highest possibly lost sequence.
It changes only the text and visibility of server-rendered badges, never
creates elements or makes requests;
htmx sends resync requests. The markup contract is in
[web contracts](web-contracts.md).

- `message-stream-v13.js` observes `checkpoint` to update only `sse-connect`'s
  `after`, monotonically and independently of applied/shown cursors. It emits `read-applied` only after successful DOM
  application is shown while visible, including on return from a hidden tab.
  `read-visibility-v6.js` initializes on scoped `htmx:load`; automatic
  triggers send the snapshot, then the latest applied cursor.
  One request and one timer per page coalesce cursors. A 429 retries
  until success or hide/removal/navigation, with jittered exponential backoff
  capped initially at 8 s; `Retry-After` is a minimum, even above that cap.
  Success resets backoff and flushes newer shown work. Other failures flush only
  strictly newer work; the same cursor waits for another trigger. Cleanup clears
  timers. Composer responses and unapplied events cannot advance it.
- `sidebar-unread-v2.js` attaches to scoped `htmx:load` and SSE open.
  Only newer, unseen, other-member `unread` frames increment badges.
  Ordered snapshots drop retained events at
  or below their sequence. Higher eligible events reapply outside recovery,
  or after resync with no loss since dispatch and a sequence covering all loss.
  Overflow records the highest
  dropped sequence; malformed/unapplied frames the highest seen, including
  parsable failed frames. Retention is not loss.
  Overflow, malformed frames and DOM/request failures stop increments.
  Loss during requests reserves another resync, including during retries.
  One htmx request and timer coalesce triggers. Retry backoff doubles
  from 250 ms to 8 s, uniformly jittered between half and full delay.
  Visible return/persisted `pageshow`: resync after uniform 0–2 s jitter;
  recovery clearing is unchanged. A 429 keeps recovery open and retries.
  Removal clears its timer.

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
  Load older selects and prepends server-rendered items. Both run htmx's
  inserted-node load tasks, including for replacement and destination insertion.
  No DOM observer or polling is needed. Token utilities add a 2 px dashed `brand`
  outline with no offset, distinct from the solid, 2 px offset focus ring:
  a shape cue with at least 3:1 contrast against the message background
  and `selected` chip in both themes (see [colour contrast](../ui-colours.md#contrast-wcag-22-aa)).
  The shared server render stays identical for every reader; unresolved and
  other-organisation references carry no ID. Quote snapshots remain plain text.
- `mention-picker-v1.js` owns only the active token, dismissal and selection.
  It triggers the server-rendered search form, rejects stale results and picks,
  and captures plain Enter/Tab before sending/navigation. Its token boundary mirrors posting;
  code checks are conservative. It suppresses IME keys and the 100 ms commit Enter,
  retaining textarea focus with listbox ARIA ([composer](web-composer.md#composer)).
- `message-composer-v17.js` preserves the list's newest position or visible item
  during [composer growth](../ui-composer.md), reveals the end caret after layout,
  and gives successful-post scrolling precedence. It closes Quote's popover and focuses the
  textarea after a chip swap. At `htmx:configRequest` it copies the native
  submitter's name/value for `#quote-form`, since popover focus restoration clears
  htmx's remembered button; the unnamed Remove button adds nothing.
  Composer submits pause during quote-form processing, preventing either
  send path using the old chip. Quoted sends use the ordinary path, waiting for
  pending sends to finish; chip markup and requests remain server/htmx-owned.
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
  Markers toggle `hidden` for valid, non-empty own member/session drafts.
  Hrefs map feed/topics to channels and topics to themselves; hide the open row.
  Apply on `#sidebar-channels` scoped `htmx:load` (SSE/resync),
  draft `storage`, `message-sent`, persisted `pageshow`, including list pages.
  Sign-out notice/deadline hide markers.
  [Drafts](web-composer.md#drafts): races/cleanup.
- `message-link-v1.js` handles "Copy link" in a message's actions menu
  (#1159): it copies the link's browser-resolved absolute `href` with the
  Clipboard API, so shared live renders hold no origin; marks the link
  `data-copied` (which shows "Link copied") until its menu closes; and
  appends that label to the polite `#message-status`. With a modifier key,
  without the API, or when copying fails, the link opens, as without
  JavaScript.
- SSE `reset` closes htmx's event source. A DOM observer holds reload while
  the composer has `htmx-request` or #1179's failure alert, or a pending form
  is queued, sending or failed (including unconfirmed). A localised status
  offers a plain reload link. With nothing at stake, one uniform 0–5 s timer
  reloads for a fresh snapshot; new at-stake state cancels it, and firing
  rechecks. Confirmed items await reload delivery; drafts do not hold it.
- **No requests of its own:** no `fetch`, `XMLHttpRequest`,
  `navigator.sendBeacon`, `WebSocket`, new `EventSource` or `htmx.ajax`.
  Requests go through HTML forms, links and htmx. A script may set
  form-control values and call `requestSubmit()`, `htmx.trigger` and
  `htmx.process` on server-rendered or template-cloned elements; request
  URLs come from server-rendered attributes, never built by a script. The
  one kept rewrite is `message-stream-v13.js` updating only the `after`
  parameter of the server-rendered `sse-connect` URL (above).
- **Storage:** `sessionStorage` may hold only non-sensitive UX settings
  whose loss affects no function and no server state, such as whether a
  panel is open or a display preference; `localStorage` only the one
  [drafts](web-composer.md#drafts) allowance, in the drafts script alone
  ([decision 37][d37]). They never hold messages, credentials, session
  tokens or other server-derived application data, except that allowance's
  bounded draft text (which may quote messages), owner metadata and
  sign-out notice. IndexedDB is allowed only by
  [decision 41](../decisions/41-client-storage-indexeddb-by-data-class.md)'s
  data classes.
- **Files:** every script is external in `web/static` and loaded with the
  response nonce. Application scripts also define no globals and are
  versioned by file name (`-vN`), since static assets are immutable
  ([`rendering.md`](rendering.md)). Vendored libraries (htmx, idiomorph)
  keep their own globals and upstream file names ([a11y]).
- **htmx events:** an `htmx:load` handler works only on the inserted element
  and descendants, never the whole document (#168).

## Composer

[Composer queue](web-composer.md#composer): at most 10 pending items,
FIFO, paused on unknown outcomes. Not sent Retry goes last; unknown Retry stays.
Edit restores failed text/key only into an empty composer outside IME, never
overwriting drafts; removes the item, frees its slot and resumes the queue.
Otherwise the visible disabled button explains why. Restored keys survive state
swaps, transfer to pending forms on send, renew on emptying. Unused keys post;
[matching bodies](posting.md) return the stored ID; different bodies under used
keys get 409/fresh Retry keys. Topic items settle by confirmed ID, response
topic/applied sequence or later move from the shown topic; no move history
([decision 37][d37]).

[a11y]: ../accessibility.md
[d37]: ../decisions/37-composer-javascript-pending-posts-keys-and-drafts.md
