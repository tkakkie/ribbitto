# Web composer

Composer JavaScript and drafts on the device.

## Composer

[Decision 37][d37] allows bounded composer state. The server-rendered key and
the sending glue and feed/topic pending items below exist; the parts marked
*planned* await their issues.

The ordinary path applies to idle feed/topic sends with blank
text, a selected quote, or over 4,000 code points after line-ending
normalisation and trimming. Contexts without `crypto.randomUUID()` also use it.

- **Ordinary sending** (input glue, #1173): the textarea stays usable while a post is
  in flight, since answers replace only `#message-composer-state`. On a 200
  the script removes the sent body (htmx's request parameters) from the
  textarea's start, only if it still starts with it, with
  `setRangeText("", 0, length, "preserve")`: the caret and selection stay on
  the remaining text, and no `input` event fires, so no typing signal
  follows. During an IME composition it waits for `compositionend`, keeping
  that one removal per textarea in a `WeakMap`. Every answer sets the
  textarea's `aria-invalid` from the state's `data-invalid`.
- **Ordinary failures** (#1179) keep the text and the key; `#message-send-alert` says
  which. The handler rejects 400/404/413/422 before any write; 429 rejects
  by spec: *not sent*, or with the mark, *this retry was refused and the
  earlier send is still unconfirmed*. A network error, a timeout, a 5xx or
  another 4xx (except 409) may follow a commit: *could not confirm*, and the
  key is marked in `data-unconfirmed-key`. Only a 200 or a 409 follows the
  key lookup; both bring a new key, and the mark goes
  exactly when the key changes. A 409 carries `data-sent-body`, and omits
  its error, only in the same channel, with neither the stored nor the retried
  text quoted, and when the stored typed-body byte length and SHA-256 digest
  verify that prefix of the retry text (without resolving handles): the
  script removes that prefix under the same rules (*the earlier message was
  sent*), or says the 409's error if the start was edited since. Otherwise
  the text stays with the error, including legacy rows without a proof.
- **Pending own posts** (#1156, #1188, #1174, #1180):
  [decision 37 (a)][d37] defines the state, lifetime, rendering and arrival
  rules. Latest feeds/topics use `#message-pending-template` and `#message-pending`,
  after `#message-items`. The item's own htmx form holds the original text,
  key, generation and shown cursor. The composer empties immediately, keeps
  focus and takes `crypto.randomUUID()`. Each queued form gets a fresh UUID
  unless restored by Edit. Up to 10 items per tab count in every state, including failures
  and confirmed items awaiting delivery. At capacity a localised alert keeps
  the next text; Retry takes no slot. Delivery, move-out, Remove and Edit free slots.
  FIFO dispatch sends one form at a time, after classifying its predecessor.
  An ordinary request blocks composer submits; pending items or requests block
  quoted or invalid ordinary submits, so the two forms' requests never overlap.
  A Not sent item lets later items continue; Retry moves it to the list and
  queue's end. An unknown outcome pauses later items (still showing Sending).
  Its same-key Retry keeps its place; success resumes dispatch. A refused retry
  keeps the pause while the earlier outcome is unknown. Remove and Edit resume
  without resolving that request: it may commit later, and its real delivery stays
  visible. Storage order follows send order on uninterrupted success or a
  successful same-key Retry, but may differ after Remove or Edit.
  A 200 swaps the composer state and out-of-band typing form, sets
  `aria-invalid="false"` and keeps `aria-describedby="message-help composer-quote-description"` and the
  textarea text.
  Its `data-posted-message` associates
  the item with delivery. A `MutationObserver` removes a
  confirmed item before paint when that ID appears in `#message-items`.
  Stream-first briefly shows both until the response; replay and feed moves
  preserve the real ID. On topics, a response also removes the item if its
  `data-posted-topic` differs, or its ID is absent and `data-applied-cursor`
  has reached `data-posted-seq`. Those attributes are read once, never kept
  on the item. After confirmation, `htmx:sseBeforeMessage` removes it when
  a `messages-moved` payload names its ID with this topic as source.
  Ordered application makes an absent, already-applied post safe to remove
  ([topic replay](replay.md#ordering-and-replay)).
  Failed responses never swap. The same failure classes as above fill only
  the item's empty polite status. Retry uses its form's
  text and key, including after an unconfirmed send's refused retry. A 409
  shows `message.error.repeat` and renews the item's key. Remove discards
  the item and text; an uncertain committed post can still arrive later.
  Edit is available on either failure only with an empty composer outside IME
  composition; otherwise it stays visible but disabled with an associated
  localised explanation. It never overwrites a draft. It restores the raw
  text, key and any unconfirmed mark, focuses the textarea with the caret at
  the end, then removes the item. The key follows that text across state swaps
  until sent or emptied; its unconfirmed mark waits in the composer's
  `data-unconfirmed-key` for either sending path. A pending resend transfers
  it to its own form and clears the composer's mark; emptying the restored
  text clears it and takes a fresh UUID.
  Editing a known refusal clears any stale composer mark.
  An unused key posts normally; a body matching the [retry rule](posting.md)
  under a used key returns the stored message; a different body gets the pending 409 above.
  Pending sends bypass prefix removal, the ordinary send alert, ordinary
  failure classification and 409 cleanup.
- **Idempotency key** (#1155): the hidden `idempotency_key`, rendered by
  the server fresh on the initial render, after success and with 409, kept
  on 422. Scoped by the server to the authorised membership; never in a
  stream payload. On a pending send the script replaces it with
  `crypto.randomUUID()` (secure contexts only; otherwise ordinary sending).
- **Stream reset:** the [reload guard](web-javascript.md#javascript) reads form
  `data-state`, the composer's request class and #1179's failure alert. Queued, sending
  and failed items hold reload; confirmed items do not. Retry success, Remove
  or Edit can release it: Edit's restored text is a draft. Each dispatched
  request has a 30 s timeout; queues, retries and failed text can extend the hold.
- **Quote chip:** `#composer-quote-description` holds only the quoted author
  and excerpt and stays present, empty, without a quote. Quote and Remove
  quote focus the textarea after the chip swap; Quote closes its actions
  popover. Chip height and its gap reduce the [growth cap](../ui-composer.md);
  the existing resize observer preserves the reader on chip changes. Without
  scripts both submit a GET with `#message-composer`; removal loses unsent text.
- **Mention picker:** `mention-picker-v1.js` enhances all composers.
  `#mention-search` uses htmx's 120 ms debounce,
  replace sync and `outerHTML` into `#mention-listbox`. Only matching `data-query`
  responses open; picks recheck the token. Empty results hide the picker;
  `#mention-status` announces no members. `@` requests eight members.
  Token starts mirror posting: line start, lone column-zero `>`, Unicode space or `(`;
  no escapes, URLs or email. Code-span/fence checks are conservative.
  Plain Enter/Tab pick; modified keys pass through except Ctrl/Cmd+Enter closes
  and follows the send setting. Blur forgets dismissal; refocus searches.
  Composition hides the picker; Enter has a 100 ms guard after commit.
  Picks use `setRangeText` and dispatch `input` for drafts and typing.
- **Drafts:** [below](#drafts).
- **Send key** (planned, #1158): a server-stored preference (`account.send_key`, #1175) read
  from a rendered attribute, never browser storage.

### Drafts

Only `message-drafts-vN.js` may use `localStorage` (the pattern check).
`message-drafts-v3.js` cleans up (#1178, #1181) and saves latest feed/topic
input (#1186), never on `?before=`/`?around=`. Restore, confirmed-send clearing
(#1162) and sidebar markers (#1177) remain planned. Sent text stays until
another edit or cleanup (decision 37's temporary plaintext copy).

- **Key:** `ribbitto.draft.v1:<member>:<session>:<channel>:<place>`, in
  canonical lower-case UUIDs; session is its UUIDv7 id, place is `feed` or
  the topic's ID.
- **Value:** JSON `{member, session, expires, text, at}`; `expires` and `at`
  are Unix milliseconds. Bounds: 4,000 code points and 50 entries per member
  and session. Retention uses last edit (`at`), checked only at document load.
- **Saving:** latest htmx forms carry `data-draft-channel` and optional
  `data-draft-topic`, outside state swaps. Snapshots use tab-monotonic input
  times, never save times; debounce 300 ms, flush on `pagehide`/hidden, and
  wait for `compositionend`. Clean tabs never flush. Blank/over-limit input
  and future clearing write empty-text tombstones with the edit's time,
  never delete keys. Tombstones restore nothing,
  count toward 50, and leave only by eviction, session ending or retention.
- **Races and cap:** a read just before writing skips equal/later stored
  edits, leaving the tab clean. Own entries sort by oldest edit time then
  key: other entries trim to 49 before insertion, all to 50 after every write.
  Both rules are best effort against observed values: overlapping reads may
  leave older text; eviction may remove a concurrent update. Neither is
  repaired. The textarea stays; only the next input saves again, never a
  clean lifecycle flush. Foreign values stay unread and keys unwritten.
- **Stopping:** sign-out notices and the deadline cancel pending saves and
  stop writes until reload; each save checks the deadline. Storage errors
  leave typing and sending usable.
- **Notice:** `ribbitto.signed-out.v1` holds the horizon, without content;
  each write fires a `storage` event in other tabs.
- **Millisecond rule:** cleanup removes malformed `ribbitto.draft.v1:` keys
  and entries whose session's 48-bit millisecond prefix (first 12 hex digits)
  strictly precedes its horizon's. Foreign sessions in the same millisecond
  or later stay unread: a stale page cannot remove a newer session's entry.
- **Sign-in page:** `data-drafts-horizon` is PostgreSQL's `uuidv7()`
  (`identity.SessionHorizon`, the session-id clock), present only when
  signed out, including sign-in errors. Cleanup then notice run once per
  document load, never on back-forward cache restore. Storage errors leave
  the page usable.
- **Organisation pages:** channel/list pages' `#organization-stream` has
  `data-session` (id, never token/hash), `data-session-expires` and
  `data-rendered-at` (Unix milliseconds) beside `data-member`. Once per load,
  the first matching rule decides each draft key: malformed or own session
  with another member, removed; foreign session, removed if earlier by the
  millisecond rule, else unread; own values, removed if invalid JSON, fields
  missing/mistyped, member/session/expiry mismatched, over 4,000 code points,
  or `at` at least 7 days old or over a day ahead. At the deadline (expiry
  minus render time, using `performance.now()` from the document's time
  origin) the open page removes its own session's keys unread.
- **Sign-out:** a successful `POST /signout` adds `Clear-Site-Data: "storage"`
  where the browser supports it (Chromium); the sign-in page covers the rest.

[d37]: ../decisions/37-composer-javascript-pending-posts-keys-and-drafts.md
