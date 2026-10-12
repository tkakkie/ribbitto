# Pin delivery *(planned, #880)*

The [domain rules](../domain/pins.md) put pin metadata on messages, owned
by `conversation`. The maintainer agreed this design on 2026-10-12 (#880);
nothing here changes the current stream contract until the split issues
implement it after M6.

## Durable invalidation, current display

**Delivery.** A durable `pins.changed` kind
owned, encoded and routed by `conversation`, with IDs-only payload
`{"channel_id","message_id"}` and a null audience. Pin/unpin state and its
event commit together; the writer raises the hub only after commit. The
event means the channel's pins need refreshing, not that an old pin should
be reconstructed. The hub still carries a committed level, never pin
content ([decision 23](../decisions/23-the-event-log-is-the-source-of-truth-the-hub-carries-a-level.md)).

Use one channel-scoped `pins` interest on the existing organisation stream
([decision 30](../decisions/30-one-organisation-wide-stream-per-tab-filtered-by-its-interests.md)).
The overview's hosting feed/topic page requests it while the pins section
is present; a panel loaded later obtains a fresh snapshot and reopens that
same stream with the interest, never a second stream. Pins are channel-wide:
a topic filter must not exclude changes in another topic of that channel.

Send a whole current pins section at every connect and after coalesced
matching invalidations, as an SSE `pins` frame **without `id:`**. Neither
`Last-Event-ID` nor the browser's applied message cursor advances from this
frame. The server's durable read cursor still advances over invalidations
and filtered events. Replayed invalidations may repeat; whole replacement
is idempotent, including an empty list that removes stale entries.

The pins, their current messages/topics, names and page cursor belong to
the same initial read-only repeatable-read snapshot. On connect, validate
the durable cursor, then read fresh pins even if no event follows it. An
expired cursor uses the existing reset/reload rule. Reconnect repairs a
lost refresh, a missed notification or a process restart without retaining
historic excerpts. Pin frames never skip an undelivered message.

Treat invalidation separately from message rendering, before a message's
topic filter or vanished-target skip. `pins.changed` dirties that channel;
same-channel branching dirties it for topic labels. Future edits/deletion
dirty the affected channel; a cross-channel move dirties both old and new
channels using persisted routing IDs. Their implementing issues own those
triggers. No pin refresh looks up a historical event's message ID, so
disappearance cannot wedge replay; the existing post/move rules remain
those in [replay](replay.md#vanished-messages).

## Ordering, authorization and cost

The single stream writer sends at most one pending pins refresh between
durable batches (currently at most 100 events), with a fixed coalescing
deadline, also checked before waiting. Bursts do not postpone that deadline;
pending work survives a quiet period and a backlog. A failed read, render
or send ends the stream; reconnect's mandatory fresh snapshot repairs the
section. No new goroutine, per-connection event queue or hub generation.

Filter by organisation, channel and interest before rendering; check the
connection's channel authorization after rendering and immediately before
every send. Denial of the connection's own membership ends it. The bounded
source returns at most 50 entries and batched names, not one query per pin.
Share snapshots/renders for the same organisation, channel and language
within a refresh round; never reuse an event-sequence cache entry as current
pin state. No member-specific controls enter shared output. Measure the
bounded refresh cost with distinct connections in the live-delivery issue.

**Considered and rejected:** replay pin/unpin deltas as new frames with `id:` (adds
ordering and vanished-target cases, and needs an explicit extension to
decision 30's message-cursor contract); use only a process-local generation
and persisted snapshots (as member read invalidation in
[decision 31](../decisions/31-presence-and-typing-are-current-state-with-a-generation.md),
but needs separate missed-notification recovery); polling (extra delay and
requests). The design uses durable triggers for persisted changes,
with current-state display as the sidebar does. Pins are not ephemeral
presence/typing, so decision 31's memory-only owners remain unchanged.
