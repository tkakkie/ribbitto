# Web composer

## Composer

Bounded state ([decision 37][d37]): server-rendered keys, send glue, feed/topic pending
items; *planned* parts await issues.

Idle feed/topic sends use the ordinary path for blank/quoted text, >4,000
code points after newline normalisation/trim, or no `crypto.randomUUID()`.

- **Ordinary sending** (input glue, #1173): textarea usable in flight:
  answers replace only `#message-composer-state`. 200 removes sent body
  (htmx request parameters) only if still a textarea prefix, via
  `setRangeText("", 0, length, "preserve")`: caret/selection stay on
  remaining text; no `input`, hence no typing signal.
  A `WeakMap` defers removal until `compositionend`, keeping the confirmation
  ID before stream settle consumes it. Answers copy `data-invalid` to textarea
  `aria-invalid`.
- **Ordinary failures** (#1179) keep the text and the key; `#message-send-alert` says
  which. 400/404/413/422 reject before any write; 429 by spec:
  *not sent*, or with the mark, *this retry was refused and the
  earlier send is still unconfirmed*. Network errors, timeouts, 5xx or
  other 4xx (except 409) may follow a commit: *could not confirm*, marking the
  key in `data-unconfirmed-key`. Only 200/409 follow the
  key lookup; both renew the key. The mark clears exactly on key change.
  A 409 carries `data-sent-body` and omits its error only in the same channel,
  with neither stored nor retried text quoted, and when stored typed-body byte length and SHA-256 digest
  verify that prefix of the retry text (without resolving handles): the
  script removes that prefix by the same rules (*the earlier message was
  sent*), or says the 409's error if the start changed. Otherwise
  text/error stay, including legacy rows without proof.
- **Pending own posts:** see [web-composer-pending.md](web-composer-pending.md).
- **Idempotency key** (#1155): hidden `idempotency_key`, fresh on load, success/409; kept on 422. Scoped to authorised membership; never in streams. Pending sends replace it with
  `crypto.randomUUID()` (secure contexts only; otherwise ordinary sending).
- **Stream reset:** the [reload guard](web-javascript.md#javascript) reads form
  `data-state`, the composer's request class and #1179's failure alert. Queued, sending
  and failed items hold reload; confirmed items do not. Retry success, Remove
  or Edit can release it: Edit's restored text is a draft. Dispatched
  requests time out at 30 s; queues/retries/failed text can extend the hold.
- **Quote chip:** `#composer-quote-description`: author/excerpt or labelled
  editable part, empty without quote. Quote closes popover; whole swaps focus
  Remove (Tab → Quote only part); parts `#quote-text` (Tab → Remove → body → send);
  removal focuses body.
  422/409 keep focus/caret/selection in server text, matching IDs/button purposes.
  Script owns swaps, suppressing htmx autofocus replay; native autofocus serves
  plain part loads. Chip/gap reduce [growth cap](../ui-composer.md); resize observer
  keeps reader. Plain Quote/Remove GET `#message-composer`; Remove loses text.
- **Mention picker:** `mention-picker-v1.js` enhances all composers.
  `#mention-search`: htmx 120 ms debounce,
  replace sync and `outerHTML` into `#mention-listbox`. Only matching `data-query`
  responses open; picks recheck the token. Empty results hide the picker;
  `#mention-status` announces no members. `@` requests eight members.
  Token starts mirror posting: line start, lone column-zero `>`, Unicode space or `(`;
  no escapes, URLs or email. Code-span/fence checks are conservative.
  Plain Enter/Tab pick; modified keys pass through except Ctrl/Cmd+Enter closes
  and follows the send setting. Blur forgets dismissal; refocus searches.
  Composition hides it; Enter has a 100 ms guard after commit.
  Picks use `setRangeText` and dispatch `input` for drafts and typing.
- **Drafts:** [below](#drafts).
- **Send key** (#1158): server-stored preference (`account.send_key`, #1175) read
  from a rendered attribute, never browser storage.

### Drafts

Only `web/src/message-drafts.ts` may use `localStorage` (checked). Pure rules
live in `web/src/drafts.ts` (`web/test/drafts.test.mjs`) and the thin adapter
`web/src/message-drafts.ts` owns storage, timers, events and marker `hidden`:
cleanup (#1178, #1181), saving (#1186), feed/topic restore (#1162), never history.
Restore seeds `at` without input/typing/focus;
[Events](web-javascript.md#javascript): server text wins; Edit saves; Remove clears
its unchanged snapshot. Snapshot keys survive 409 rotation.
At dispatch, save raw text and its send key before the request leaves or the
textarea empties (#1328), including in-flight reload/navigation. Restore and
reuse the key only with valid unchanged text (`restoredSendKey`, tested in
`web/test/drafts.test.mjs`); any edit or emptying renews it. A 200 or stored-key
confirmation clears the saved key while keeping newer text. Only the latest send
dispatched in a place is kept: an unconfirmed item followed by another send loses
its key on reload, as its text did before.
Retry keeps unsaved edits. In-place 409 confirms recovered prefixes;
dirty cleanup keeps edit time/debounce. Lost replies can leave drafts (#1177).

- **Key and value (`web/test/drafts.test.mjs`):**
  `ribbitto.draft.v1:<member>:<session>:<channel>:<place>` (lowercase UUIDs;
  UUIDv7 session; `feed` or topic ID) and JSON
  `{member, session, expires, text, at, key}` (≤4,000 code points, 50 entries
  per member/session, `at` <7 days old and ≤1 day ahead, checked at load).
  Restore a send key only for the current member/session/place. `expires` and
  `at` are Unix milliseconds; `key` is a send UUID or null, and older entries
  may omit it.
- **Saving and cap (`web/test/drafts.test.mjs`):** latest htmx forms carry
  `data-draft-channel`/optional `data-draft-topic` outside state swaps.
  Tab-monotonic edit times (`nextEditTime`), 300 ms debounce, `pagehide`/hidden
  flush, wait for `compositionend`; clean tabs never flush. Blank/over-limit
  text (`normalizeDraftText`) and confirmed blank text write empty tombstones at
  edit time; saving never deletes the key. Tombstones restore no text, count
  toward 50, and persist until eviction, session end or retention cleanup.
  Equal/later stored edits win (`shouldWriteDraft`), and a skipped equal/later
  write leaves the tab clean; own entries trim by edit time then key to 49
  before insert and 50 after (`selectKeysToTrim`), best effort without reading
  or writing foreign keys. Overlapping writes can retain older text, and
  eviction can remove a concurrent update; neither race is repaired, textarea
  text stays unchanged, and only subsequent input saves again.
- **Stopping and expiry (`web/test/drafts.test.mjs`):** sign-out notices
  (`ribbitto.signed-out.v1`) call `stop()`, which only cancels saves and stops
  writes; either expiry clock (`checkSessionExpired`:
  `performance.now() >= expires - rendered` or `Date.now() >= expires`, latched
  against rollback with chained `nextExpiryDelay` waits) also removes the
  session's keys. Both stop writes until reload; expiry removes session keys
  unread, preserving page text and sending, and storage errors leave
  typing/sending usable. The monotonic deadline uses the document time origin;
  chained waits do not poll. An ahead device clock may expire drafts early
  (#1798).
- **Cleanup (`web/test/drafts.test.mjs`):** once per document load (never on
  bfcache restore), first match per key wins: `sweepKeys` removes malformed
  `ribbitto.draft.v1:` keys, own-session keys under another member, invalid own
  entries (own values are removed for invalid JSON, missing or mistyped required
  fields, member/session/expiry mismatch, or the stated text/time bounds), and
  foreign sessions whose 48-bit millisecond prefix strictly precedes the horizon
  (leaving same-millisecond or later foreign sessions unread). The sign-in page
  uses `data-drafts-horizon` (`identity.SessionHorizon`, PostgreSQL `uuidv7()`)
  when signed out and writes `ribbitto.signed-out.v1`; organisation pages use
  `#organization-stream`'s `data-member`, `data-session`, `data-session-expires`
  and `data-rendered-at`.
- **Sign-out:** a successful `POST /signout` adds `Clear-Site-Data: "storage"`
  where supported (Chromium); the sign-in page covers the rest.

[d37]: ../decisions/37-composer-javascript-pending-posts-keys-and-drafts.md
