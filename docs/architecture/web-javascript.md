# Web JavaScript

## JavaScript

Only focus, scroll, keyboard, local time, selection constraints, clipboard,
htmx/SSE glue; no HTML generation/application state except
[composer](web-composer.md#composer) (decision 37).
UI-library islands need scoped [decision 40](../decisions/40-plain-javascript-by-default-library-islands-by-measured-exception.md)
exceptions; none exist.

**Exception (#1039):** [decision 34](../decisions/34-live-unread-counts.md) lets one script own
sidebar badge numbers and bounded recovery state: the snapshot sequence,
at most 1,000 retained `unread` events with eligibility and application flags,
a recovering flag, loss generations and the highest possibly lost sequence.
Only server-rendered badge text/visibility changes; no elements or requests.
htmx sends resyncs. Markup: [web contracts](web-contracts.md).

- `message-stream-v13.js`: `checkpoint` updates only `sse-connect`'s `after`, monotonically, independent of applied/shown cursors. `read-applied` emits only after successful DOM application is visibly shown, including hidden-tab return. `read-visibility-v6.js` initializes on scoped `htmx:load`; automatic triggers send snapshot then latest applied cursor. One request/timer per page coalesce
  cursors. A 429 retries until success/hide/removal/navigation: jittered exponential backoff, initial 8 s cap; `Retry-After` is a minimum even above it. Success resets backoff, flushes newer shown work; other failures flush strictly newer work only. Same cursor awaits another trigger. Cleanup clears timers. Composer responses/unapplied events cannot advance it.
- `sidebar-unread-v2.js` attaches to scoped `htmx:load` and SSE open events.
  Badges increment only for newer unseen `unread` frames from other members;
  never `message` frames. Snapshots apply in stream order, dropping retained
  events at/below their sequence. Eligible
  retained events above it reapply while not recovering, or when an answered
  resync has no loss since dispatch and its sequence covers every possibly lost
  event. Overflow records the highest dropped sequence; malformed or unapplied
  frames record the highest sequence seen, including a parsable failed frame.
  Retention is not loss. Overflow, malformed frames and DOM/request failures
  stop increments; losses during requests reserve another, including during
  retries. One pending htmx request/timer merge triggers, with exponential backoff
  starting at 250 ms and capped at 8 s; each retry chooses uniformly between
  half and all of that backoff. Visibility return and persisted `pageshow`
  request resync after uniform 0–2 s jitter. Recovery clearing is unchanged.
  A 429 is a request failure: recovery stays open and retries with that backoff.
  Removing the stream container clears the timer.

  | Attribute / target | Contract |
  |---|---|
  | `data-member` on `#organization-stream` | session-resolved member UUID for own-post exclusion |
  | `data-sidebar-topic` on that container | selected topic UUID, present only on topic pages; routes the unlisted heading badge |
  | `data-snapshot-seq` on `#sidebar-channels` | exact counts' organisation sequence; replaced by every snapshot |
  | `data-unread-count` on every badge | raw numeric count, including hidden zero; only its number, text and visibility change |
  | `#sidebar-resync` | hidden htmx trigger; custom `sidebar-resync` event sends the member-scoped POST, without a swap |

- `pins-sync-v1.js` compares snapshot sequences before pins/response swaps; section pin IDs select loaded controls' server actions. Restores button focus, preserving history/draft/cursors.
- `member-mentions-v1.js` marks resolved references matching `data-member-id` to `#organization-stream`'s `data-member` on scoped `htmx:load`, including the initial body event after deferred scripts. Live posts, duplicate replay and moves use `htmx.swap`; Load older selects/prepends server-rendered items. Both run inserted-node load tasks, including replacements/destination insertions; no DOM observer/polling. Token utilities add a 2 px dashed `brand` outline, no offset, versus the solid 2 px
  offset focus ring: a shape cue with ≥3:1 contrast against message background and `selected` chip in both themes ([contrast](../ui-colours.md#contrast-wcag-22-aa)). Shared server renders are identical for all readers; unresolved/other-organisation references have no ID. Quote snapshots stay plain text.
- `mention-picker-v1.js` owns only the active token, dismissal and selection. It triggers server-rendered search, rejects stale results/picks and captures plain Enter/Tab before send/navigation. Token boundaries mirror posting; code checks are conservative. IME keys and the 100 ms commit Enter are suppressed; textarea focus/listbox ARIA stay ([composer](web-composer.md#composer)).
- `message-composer-v21.js` anchors [growth](../ui-composer.md) to newest/visible
  item; shows end caret after layout; post-success scroll wins.
  Quote closes popover; swaps focus Remove (Tab → Quote only part), part
  `#quote-text`, body on removal; 422/409 keep focus/caret/selection.
  [Autofocus](web-composer.md#composer): native on plain loads, script on swaps.
  `htmx:configRequest` copies native `#quote-form` submitter name/value: popover
  focus restore clears htmx's submitter; unnamed Remove adds nothing.
  Submits await quotes (no stale chip). Quoted sends follow pending sends on
  the ordinary path; server/htmx own chip markup/requests.
- Parts share `name="part"` submitter glue; server renders editable text.
  `quote-selection-v1.js`: nonempty selections within one body, outside quotes.
  Plain text stays; rich text follows `ExtractChatV1`: whole mentions use
  `data-quote-handle`, blocks and `<br>` each add one newline;
  code/emphasis/links keep text. Cut/unresolved mentions or unknown markup hide
  the button; triple-click may leave an empty outside tail. Pointerdown/mousedown
  preserve selection. Submit/`htmx:configRequest` recheck scope/text. Only selections
  supply `quote_text` via parameter proxy/hidden input; other submits, before send
  and after request/swap clear/disable it. Swaps refresh visibility; focus `#quote-text`.
- `message-drafts-v6.js` saves after 300 ms or lifecycle flush with monotonic input times. Blank/over-limit text writes tombstones; 4,000-code-point/50-entry limits apply immediately. Equal/later stored edits win observed reads; eviction is best effort. Sign-out/deadline stop writes/cancel saves. Sweep/restore share validity checks; restore seeds edit time without input/typing/focus; server text wins. Bubbling textarea `message-draft-restored` (`{key}`) hands the saved key to the composer's `restoredKey`; any text edit/emptying renews it. `message-sending` (`{key, body, inPlace}`) saves text/key before dispatch/emptying, cancelling debounce; Retry/busy drops emit none. This covers in-flight navigation/reload; several tabs can safely send one key (#1328).
  `message-sent` (`{key, message}`) clears the saved key after posted-message state/ordinary prefix removal (after IME). Pending success requires 200; in-place 409 requires prefix recovery. Snapshot keys survive 409 request-key rotation, including Edit/resend. Only this tab's unchanged last write becomes current text/tombstone for matching snapshots or typing after in-place sends. Later sends, pending edits and other tabs' writes survive; failures/unknown keys do nothing. Dirty in-place prefix cleanup refreshes text but retains edit time/debounce even if storage differs; saves check edit times. `message-pending-action` (`{key, restoredKey, kind}`): `edit` saves text/key immediately without input/typing; `remove` replaces the unchanged snapshot. Markers toggle `hidden` for valid non-empty own drafts. Hrefs map feeds/topics to channels, topics to themselves; hide the open row. Refresh: scoped sidebar `htmx:load` (SSE/resync), draft `storage`, `message-sent`, persisted `pageshow`, including list pages. Sign-out/deadline hide markers. [Drafts](web-composer.md#drafts): schema/races/cleanup.
- `message-link-v1.js` handles actions-menu "Copy link" (#1159) with the Clipboard API and browser-resolved absolute `href`, keeping shared live renders origin-free. `data-copied` shows "Link copied" until menu close; that label also goes to polite `#message-status`. Modifiers, missing API or copy failures open the link, as without JavaScript.
- SSE `reset` closes htmx's event source. DOM observer holds reload for composer `htmx-request`/#1179's failure alert or queued/sending/failed (even unconfirmed) pending forms. Localised status: plain reload link. Nothing at stake: one uniform 0–5 s timer reloads for a fresh snapshot; new at-stake state cancels; firing rechecks. Confirmed items await reload delivery; drafts never hold it.
- **Requests:** only forms, links and htmx; no `fetch`, `XMLHttpRequest`, `navigator.sendBeacon`, `WebSocket`, new `EventSource` or `htmx.ajax`. Scripts may set controls/call `requestSubmit()`, `htmx.trigger`, `htmx.process` on server-rendered/template-cloned elements. URLs come from server attributes, never scripts. Only `message-stream-v13.js` rewrites `sse-connect`'s `after` (above).
- **Storage:** `sessionStorage` holds only non-sensitive UX settings whose loss affects no function/server state (e.g. panel preference). Only the drafts script uses `localStorage` ([decision 37][d37]): bounded draft text (possibly quoting messages), send key, owner metadata and sign-out notice. Neither holds messages, credentials, session tokens or other server-derived application data. IndexedDB requires [decision
  41](../decisions/41-client-storage-indexeddb-by-data-class.md)'s data classes.
- **Files:** external in `web/static`, loaded with the response nonce. No application globals; immutable assets need versioned names (`-vN`, [rendering](rendering.md)). Vendored htmx/idiomorph keep upstream globals/names ([a11y]).
- **htmx:** `htmx:load` handlers visit only the inserted subtree, never the document (#168).

## Composer

[Composer queue](web-composer.md#composer): ≤10 pending items, FIFO; unknown outcomes pause.
Not sent Retry goes last; unknown Retry stays. Edit restores failed text/key into an
empty composer outside IME, preserving drafts; removes item, frees slot, resumes queue.
Else disabled button explains why. Restored keys survive state swaps,
move to pending forms on send, renew on emptying. Remove holds unconfirmed raw
text/key in memory per feed/topic for 5 minutes; identical text reuses it before
the limit; different text takes a fresh key. Expiry/pagehide drops the hold.
Remove then reload loses it;
stored messages remain visible. Unused keys post;
[matching bodies](posting.md) return stored ID; different bodies under used keys get
409/fresh Retry keys. Topic items settle by confirmed ID, response topic/applied sequence
or later move out; no move history ([decision 37][d37]).

[a11y]: ../accessibility.md
[d37]: ../decisions/37-composer-javascript-pending-posts-keys-and-drafts.md
