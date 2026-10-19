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
- **Pending own posts** (#1156, #1188, #1174, #1180):
  [decision 37 (a)][d37] defines the state, lifetime, rendering and arrival
  rules. Latest feeds/topics use `#message-pending-template` and `#message-pending`,
  after `#message-items`. Each item's htmx form holds original text,
  key, generation and shown cursor. Composer clears immediately, keeps
  focus, takes `crypto.randomUUID()`. Each queued form gets a fresh UUID unless
  Edit restored it. At most 10 items per tab in all states, including failures
  and confirmed items awaiting delivery. At capacity a localised alert keeps
  the next text; Retry takes no slot. Delivery, move-out, Remove and Edit free slots.
  FIFO: one form at a time, predecessor classified first.
  Ordinary requests block composer submits; pending items or requests block
  quoted or invalid ordinary submits, preventing overlap between forms' requests.
  A Not sent item lets later items continue; Retry moves it to the list and
  queue's end. Unknown outcomes pause later items (showing Sending).
  Same-key Retry keeps its place; success resumes dispatch. A refused retry
  keeps the pause while the earlier outcome is unknown. Remove and Edit resume
  without resolving that request: it may commit later; real delivery stays
  visible. Storage follows send order on uninterrupted success or
  successful same-key Retry; Remove or Edit may reorder it.
  200 swaps composer state and out-of-band typing form, sets
  `aria-invalid="false"` and keeps `aria-describedby="message-help composer-quote-description"` and the
  textarea text.
  `data-posted-message` links the item to delivery. A `MutationObserver` removes
  confirmed items before paint when their ID appears in `#message-items`.
  Stream-first briefly shows both until the response; replay and feed moves
  preserve the real ID. Topic responses also remove the item if its
  `data-posted-topic` differs, or its ID is absent and `data-applied-cursor`
  has reached `data-posted-seq`. Read attributes once, never keep them
  on the item. Once confirmed, `htmx:sseBeforeMessage` removes it when
  a `messages-moved` payload names its ID with this topic as source.
  Ordering allows removal of absent, already-applied posts
  ([topic replay](replay.md#ordering-and-replay)).
  Failed responses never swap; the failure classes above fill only
  the item's empty polite status. Retry uses form text/key,
  even after an unconfirmed send's refused retry. A 409
  shows `message.error.repeat` and renews the item's key. Remove discards
  item and text; an uncertain committed post can still arrive later.
  Edit requires either failure, an empty composer and no IME
  composition; otherwise visible but disabled with an associated
  localised explanation. It never overwrites a draft. It restores raw
  text, key and any unconfirmed mark, focuses the textarea with caret at
  end, then removes the item. The key follows text across state swaps
  until sent/emptied; its unconfirmed mark stays in composer
  `data-unconfirmed-key` for either path. A pending resend transfers
  it to its form and clears the composer's mark; emptying restored
  text clears it and takes a fresh UUID.
  Editing a known refusal clears stale composer marks.
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

Only `message-drafts-vN.js` may use `localStorage` (checked).
`message-drafts-v4.js`: cleanup (#1178, #1181), saving (#1186), feed/topic
restore (#1162), never history. Restore seeds `at` without input/typing/focus;
[Events](web-javascript.md#javascript): server text wins; Edit saves; Remove clears
its unchanged snapshot. Snapshot keys survive 409 rotation.
Retry keeps unsaved edits. In-place 409 confirms recovered prefixes;
dirty cleanup keeps edit time/debounce. Lost replies can leave drafts (#1177).

- **Key:** `ribbitto.draft.v1:<member>:<session>:<channel>:<place>`, in
  canonical lower-case UUIDs; session: UUIDv7 id; place: `feed` or topic ID.
- **Value:** JSON `{member, session, expires, text, at}`; `expires` and `at`
  are Unix milliseconds. Limit: 4,000 code points, 50 entries per member
  and session. Retention: last edit (`at`), checked only at load.
- **Saving:** latest htmx forms carry `data-draft-channel`/optional
  `data-draft-topic` outside state swaps. Tab-monotonic edit times, never save
  times; 300 ms debounce, `pagehide`/hidden flush, wait for `compositionend`.
  Clean tabs never flush. Blank/over-limit input and confirmed blank text write
  empty tombstones at edit time, never delete keys. Tombstones restore nothing,
  count toward 50; only eviction, session end or retention removes them.
- **Races and cap:** pre-write reads skip equal/later stored edits, leaving
  the tab clean. Sort own entries by edit time (oldest first), then key;
  trim other entries to 49 before insertion, all to 50 after every write.
  Both rules are best effort on observed values: overlapping reads may
  leave older text; eviction may remove a concurrent update. No repair;
  textarea unchanged. Only next input saves again.
  Foreign values unread, keys unwritten.
- **Stopping:** sign-out notices and deadline cancel pending saves,
  stop writes until reload; every save checks the deadline. Storage errors
  leave typing/sending usable.
- **Notice:** `ribbitto.signed-out.v1` holds only the horizon;
  every write fires `storage` in other tabs.
- **Millisecond rule:** cleanup removes malformed `ribbitto.draft.v1:` keys
  and entries whose session's 48-bit millisecond prefix (first 12 hex digits)
  strictly precedes its horizon's. Foreign sessions at/after that millisecond
  stay unread: stale pages cannot remove newer sessions' entries.
- **Sign-in page:** `data-drafts-horizon` is PostgreSQL's `uuidv7()`
  (`identity.SessionHorizon`, session-id clock), only when
  signed out, including sign-in errors. Cleanup then notice run once per
  document load, never on back-forward cache restore. Storage errors keep it usable.
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
- **Sign-out:** a successful `POST /signout` adds `Clear-Site-Data: "storage"`
  where supported (Chromium); the sign-in page covers the rest.

[d37]: ../decisions/37-composer-javascript-pending-posts-keys-and-drafts.md
