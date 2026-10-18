# Web composer

Composer JavaScript and drafts on the device.

## Composer

[Decision 37][d37] allows bounded composer state. The server-rendered key and
the sending glue and feed/topic pending items below exist; the parts marked
*planned* await their issues.

The ordinary path applies to idle feed/topic sends with blank
text or over 4,000 code points after line-ending
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
  its error, only when the retried text started with the stored body: the
  script removes that body under the same rules (*the earlier message was
  sent*), or says the 409's error if the start was edited since. Otherwise
  the text stays with the error.
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
  invalid ordinary submits, so the two forms' requests never overlap.
  A Not sent item lets later items continue; Retry moves it to the list and
  queue's end. An unknown outcome pauses later items (still showing Sending).
  Its same-key Retry keeps its place; success resumes dispatch. A refused retry
  keeps the pause while the earlier outcome is unknown. Remove and Edit resume
  without resolving that request: it may commit later, and its real delivery stays
  visible. Storage order follows send order on uninterrupted success or a
  successful same-key Retry, but may differ after Remove or Edit.
  A 200 swaps the composer state and out-of-band typing form, sets
  `aria-invalid="false"` and keeps `aria-describedby="message-help"` and the
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
  Edit is available on either failure only with an empty composer; otherwise
  it is disabled with an associated localised explanation. It restores the raw
  text and key, focuses the textarea with the caret at the end, then removes
  the item. The key follows that text across state swaps until sent or emptied;
  emptying it takes a fresh UUID. An unused key posts normally; an equivalent
  validated body under a used key returns the stored message; a different body
  gets the pending 409 above. Pending sends bypass prefix removal, the ordinary
  send alert, the unconfirmed-key mark and 409 cleanup.
- **Idempotency key** (#1155): the hidden `idempotency_key`, rendered by
  the server fresh on the initial render, after success and with 409, kept
  on 422. Scoped by the server to the authorised membership; never in a
  stream payload. On a pending send the script replaces it with
  `crypto.randomUUID()` (secure contexts only; otherwise ordinary sending).
- **Drafts:** [below](#drafts).
- **Send key** (planned, #1158): a server-stored preference (`account.send_key`, #1175) read
  from a rendered attribute, never browser storage.

### Drafts

Only `message-drafts-vN.js` may use `localStorage` (the pattern check).
`message-drafts-v2.js` cleans up on the sign-in page (#1178) and on
organisation pages (#1181); saving and restoring (#1162) and the sidebar
marker (#1177) are planned: a page restores only its own member and
session's unexpired entries, writes only before its session's deadline, and
only shows or hides a server-rendered sidebar marker.

- **Key:** `ribbitto.draft.v1:<member>:<session>:<channel>:<place>`, in
  canonical lower-case UUIDs; the session is its id, a UUIDv7, and the place
  the literal `feed` or the topic's ID.
- **Value:** JSON `{member, session, expires, text, at}`; at most 50 entries
  per member and session (best effort across tabs, #1186), each kept 7 days
  after its last edit (`at`).
- **Notice:** `ribbitto.signed-out.v1`, without content; its value is the
  horizon that wrote it, so each write fires a `storage` event in other tabs.
- **Millisecond rule:** a cleanup removes an entry only if its session's
  48-bit millisecond prefix (the first 12 hex digits) is strictly before its
  horizon's, plus every `ribbitto.draft.v1:` key without that format. Entries
  in the horizon's millisecond stay until expiry or a later cleanup, so a
  stale page never removes a newer session's entry, and never reads another
  session's value.
- **Sign-in page:** the form's `data-drafts-horizon` is a `uuidv7()` from
  PostgreSQL (`identity.SessionHorizon`, the clock of session ids), rendered
  only for signed-out requests, including `POST /signin`'s error renders.
  With it the script cleans up, then writes the notice, once per document
  load, never on a back-forward cache restore. A storage error leaves the
  page working.
- **Organisation pages:** the channel and channel list pages render on
  `#organization-stream` `data-session` (the session's id, never its token
  or hash), `data-session-expires` and `data-rendered-at` (Unix
  milliseconds) beside `data-member`. Once per document load the script
  decides each key under the prefix by the first rule that applies: without
  the format, or with the page's session and another member, removed;
  another session's, removed if its millisecond is before the page's, else
  kept unread; the page's own, the only values read, removed if not JSON,
  lacking or mistyping a field, differing from the key and page in member,
  session or expiry, over 4,000 code points, or with `at` 7 days old or more
  than a day ahead. At the deadline (expiry minus render time, timed with
  `performance.now()` from the document's time origin) an open page removes
  its own session's keys unread.
- **Sign-out:** a successful `POST /signout` adds `Clear-Site-Data: "storage"`
  where the browser supports it (Chromium); the sign-in page covers the rest.

[d37]: ../decisions/37-composer-javascript-pending-posts-keys-and-drafts.md
