# Ephemeral state and generations

How presence, typing and member read invalidations reach streams with generations
([decision 31](../decisions/31-presence-and-typing-are-current-state-with-a-generation.md));
durable events are in [real time](realtime.md).

## Ephemeral generations

The hub publishes durable, presence and typing levels per organisation as
atomic level/channel snapshots ([stream limits](stream-limits.md)). The single
writer waits on the durable cursor, declared generations, heartbeat and context
without a lost wake-up or registry lock.

## Member read-change watches

`unread.Changes` owns process-local generations keyed by organisation and member
(#829). Active watches share a generation and wake channel;
publication closes/replaces that channel under the same mutex as subscription,
snapshot capture and release. Publication without a watch retains no entry, and
the last idempotent release deletes it. A reopened entry starts at zero; generations
are comparable only within one subscription lifetime. No queue or worker is added.

Callers subscribe, then capture the generation/channel pair **before** each
persisted read, the initial one included, and wait on that captured channel. A
commit before capture is in the read; one after it closes the channel, even during
the read, so the next recount covers it. A pair captured after the read would
miss a commit in between, until the member's next change or a reconnect.
Publication costs one lookup/update and wakes only that member's waiting tabs;
memory tracks active subscriptions, bounded by production stream caps.
`cmd/ribbitto` shares one registry between Reading and the live sidebar
`realtime.SidebarWatcher` adapter (#711). The stream acquires before its initial
recount and defers release immediately. Observed generations dirty the existing
coalescer without moving its deadline; only a successful send or explicit denial
acknowledges the generation captured before the persisted read. Changes during
rendering remain pending. Scheduled delivery subscribes before submission, captures
at the computation boundary and covers only that request's generation after send.
The writer seals boundary access before its exactly-once release; an uncooperative
worker may finish its read later but cannot snapshot the released watch.
Atomic capture in the combined wait prevents a lost
wake; advancing observation while throttled prevents spinning on already observed work.
Recounts and authorisation retain per-tab costs; wakes/log reads are not throttled.
Sidebar frames render before checking both organisation and bound member, omit
`id:` and preserve the cursor; a deny ends the stream (#852).

This registry is single-process. Reconnect recounts persisted state; an omitted
notification has no bounded repair without reconnect. Multiple processes need
cross-process member invalidation and a subscription/reconnect handshake; bounded
missed-notification recovery additionally needs durable revisions or replay.
The [short read measurements and acceptance](read-advance-results.md) attribute
work to organisation/member keys. Healthy delivery converges by generation;
unknown-COMMIT recounts and process loss retain the [reconnect exception](unread-writes.md).
The durable watermark worker cannot repair a missing member invalidation.

## Ephemeral state

[Decision 31](../decisions/31-presence-and-typing-are-current-state-with-a-generation.md);
pages hold one organisation stream with [interests](stream-scope.md#stream-scope).
Delivery, sink, presence and typing state, signals and delivery are current
(#784–#790, #813, #833), including the page's typing status region. Only visible transitions raise generations.

- **Owners.** Presence counts accepted streams through web's lifecycle seam,
  independent of interests, and marks members offline 30 s after the last
  close. A reconnect in that window cancels expiry without a transition.
  Bounded member snapshots return a process/generation token
  from one locked read; transitions publish state before raising
  the organisation generation. Typing state ends 5 s after
  the last signal or when the last channel stream closes.
  `realtime.EphemeralOwner` returns current state and its represented
  generation; web adapters render feature state. The subscription carries
  the opaque presence token. `cmd/ribbitto` wires one presence state to pages, stream counts and web's adapter.
- **Generations.** Presence keeps one entry per member, ordered by its last
  change. Changes after a token are a suffix; offline entries stay five minutes.
  Discarding one advances a monotonic boundary to its generation. State, boundary
  and generation are read under one lock; transitions publish before raising
  the hub. Typing keeps each channel/topic's typist set, count, four latest
  and last-change generation.
  A read uses only its place's summary (the channel for a feed), subtracting
  the viewer in constant time; work never grows with other places' state.
- **Delivery.** After each durable batch and before waiting, at most
  one frame without `id:` per advanced kind. Durable order and the server
  cursor are unchanged; a backlog delays changes by at most one batch, and
  empty wakes preserve the heartbeat deadline since the last write.
  Presence reads at most 100 entries from the changed suffix; typing
  supplies its place's indicator (at most three names, then a count) when changed. Presence entries
  share bounded renders per member, state and language, replacing stable indicator IDs through the hidden sink.
- **Reset.** Presence requires `reset` for another process's token, a start below
  the discard boundary, or over 100 changed entries, on connect or later. Equality
  at the boundary is served. The web adapter sends `reset` without
  `id:`; the page reloads with fresh state (decision 24).
- **Authorization** runs immediately before each frame, by the rule for an
  organisation-wide event (presence) or one of the frame's channel (typing),
  bound to the stream's member: a deny means that membership was removed or
  replaced and ends the stream (#852), and a failed check stops it, as for
  durable events. Checks per connection are
  bounded by batches and wakes, at most one per kind each, not by changes;
  raises are bounded by visible transitions, and typing signals by a
  per-member limit (#288).
- **Restore.** At connect, typing sends its current indicator, presence the
  changes after the page's token, and the sidebar a recount (decision 30).
- **One process.** State and levels are per process; several processes need
  a shared source for both (#236).
