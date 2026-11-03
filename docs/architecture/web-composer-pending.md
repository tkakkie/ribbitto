# Web composer pending posts

Pending-post rules for the [web composer](web-composer.md#composer).
[`web/src/composer-queue.ts`](../../web/src/composer-queue.ts) and its
[table-driven tests](../../web/test/composer-queue.test.ts) specify transitions,
refusals, ordering and identity; `web/src/composer.ts` adapts these decisions to
htmx and the existing pending DOM. It clones the server-provided template and sets
textContent, field values, hidden and contract attributes with DOM APIs. Each typed
item has held form/item references in the adapter; no pending-state query or DOM read-back occurs.
The adapter supplies `now`, `newKey` and a place identity covering channel and
feed/topic. Timers call `expire(item)` to clear only their own hold even with a
frozen clock. Remove sweeps aged holds; admission consumes only its
own hold and checks age only without a restored key. `pagehide`/`sent` clear holds.
Before `admit`, the adapter calls `changeText` with a renewal callback that clears
the uncertainty mark before generating/writing the composer key, including when
admission returns busy or ordinary. The fallback key clears only after that write.
Only successful admission calls the adapter's `prepare` callback to clone/copy the
form, before key generation and the synchronous projection notification.
That released key belongs to the composer; a queued item instead takes the restored
key, else an unexpired Remove hold for the same place and raw text, else a fresh
`newKey()`. As in the legacy composer, enqueue rotates the composer's own key,
discarding any released key. Items expose readonly views; `restoration` returns a
readonly snapshot. Falsy draft-event keys do not arm restoration; sent events
still compare their raw identity exactly, including null and undefined.

## Pending own posts

(#1156, #1188, #1174, #1180):
[decision 37 (a)][d37] defines the state, lifetime, rendering and arrival
rules. Latest feeds/topics use `#message-pending-template` and `#message-pending`,
after `#message-items`. Each item's htmx form holds original text,
key, generation and shown cursor. Composer clears immediately, keeps
focus, takes `crypto.randomUUID()`. Each queued form gets a fresh UUID unless
Edit, a device draft or the Remove hold supplies one.
Admission limits, FIFO dispatch, failure classification, Retry placement and
restored identity are specified by the table-driven queue tests above.
`internal/web/composer_pending_browser_test.go` covers request ordering,
capacity, Retry, Edit, delivery races and live composer-state swaps with real htmx.
The typed item's `postedMessage` links it to delivery; `data-posted-message` is
an output for tests. A `MutationObserver` removes
confirmed items before paint when their ID appears in `#message-items`.
Stream-first briefly shows both until the response; replay and feed moves
preserve the real ID. Topic responses also remove the item if its
`data-posted-topic` differs, or its ID is absent and `data-applied-cursor`
has reached `data-posted-seq`. This also clears the busy alert for a removed
in-flight item; delivered or still-pending detached responses leave it alone.
Read attributes once, never keep them
on the item. Once confirmed, `htmx:sseBeforeMessage` removes it when
a `messages-moved` payload names its ID with this topic as source.
Ordering allows removal of absent, already-applied posts
([topic replay](replay.md#ordering-and-replay)).
Failed responses never swap; their localised notice stays on the item.
The queue tests specify status handling and immutable `draftKey` across 409
`requestKey` rotation. Browser tests verify raw text, caret/focus, draft
protection and restoration across swaps. Ordinary sending retains the
[failure classes](web-composer.md#composer) and [retry rule](posting.md).

The module's `holdsReload(items)` is true exactly for queued, sending or failed
items, regardless of uncertainty; confirmed items do not hold reload. Its
`changed` callback runs synchronously after queue mutations, including last-item
Remove, Edit and delivery, so listeners see the completed transition once.
On 409 the adapter stages failed-state/uncertainty cleanup before generating
the rotated key, preserving DOM order even if the generator throws. The request
lock releases only after rotation succeeds; pending rotation requires a UUID
generator, while restoration release retains the ordinary HTTP fallback.
The synchronous subscription writes form `data-state`/`data-unconfirmed` and item `data-posted-message`
as output-only projections; `draftKey` stays in memory and is never rendered.
It publishes `holdsReload(items)` through the [contract events](web-contracts.md),
including notifications without attribute changes and removals. A query/reply
handshake supplies late listeners. The guard retains its ordinary-request and
alert clauses, timer, notice and reload behaviour. Browser tests rewrite pending
projections externally to prove that decisions and reset holds stay queue-driven.

## Remove hold

The queue tests specify the five-minute, page-local Remove hold, exact raw-text
and place matching, expiry, confirmation and `pagehide` cleanup.
Remove then reload loses the held identity, even within five minutes; a stored
message is visible in the conversation (#1328).

[d37]: ../decisions/37-composer-javascript-pending-posts-keys-and-drafts.md
