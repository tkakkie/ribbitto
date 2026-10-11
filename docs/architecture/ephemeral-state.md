# Ephemeral state and generations

How presence and typing reach streams as current state with a generation
([decision 31](../decisions/31-presence-and-typing-are-current-state-with-a-generation.md));
durable events are in [real time](realtime.md).

## Ephemeral generations

The hub publishes durable, presence and typing levels per organisation as
atomic level/channel snapshots ([stream limits](stream-limits.md)). The single
writer waits on the durable cursor, declared generations, heartbeat and context
without a lost wake-up or registry lock.

## Ephemeral state

[Decision 31](../decisions/31-presence-and-typing-are-current-state-with-a-generation.md);
pages hold one organisation stream with [interests](streaming.md#stream-scope).
Delivery, sink, presence and typing state are current; typing signals and
delivery are planned (#288). Only visible transitions raise generations.

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
  the hub. Typing will keep each channel/topic's typist set, count, four latest
  and last-change generation.
  A read uses only its place's summary (the channel for a feed), subtracting
  the viewer in constant time; work never grows with other places' state.
- **Delivery.** After each durable batch and before waiting, at most
  one frame without `id:` per advanced kind. Durable order and the server
  cursor are unchanged; a backlog delays changes by at most one batch, and
  empty or denied wakes preserve the heartbeat deadline since the last write.
  Presence reads at most 100 entries from the changed suffix; typing will
  supply its place's indicator (at most three names, then a count) when changed. Presence entries
  share bounded renders per member, state and language, replacing stable indicator IDs through the hidden sink.
- **Reset.** Presence requires `reset` for another process's token, a start below
  the discard boundary, or over 100 changed entries, on connect or later. Equality
  at the boundary is served. The web adapter sends `reset` without
  `id:`; the page reloads with fresh state (decision 24).
- **Authorization** runs immediately before each frame, by the rule for an
  organisation-wide event (presence) or one of the frame's channel (typing):
  a deny skips the frame and its generation counts as seen, and a failed
  check stops the stream, as for durable events. Checks per connection are
  bounded by batches and wakes, at most one per kind each, not by changes;
  raises are bounded by visible transitions, and typing signals by a
  per-member limit (#288).
- **Restore.** At connect, typing sends its current indicator, presence the
  changes after the page's token, and the sidebar a recount (decision 30).
- **One process.** State and levels are per process; several processes need
  a shared source for both (#236).
