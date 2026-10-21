# Web JavaScript

## JavaScript

[Decision 43](../decisions/43-typescript-with-a-go-driven-build.md).

JavaScript: no HTML generation/application state; only focus, scroll, keyboard,
local time, selection constraints, clipboard and htmx/SSE glue.
Bounded state: [composer][c], decision 37.
UI-library islands need scoped [decision 40](../decisions/40-plain-javascript-by-default-library-islands-by-measured-exception.md)
exceptions; none exist.

**Exception (#1039):** [decision 34](../decisions/34-live-unread-counts.md) lets one script own
sidebar badge numbers and bounded recovery state: the snapshot sequence,
at most 1,000 retained `unread` events with eligibility and application flags,
a recovering flag, loss generations and the highest possibly lost sequence.
It changes only server-rendered badge text/visibility, never creates elements
or requests; htmx sends resyncs. Markup: [web contracts](web-contracts.md).

- [Unread scripts](web-javascript-unread.md): stream checkpoints, visible reads and sidebar badges.
- `pins-sync-v2.js` compares server snapshot sequences before pins/response swaps and selects loaded controls' server-rendered actions from the section's pin IDs. It restores button focus, preserving history/draft/cursors.
- `member-mentions-v1.js` marks resolved references matching `data-member-id` to `#organization-stream`'s `data-member` on scoped `htmx:load`, including the initial body event after deferred scripts. Live posts, duplicate replay and moves use `htmx.swap`; Load older selects/prepends server-rendered items. Both run inserted-node load tasks, including replacements/destination insertions; no DOM observer/polling. Token utilities add a 2 px dashed `brand` outline, no offset, versus the solid 2 px
  offset focus ring: a shape cue with ≥3:1 contrast against message background and `selected` chip in both themes ([contrast](../ui-colours.md#contrast-wcag-22-aa)). Shared server renders are identical for all readers; unresolved/other-organisation references have no ID. Quote snapshots stay plain text.
- `mention-picker-v1.js` owns only the active token, dismissal and selection. It triggers server-rendered search, rejects stale results/picks and captures plain Enter/Tab before send/navigation. Token boundaries mirror posting; code checks are conservative. IME keys and the 100 ms commit Enter are suppressed; textarea focus/listbox ARIA stay ([composer][c]).
- `message-composer-v21.js` anchors [growth](../ui-composer.md) to newest/visible
  item; shows end caret after layout; post-success scroll wins.
  Quote closes popover; swaps focus Remove (Tab → Quote only part), part
  `#quote-text`, body on removal; 422/409 keep focus/caret/selection.
  [Autofocus][c]: native on plain loads, script on swaps.
  `htmx:configRequest` copies native `#quote-form` submitter name/value: popover
  focus restore clears htmx's submitter; unnamed Remove adds nothing.
  Submits await quotes (no old chip). Quoted sends go ordinary after pending
  sends; server/htmx own chip markup/requests.
- Parts share submitter glue (`name="part"`); server renders editable text. `quote-selection-v1.js` accepts nonempty selections in one body, outside quotes. Plain text stays; rich text follows `ExtractChatV1`: whole mentions use `data-quote-handle`, blocks join with one newline, `<br>` adds one; code, emphasis/links keep text. Cut/unresolved mentions or unknown markup hide the button; triple-click may leave an empty outside tail. Pointerdown/mousedown preserve selection.
  Submit/`htmx:configRequest` recheck scope/text. Only selection supplies `quote_text` via parameter proxy/hidden input; other submits, before send and after request/swap clear/disable it. Swaps refresh visibility. Selection quotes focus `#quote-text`.
- `message-drafts-v6.js` saves after 300 ms or lifecycle flush using monotonic input times. Blank/over-limit text writes tombstones; 4,000-code-point/50-entry limits apply from the first write. Equal/later stored edits win against the observed read; eviction is best effort. Sign-out/deadline stop writes and cancel saves. Sweep/restore share validity checks; restore seeds edit time without input/typing/focus, and server text wins. Bubbling `message-sending` (`{key, body, inPlace}`) snapshots
  text and key at dispatch before the request leaves or textarea empties, cancelling debounce; Retry/busy drops emit none. Bubbling textarea `message-draft-restored` (`{key}`) gives the composer its `restoredKey`; reuse it only while restored text is unchanged, renewing on any edit/emptying.
  `message-sent` (`{key, message}`) clears the saved key on a 200 or stored-key confirmation and follows posted-message state and ordinary prefix removal (after IME). Pending success requires 200; in-place 409 requires prefix recovery. Pending events retain snapshot keys across 409 request-key rotation, including Edit/resend. Only this tab's unchanged last write becomes current text/tombstone for matching send snapshots or typing after in-place sends. Later sends, pending-path
  edits and other tabs' writes survive; failures/unknown keys do nothing. After prefix removal, dirty in-place text refreshes, retaining edit time/debounce even if storage differs; saves check edit times. Bubbling textarea `message-pending-action` (`{key, restoredKey, kind}`): `edit` saves text/key now, cancelling stale debounce without input/typing; `remove` replaces that key's unchanged send snapshot. Markers toggle `hidden` for valid non-empty own member/session drafts. Hrefs map feed/topics to channels, topics to
  themselves; hide the open row. Refresh: scoped `#sidebar-channels` `htmx:load` (SSE/resync), draft `storage`, `message-sent`, persisted `pageshow`, even on list pages. Sign-out notice/deadline hide markers. [Drafts][dr]: races/cleanup.
- `message-link-v1.js` handles actions-menu "Copy link" (#1159) with the Clipboard API and browser-resolved absolute `href`, keeping shared live renders origin-free. `data-copied` shows "Link copied" until menu close; that label also goes to polite `#message-status`. Modifiers, missing API or copy failures open the link, as without JavaScript.
- SSE `reset` closes htmx's event source. DOM observer holds reload for composer `htmx-request`/#1179's failure alert or queued/sending/failed (even unconfirmed) pending forms. Localised status: plain reload link. Nothing at stake: one uniform 0–5 s timer reloads for a fresh snapshot; new at-stake state cancels; firing rechecks. Confirmed items await reload delivery; drafts never hold it.
- **Requests:** HTML forms/links or htmx; no `fetch`, `XMLHttpRequest`, `navigator.sendBeacon`, `WebSocket` or new `EventSource`; `htmx.ajax` only in `pins-sync-v2.js`. Scripts may set controls/call `requestSubmit()`, `htmx.trigger` and `htmx.process` on server-rendered/template-cloned elements. Request URLs come from server attributes, never script-built. Only `message-stream-v14.js` rewrites `sse-connect`'s `after` parameter ([unread scripts](web-javascript-unread.md)).
- **Storage:** `sessionStorage` holds only non-sensitive UX settings whose loss affects no function/server state (e.g. panel open/display preference). `localStorage` allows only [drafts][dr], in the drafts script ([decision 37][d37]). Neither holds messages, credentials, session tokens or other server-derived application data except bounded draft text (possibly quoting messages), send key, owner metadata and sign-out notice. IndexedDB requires [decision
  41](../decisions/41-client-storage-indexeddb-by-data-class.md)'s data classes.
- **Files:** scripts are external in `web/static`, loaded with the response nonce. Application scripts define no globals; immutable assets require versioned names (`-vN`, [rendering](rendering.md)). Vendored htmx/idiomorph retain their globals and upstream names ([a11y]).
- **htmx events:** an `htmx:load` handler works only on the inserted element and descendants, never the document (#168).

## Composer

[Composer queue][p]: ≤10 pending items, FIFO; unknown outcomes pause.
Not sent Retry goes last; unknown Retry stays. Edit restores failed text/key only into an
empty composer outside IME, preserving drafts; removes item, frees slot, resumes queue.
Else visible disabled button explains why. Restored keys survive state swaps,
move to pending forms on send, renew on emptying.
[Remove hold](web-composer-pending.md#remove-hold) defines its lifetime and reload limit.
Unused keys post;
[matching bodies](posting.md) return stored ID; different bodies under used keys get
409/fresh Retry keys. Topic items settle by confirmed ID, response topic/applied sequence
or later move from shown topic; no move history ([decision 37][d37]).

[a11y]: ../accessibility.md
[d37]: ../decisions/37-composer-javascript-pending-posts-keys-and-drafts.md

[c]: web-composer.md#composer
[dr]: web-composer.md#drafts
[p]: web-composer-pending.md#pending-own-posts
