# Web composer pending posts

Pending-post rules for the [web composer](web-composer.md#composer).

## Pending own posts

(#1156, #1188, #1174, #1180):
[decision 37 (a)][d37] defines the state, lifetime, rendering and arrival
rules. Latest feeds/topics use `#message-pending-template` and `#message-pending`,
after `#message-items`. Each item's htmx form holds original text,
key, generation and shown cursor. Composer clears immediately, keeps
focus, takes `crypto.randomUUID()`. Each queued form gets a fresh UUID unless
Edit, a device draft or the Remove hold supplies one.
At most 10 items per tab in all states, including failures
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
Failed responses never swap; the [failure classes](web-composer.md#composer) fill only
the item's empty polite status. Retry uses form text/key,
even after an unconfirmed send's refused retry. A 409
shows `message.error.repeat` and renews the item's key. Remove discards
the item with the [hold below](#remove-hold); an uncertain committed post
can still arrive later.
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

## Remove hold

Remove keeps an unconfirmed item's raw text and key only in page memory, scoped
to its feed/topic, for five minutes (`removedKeyRetention`, one named constant).
Identical raw text sent in that place before expiry reuses the key; different
text takes a fresh key. Expiry, confirmation or `pagehide` clears the hold.
Remove then reload loses the held identity, even within five minutes; a stored
message is visible in the conversation (#1328).

[d37]: ../decisions/37-composer-javascript-pending-posts-keys-and-drafts.md
