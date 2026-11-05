# Web state and swap zones

State lifetime and swap ownership contract.

## State classes

| Class | Examples | Owner | Kept across | Discarded on |
|---|---|---|---|---|
| S server-authoritative | messages, membership, read ranges, send-key preference | server | — | — |
| P projection | message list (DOM), unread badges, pins seq | zone's primary owner | unrelated swaps | its own reconciliation; `reset` |
| U transient UI | textarea text, selection, IME, picker token, scroll anchor | textarea or feature controller | every swap | element removal, page end |
| O async operation | pending queue, in-flight send, unconfirmed and restored keys, Remove hold, read retry, resync request | operation owner | every swap, except one carrying that operation's outcome | its outcome; `pagehide` for holds; reload (text survives as a draft) |
| C connection | page cursors, connection state, status | Progress, stream client | reconnects, bfcache | `reset` |
| D device | drafts, UX settings | drafts owner, panel controller | reloads | sign-out, session expiry, 7 days, eviction |

## Swap zones

The server never replaces `#message-body`, `#message-pending`,
`#message-composer` or `#organization-stream`.

| Zone | Replaced by | Primary owner | Allowed secondary reactions |
|---|---|---|---|
| `#message-items` children | `message`, `messages-moved` | conversation applier | composer (`Delivery`), branch constraints, pins controls, enhancers, unread divider, read |
| `#message-items` head, `#load-older` | Load older | conversation applier | enhancers |
| `#sidebar-channels` | `sidebar` frame or resync (OOB via `#stream-sink`) | unread owner | draft markers, arrival mark, sidebar scroll |
| `#topic-heading-count` | the same, OOB | unread owner | — |
| `#presence-<id>` | `presence` frame, OOB | none | — |
| `#typing-status` | `typing` frame | none | — |
| `#pins-section` | `pins` frame; pin response OOB | pins owner | focus restore |
| pin control forms | `pins-sync.ts` `htmx.ajax` | pins owner | — |
| `#message-composer-state` | ordinary and pending POSTs | composer controller | drafts, post scroll |
| typing form | the same POSTs, OOB | none | — |
| `#composer-quote` | `#quote-form`; composer POST OOB | composer controller | composer growth |
| `#mention-listbox` | `#mention-search` | mention picker | — |
| `#branch-feedback` | branch POST | none | — |

Rules:

1. Only the primary owner rebuilds a zone's projection.
2. Secondary reactions refresh only their own display.
3. No swap resets U or O state, except one that carries that operation's own
   outcome.
4. A new zone adds a row.

