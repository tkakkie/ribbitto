# Web composer

## Composer

Bounded state ([decision 37][d37]): keys, send glue, feed/topic pending items.

Idle ordinary path: blank/quoted text, >4,000 normalised/trimmed code points,
or no `crypto.randomUUID()`.

- **Ordinary sending** (input glue, #1173): textarea usable in flight:
  answers replace only `#message-composer-state`. 200 removes sent body
  (htmx request parameters) only if still a textarea prefix, via
  `setRangeText("", 0, length, "preserve")`: caret/selection stay on
  remaining text; no `input`, hence no typing signal.
  A `WeakMap` waits for `compositionend`, retaining the confirmation ID before
  stream settle. Answers copy `data-invalid` to textarea
  `aria-invalid`.
- **Ordinary failures** (#1179) keep the text and the key; `#message-send-alert` says
  which. 400/404/413/422 reject before writing; 429 by spec:
  *not sent*, or with the mark, *this retry was refused and the
  earlier send is still unconfirmed*. Network errors, timeouts, 5xx or
  other 4xx (except 409) may follow a commit: *could not confirm*, marking the
  key in `data-unconfirmed-key`. Only 200/409 follow the
  key lookup; both renew the key. The mark clears exactly on key change.
  A 409 carries `data-sent-body` and omits its error only in the same channel,
  neither body quoted, and the stored typed-body byte length/SHA-256 verify
  the retry prefix without resolving handles. It removes that prefix by the
  same rules (*the earlier message was sent*), or says the 409 error if changed.
  Otherwise text/error stay, including legacy rows without proof.
- **Pending own posts** (#1156, #1188, #1174, #1180):
  [Decision 37 (a)][d37]: state, lifetime, rendering, arrival. Latest feeds/topics
  use `#message-pending-template` and `#message-pending` after `#message-items`.
  Each item's htmx form holds raw text,
  key, generation and shown cursor. Composer clears, keeps focus, takes
  `crypto.randomUUID()`. Queued forms get fresh UUIDs unless restored or held
  after Remove (#1328). At most 10 items per tab, including failures
  and confirmed items awaiting delivery. At capacity a localised alert keeps
  text; Retry takes no slot. Delivery/move-out/Remove/Edit free slots.
  FIFO: classify predecessors first; one request at a time. Ordinary requests
  block submits; pending items/requests block quoted/invalid ordinary submits.
  Not sent lets later items continue; Retry moves it to the list/queue's end.
  Unknown outcomes pause later items (showing Sending).
  Same-key Retry keeps its place; success resumes dispatch. Refusal keeps the
  pause while the earlier outcome is unknown. Remove/Edit resume
  without resolving the request: it may commit later; delivery stays visible.
  Storage follows send order on uninterrupted success or
  successful same-key Retry; Remove or Edit may reorder it.
  200 swaps composer state and out-of-band typing form, sets
  `aria-invalid="false"` and keeps `aria-describedby="message-help composer-quote-description"` and the
  textarea text.
  `data-posted-message` links delivery. A `MutationObserver` removes confirmed
  items before paint when their ID appears in `#message-items`.
  Stream-first briefly shows both until the response; replay and feed moves
  preserve the real ID. Topic responses also remove the item if its
  `data-posted-topic` differs, or its ID is absent and `data-applied-cursor`
  has reached `data-posted-seq`. Read attributes once, never keep them
  on the item. Once confirmed, `htmx:sseBeforeMessage` removes it when
  a `messages-moved` payload names its ID with this topic as source.
  Ordering proves absent applied posts moved ([topic replay](replay.md#ordering-and-replay)).
  Failed responses never swap; the failure classes above fill only
  the item's empty polite status. Retry uses form text/key,
  even after an unconfirmed send's refused retry. A 409
  shows `message.error.repeat` and renews the item's key. Remove drops the item
  and draft snapshot. For unconfirmed items it holds raw text/key in memory
  for 5 minutes, in this feed/topic only: identical text reuses the key;
  different text or at/after the limit gets a fresh key. Pagehide drops the hold.
  Remove then reload loses it even within 5 minutes; a stored post is visible.
  Edit requires failure, empty composer and no IME; otherwise visible but
  disabled with a localised description. It never overwrites drafts. It restores
  raw text/key/unconfirmed mark, focuses the end, removes the item.
  The key follows text across state swaps
  until sent/emptied; its unconfirmed mark stays in composer
  `data-unconfirmed-key` for either path. A pending resend transfers
  it to its form and clears the composer mark; emptying clears it and renews the
  UUID. Edit of a known refusal clears stale marks.
  Unused keys post normally; used keys return the stored message for a body
  matching the [retry rule](posting.md), otherwise that pending 409.
  Pending sends bypass prefix removal, ordinary send alerts/failure
  classification and 409 cleanup.
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

Only `message-drafts-vN.js` uses `localStorage` (checked).
`message-drafts-v6.js`: cleanup, saving, feed/topic restore, never history.
Restore seeds `at` without input/typing/focus;
[Events](web-javascript.md#javascript): server text wins; Edit saves; Remove clears
its unchanged snapshot. Snapshot keys survive 409 rotation.
Retry keeps unsaved edits. In-place 409 confirms recovered prefixes;
dirty cleanup keeps edit time/debounce. Dispatch saves text/key before the request
leaves (#1328), including in-flight navigation/reload. Restore uses that key only
while text is unchanged; any edit/emptying renews it. Matching stored-message
confirmation clears the saved key; several tabs may safely send one key.

- **Key:** `ribbitto.draft.v1:<member>:<session>:<channel>:<place>`, in
  canonical lower-case UUIDs; session: UUIDv7 id; place: `feed` or topic ID.
- **Value:** JSON `{member, session, expires, text, at, key}`; `key` is a send
  UUID or null (older entries may omit it); `expires` and `at`
  are Unix milliseconds. Limit: 4,000 code points, 50 entries per member
  and session. Retention: last edit (`at`), checked only at load.
- **Saving:** latest htmx forms carry `data-draft-channel`/optional
  `data-draft-topic` outside state swaps. Tab-monotonic edit times, never save
  times; 300 ms debounce, `pagehide`/hidden flush, wait for `compositionend`.
  Clean tabs never flush. Blank/over-limit input and confirmed blank text write
  empty tombstones at edit time, never delete keys. Tombstones restore nothing,
  count toward 50; only eviction, session end or retention removes them.
- **Races and cap:** equal/later stored edits win pre-write reads; tab stays clean.
  Sort own entries by edit time (oldest first), then key;
  trim other entries to 49 before insertion, all to 50 after every write.
  Best effort: overlapping reads may leave older text; eviction may remove a
  concurrent update. No repair; textarea unchanged. Next input saves again.
  Foreign values unread, keys unwritten.
- **Stopping:** sign-out notice/deadline cancel saves and stop writes until
  reload; every save checks the deadline. Storage errors leave typing/sending usable.
- **Notice:** `ribbitto.signed-out.v1` holds only the horizon;
  every write fires `storage` in other tabs.
- **Millisecond rule:** cleanup removes malformed `ribbitto.draft.v1:` keys
  and entries whose session's 48-bit millisecond prefix (first 12 hex digits)
  strictly precedes its horizon's. Foreign sessions at/after that millisecond
  stay unread: stale pages cannot remove newer sessions' entries.
- **Sign-in page:** only signed-out renders (including errors) carry
  `data-drafts-horizon`: PostgreSQL `uuidv7()` from `identity.SessionHorizon`
  (session-id clock). Cleanup then notice run once/load, never on back-forward
  cache restore. Storage errors leave the page usable.
- **Organisation pages:** channel/list pages' `#organization-stream` has
  `data-session` (id, never token/hash), `data-session-expires` and
  `data-rendered-at` (Unix milliseconds) beside `data-member`. Once per load,
  first match per draft key wins: malformed or own session
  with another member, removed; foreign session, removed if earlier by the
  millisecond rule, else unread; own values, removed if invalid JSON, fields
  missing/mistyped, member/session/expiry mismatched, over 4,000 code points,
  or `at` at least 7 days old or over a day ahead. At the deadline (expiry
  minus render time; `performance.now()` from document time origin),
  remove own session's keys unread.
- **Sign-out:** successful `POST /signout` adds `Clear-Site-Data: "storage"`
  (Chromium); sign-in cleanup covers other browsers.

[d37]: ../decisions/37-composer-javascript-pending-posts-keys-and-drafts.md
