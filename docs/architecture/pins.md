# Pin delivery

The [domain rules](../domain/pins.md) put pin metadata on messages, owned by
`conversation`. HTTP pin/unpin requests first pass [member admission](member-admission.md).

## Durable invalidation, current display

`pins.changed` is a durable kind owned, encoded and routed by `conversation`,
with IDs-only payload `{"channel_id","message_id"}` and a null audience.
Pin state and event commit together; raise the hub only after commit. The event
invalidates the channel's pins, not an old pin to reconstruct. The hub carries
a committed level, never content, per [decision 23](../decisions/23-the-event-log-is-the-source-of-truth-the-hub-carries-a-level.md).

Use one channel-scoped `pins` interest on the existing organisation stream
([decision 30](../decisions/30-one-organisation-wide-stream-per-tab-filtered-by-its-interests.md)).
The hosting feed/topic requests it while the pins section is present. A later
panel obtains a fresh snapshot and reopens that stream, never a second one.
Topic selection must not exclude another topic's pins changes in the channel.

Send the whole current section at every connect and after coalesced matching
invalidations, as `pins` **without SSE `id:`**. Neither `Last-Event-ID` nor the
browser's applied message cursor advances; the server's durable read cursor
still advances over invalidations and filtered events. Whole replacement is
idempotent, including an empty list removing stale entries.

Initial pins, current messages/topics, names and page cursor share one read-only
repeatable-read snapshot. On connect, validate the durable cursor, then read
fresh pins even with no later event. An expired cursor uses reset/reload.
Reconnect repairs a lost refresh, missed notification or restart without historic
excerpts; pin frames never skip an undelivered message.

Classify invalidations before message topic filtering or vanished-target skips.
`pins.changed` dirties its channel; same-channel branching dirties it for topic
labels. Future edits/deletion must dirty the affected channel; cross-channel
moves must dirty both old and new channels using persisted routing IDs. Their
implementing issues own these triggers. Refreshes follow the domain's
[vanished-message contract](../domain/pins.md#when-the-message-changes) and
[replay rules](replay.md#vanished-messages), independent of historical event targets.

## Ordering, authorization and cost

The channel-state source owns triggers, bounded whole-state reads, frame names
and shared renders. The single writer refreshes at most once between durable
batches, coalescing at `ChannelStateInterval` (one second). Bursts never postpone
the fixed deadline; pending work survives quiet periods and backlogs, and changes
committed during refresh remain pending for later batches. Add no goroutine,
per-connection event queue or hub generation (`TestChannelStateBacklogAndTriggerDuringRefresh`).

Filter organisation, channel and interest before rendering. After rendering,
check channel authorization and bound membership immediately before every send.
Denial of the connection's membership, or read/render/send failure, ends delivery;
reconnect's fresh snapshot repairs the section. Refresh frames must omit `id:`.

Read at most the domain's 50 pins with batched names, never a query per pin.
Share fresh snapshots per organisation/channel and renders per language within
a refresh round; never use an event-sequence cache as current pin state. A newer
invalidation starts another round, and completed rounds are discarded. Shared
output has no member controls (`TestPinsSourceCost`, `TestPinsRefreshScopes`).
Exclude pin invalidations from message and unread rendering after trigger
classification; skipped events still advance the durable cursor.

**Considered and rejected:** replay pin/unpin deltas with `id:` (adds ordering
and vanished-target cases and needs an extension to decision 30's message-cursor
contract); only a process-local generation with persisted snapshots, as member
read invalidation in [decision 31](../decisions/31-presence-and-typing-are-current-state-with-a-generation.md)
(needs separate missed-notification recovery); polling (extra delay and requests).
Durable triggers serve persisted changes, with current-state display as in the
sidebar; pins do not change decision 31's memory-only presence/typing owners.

The section's snapshot sequence orders racing frames and out-of-band responses:
stale sections must never overwrite newer ones, without changing durable or
applied cursors. Loaded controls retain both server-rendered actions; the script
selects visibility and restores focus. Closing the overview unsubscribes;
controls recover on reload. Forms/buttons retain their DOM contract and native
POST/303 fallback, including when htmx is missing.

Pin buttons omit `type` (native submit) and `hx-*`, as do Quote and Quote selection;
these four are the only exception to the [button type rule](../accessibility.md).
Popover triggers keep `type="button"`; [pin forms have no `hx-*`](web-htmx.md).
Enhanced submission uses the submitter's server-rendered `formaction` (the visible
button when absent), with independent per-form in-flight drops. A present overview
requests the current section with the response. A 409 swaps the unchanged control
and limit alert; 404/5xx do not swap.
