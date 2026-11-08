# Web state and swap zones

Conversation applier, unread owner and pins owner are #2060 targets.

## Progress

`web/src/stream-progress.ts` is DOM-free; both modes take the same inputs.
The stream reports in compatible mode.
`web/src/stream-progress-adapter.ts` alone writes `data-applied-cursor`,
`data-event-cursor` and `sse-connect`'s `after`. Decoding, verification and
strict recovery remain [M10](../roadmap-m10.md) work.

### Sequence groups and cursors

Page cursors survive reconnects and bfcache for the page's lifetime.
Connection state belongs to each EventSource and resets on creation.
Sidebar (S, u), pins seq and ephemeral generations are separate sequence
groups, never compared with conversation cursors.

| Value | Meaning | Starts at | Advances on |
|---|---|---|---|
| `P0` | The page snapshot's rendered `event_seq` | Snapshot | Never changes |
| `Ap` | The highest durable id applied in the DOM, including a contract no-op | `P0` | Applied or contract no-op outcomes; stops after a failure in strict mode |
| `Sh` | The cursor the visible conversation shows | `P0` | Follows `Ap` while visible; catches up to `Ap` on hidden-to-visible, including after a terminal reset; stays put while hidden |
| `G` | The safe resume cursor used for `after` | `P0` | Applied, contract no-op or checkpoint outcomes; stops after a failure in strict mode |
| `F` | First failure, `{ reason, id }`; `id` is a decimal string when known, otherwise `null` | `null` | First failed decode, application or history re-application; later failures never replace it |
| `Uc` | The `after` in the URL the source was created with | `P0` for the first source | Set on creation; recreate or bfcache uses `G`; the model rejects an `after` above `G` |
| `Rc` | The id of the last id-bearing durable frame dispatched on that connection | Unset (`null`) | Dispatch of a durable frame with a decimal raw id; reset to `null` on source creation |

- `P0 ≤ Sh ≤ Ap ≤ G`; no page cursor decreases.
- `Uc ≤ G` at all times, because `G` is monotonic.
- Decimal strings preserve exact ids above 2^53; comparisons use `BigInt`.
- `Rc` has no general order relation to page cursors. In failure-free runs,
  `Rc ≤ G` holds after synchronous outcomes until a terminal `reset`.
  A durable reset can carry a newer cursor after the server reads skipped
  events before a gap: it freezes `G` and can leave `Rc > G` without failure.
  The adapter closes the source on reset.
- `F` keeps the first reason and a decimal id or `null`. A malformed failure
  id becomes `null`; it does not prevent strict mode from freezing progress.

### Safety condition S

**The resume point `X` sent to the server must satisfy `X ≤ G`**, so a
reconnect never skips an unapplied durable frame.

| Path | Resume point | Why S holds |
|---|---|---|
| New source: first connection, extension recreate or bfcache | The source's creation-time `Uc` | Creation requires `Uc ≤ G` |
| Native reconnect before any id-bearing durable dispatch | `Uc`, since `Rc` is unset | `Uc ≤ G` remains true as `G` advances |
| Native reconnect after an id-bearing durable dispatch | `Rc` | Requires `Rc ≤ G`: each dispatch must reach a non-failed outcome synchronously before a reconnect task runs |

Outcomes are decided within dispatch: `settleDelay: 0`, no view transitions
and no `await`. After a failure, `Rc` can exceed the safe cursor. Strict mode's
adapter must call `close()` within that dispatch to prevent native reconnect:
the model does not gate `Rc`. Compatible mode preserves the existing risk.
A resume point below `G` replays duplicates, applied idempotently by replacement
by id. Below retention, the server replays or sends the retention `reset`
from [decision 24](../decisions/24-a-cursor-that-cannot-be-served-gets-reset.md).

### Frame outcomes

Outcomes describe delivered frames only. **Sequence numbers are never checked
for continuity.** Filtering and authorization can leave gaps.

| Outcome | Meaning | Compatible mode | Strict mode |
|---|---|---|---|
| Applied | `message` or `messages-moved` passes post-application verification | Advance `Ap` and `G` to the highest id, even after failure; visible `Sh` follows `Ap` | Same, only while `F` is unset |
| Contract no-op | Move outside the loaded range or for an unshown topic; identical replacement on replay | Same as applied | Same as applied |
| Failed application | Exception or post-application verification mismatch | Record first `F`: `apply-exception` or `apply-verification`; this outcome advances no cursor | Record `F` and stop `Ap` and `G` |
| Failed decode | Missing item, or a missing, non-decimal, negative or non-increasing id on `message`, `messages-moved` or `checkpoint` | Record first `F`: `decode-missing-item`, `decode-missing-id`, `decode-invalid-id` or `decode-non-monotonic`; this outcome advances no cursor | Record `F` and stop `Ap` and `G`, even with `id: null` |
| Failed history re-application | Exception or verification mismatch when a move crossing Load older is re-applied after the response | Record first `F`: `history-reapply`; receipt already advanced `Ap` and `G`, with no rollback | Same monotonic cursors, then stop; replay cannot repair it, so recovery uses the reload guard |
| Checkpoint (transport progress) | `checkpoint` c covers eligible events through c | Advance only `G` to the highest id, even after failure; never `Ap` or `Sh` | Same, only while `F` is unset |
| Terminal | `reset` with or without id, including an equal or lower id | Handle before id-order checks; freeze page progress, close the source and run the reload guard | Same |
| Not observed | Sequence gaps from filtering or authorization | No outcome or cursor effect; server contract and checkpoints cover them | Same |

Terminal freezes outcome-driven cursor changes; visibility can still promote
`Sh` to `Ap`. Dispatch and connection observations remain separate from outcomes.
The pure model records terminal state; source closure and the reload guard
are adapter responsibilities.

Post-application verification:

| Frame | Expected before applying | Checked after applying |
|---|---|---|
| `message` (item `#X`) | — | Exactly one `#X`, whose parent is `#message-items` |
| `messages-moved` | Per id, present or absent by the current `applyMove` rules | Presence matches |
| Move crossing Load older | Applied to loaded items at receipt, recorded, re-applied after the response (`historyMoves`) | Same check at receipt and after re-application; a later mismatch is `history-reapply` |

The decoder/applier work must confirm whether a message frame's id equals
its item's `data-event-seq`; if so, that equality becomes part of verification.
Frames without `id:` still expose the inherited `MessageEvent.lastEventId`,
so that value never establishes durability. Durable kinds are `message`,
`messages-moved`, `checkpoint` and `reset`. Other kinds and malformed raw
dispatch ids leave `Rc` alone; frames without `id:` never move page cursors.

### Compatible and strict modes

The root sets one mode flag. Strict recovery is the direction described in
M10; the page selects compatible mode.

| | Compatible | Strict |
|---|---|---|
| `Ap` and `G` after a failure | Advance past it to the highest applied/no-op id; checkpoints advance `G` | Stop |
| Cursor attributes | Today's values | The stopped values |
| `F` visible to tests | Recorded, visible to tests | Recorded |
| Decode or apply failure | No recovery action, as today | `close()` within the same dispatch, preventing native reconnect, then the reset path's reload guard |
| History re-application failure | No recovery action; the record is dropped and the DOM stays inconsistent | `close()`, then the reload guard; a fresh snapshot repairs it |
| Condition S | Holds while no failure occurs | Always holds with the adapter actions above |

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
