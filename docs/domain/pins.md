# Pinned messages

Pins keep important messages within reach in a channel's overview panel
(#874). They are post-M6 message work, not M5 or M6 polish. The maintainer agreed
these rules on 2026-10-12 (#880). Metadata storage and bounded snapshot
reads (#887), pin/unpin use cases (#888), realtime's generic channel-state
refresh seam (#891), the overview's pins section (#889) and Pin and Unpin in
each message's actions menu (#890) are implemented. Live pins (#1205) refresh
the overview and loaded controls across members and tabs while it is open.
The jump from a pin uses the message's URL ([decision 36](../decisions/36-one-message-has-a-stable-url.md),
implemented by #1170), which comes first.

## Permissions and limits

**Permissions.** Every member who may
write in a channel may pin any of its messages and unpin any pin, including
another member's. This matches [branching](topics.md#branching) and lets
members curate shared information. Considered and rejected: only owners may manage
pins (bottleneck); everyone may pin, but only the pinner or an owner may
unpin (stale pins can outlive the available pinner).

Reads require permission to read the channel; mutations require permission
to write it. Resolve the organisation from the URL and membership from the
session. A non-member, inaccessible channel or message outside that
organisation/channel returns 404. An interest or message ID grants nothing.
Private-channel and direct-message work must preserve these checks.

**Limit.** **50 pins per channel**, across all
its topics, with no per-member quota. A bounded overview stays readable and
cheap to refresh. Considered and rejected: 20 (stricter curation), 100 (more capacity),
or unlimited with paging (a larger feature). At the limit a new pin fails
with 409 and a localized explanation; nothing is evicted automatically.
Concurrent requests cannot exceed the limit.

One message has at most one pin. Pinning an already pinned message succeeds
without replacing its pinner or time; unpinning an existing, unpinned
message succeeds without a change. Neither no-op emits an event. Unpinning
then pinning again records the new pinner and time. Pinning, unpinning and
reading the overview do not post a notice, change unread state or notify
members individually.

## What a pin shows

**Presentation.** Newest pin first, with a
stable message-ID tie-break, and an excerpt of at most **200 Unicode code
points**, collapsing whitespace and appending an ellipsis when shortened.
Considered and rejected: oldest first (stable reference order), full bodies (large
panel), or manual ordering (additional state and controls).

Each entry shows the current message excerpt, its author, its current topic,
who pinned it and when, and the message's ordinary URL
([decision 36](../decisions/36-one-message-has-a-stable-url.md)). Author
and pinner use current [display names and handles](names.md); the pin time
is server-assigned and uses the normal timestamp fallback. The overview
includes every topic, even when opened from one topic. A reply can be pinned
like any other message; the entry previews that reply, not its whole chain.
Opening the link follows ordinary message navigation and read rules; the
pin itself adds no read scope ([decision 22](../decisions/22-replies-stay-in-the-stream-with-a-reply-chain-panel.md)).

The excerpt is derived from the stored message, never a retained copy.
It shortens the message's extracted text: one extraction function per
format, owned by `conversation`, which returns a `plain_text` body unchanged
and a `chat_v1` body with its references resolved, counting the 200 code
points after resolution ([messages](messages.md#showing-bodies)), and
leaves out a quote's snapshot ([decision 38](../decisions/38-a-quote-is-a-snapshot-owned-by-the-quoting-message.md), *planned*). A future
format adds its own extraction, never reinterpretation of old bodies
([decision 18](../decisions/18-stored-message-formats-never-change-meaning.md)).
Web escapes the excerpt through templ and isolates its direction. No HTML
is stored as pin content and no `templ.Raw` is used.

## When the message changes

| Change | Result |
|---|---|
| Move to another topic in the same channel | Keep the pin, pinner and time; show the current topic and keep the stable message link. Branching keeps message identity (decision 21). |
| Move to another channel | Remove the source pin atomically with the move. Do not create a destination pin or expose the destination through the source overview. A destination member may pin it anew. |
| Edit | Show the current excerpt; retain the pinner and time. A pin is a reference, not an endorsement of a frozen version. |
| Delete | Remove the pin atomically with deletion, freeing its slot. Keep no excerpt or deleted-message placeholder in the pins section. |

**Lifecycle.** These four rules apply. Considered and rejected:
unpin on every topic move (loses useful references); transfer a pin on a
channel move (changes the destination's curated list and may exceed its
limit); unpin on edit (requires curation again). Retaining deleted text or
a frozen excerpt would require a separate retention design. A reply's deleted-parent placeholder belongs to the reply
chain, not to pins ([replies](replies.md)).

Pin writes serialize with message moves/deletion, so a stale source-channel
request cannot recreate a pin after the message leaves. Future deletion
and cross-channel moves implement the [vanished-messages rule](../architecture/replay.md#vanished-messages):
obsolete posts skip, partial moves deliver surviving messages, and later
events still arrive. Pin refreshes read the surviving current list, even
when empty; an old pin event never requires its named message to exist.
Only genuine disappearance is normal; other read, authorization, render
and send errors remain failures.

## Storage, ownership and delivery

**Model.** Nullable `pinned_by_member_id` and
`pinned_at` on `message`, both null or both present, with an
organisation-scoped member reference. The channel is the message's current
channel; the lifecycle rules clear these fields on a cross-channel move.
This extends message state and belongs in `conversation` under
[decision 27](../decisions/27-channels-topics-and-messages-are-one-conversation-module.md).
It adds no table or module-import edge. Considered and rejected: a separate pin table
that only references messages/channels belongs to a new `pins` module
depending on `conversation`'s API; split any accompanying message behaviour
into `conversation`. That alternative needs transaction-bound injections
for atomic cleanup and a newly reviewed split, not foreign-table writes.

The use case locks the organisation before message reads/writes, checks
the current channel and pin count, changes pin state and appends its event
in one transaction using the existing org/realtime injections. Failed and
no-op operations leave the sequence unchanged: if allocating the sequence
acquires the lock, roll back that reservation on a no-op and return success.
The read API returns plain
structs from one snapshot; only web renders HTML. Existing member-directory
APIs supply names, never direct foreign-table queries.

The [delivery design](../architecture/pins.md) records the durable
invalidation kind, current-state frames, reconnect rules and alternatives
against decisions 23, 30 and 31. When editing, deletion and moving messages
to another channel are implemented, their issues update or remove the pin
in the same transaction as the message change, and add the refresh
triggers; the pin issues do not implement those features ahead of them.
