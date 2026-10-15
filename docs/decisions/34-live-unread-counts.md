# 34. Live sidebar unread counts are eventually exact

**Decided** (#1039, 2026-10-14; documented by #1043): counts are exact at
page load and at every successful resync, approximate in between, and always
exact again after the next successful resync. PRs 2–3 of #1039 implement the
contract, client increments and resync; PR 4's delays and server bound remain planned.
Rules: [unread](../domain/unread.md#counts); composition:
[unread counts](../architecture/unread-counts.md); transport:
[streaming](../architecture/streaming.md); markup:
[web layers](../architecture/web-layers.md#javascript).

- **Intended approximations.** A `message` frame never raises a count. A
  hidden tab, or a scope the tab shows but has not yet read, therefore does
  not rise while messages arrive; the next resync or read makes it exact.
  Legacy events and channels or topics created after the snapshot wait for
  the next resync.
- **Narrow exception to [decision 19](19-web-layers-server-owned-html-htmx-swaps-javascript-as-enhancement.md).**
  One script owns sidebar badge numbers. It changes only the text and
  visibility of server-rendered badges, never creates elements and never
  makes requests: htmx sends the resync request. Its bounded recovery state
  is the snapshot sequence, at most 1,000 retained `unread` events since
  that snapshot with eligibility and whether each is applied, a recovering flag,
  loss generations and the highest possibly lost sequence.
- **[Decision 30](30-one-organisation-wide-stream-per-tab-filtered-by-its-interests.md) still holds.**
  Every `unread` frame passes the existing audience and final bound-member
  checks; interests grant nothing new. The connection, interests and cursor
  contract stay; small shared frames replace all-connection post recounts.
  **Precondition:** every member may read every channel. Private channels
  must add a per-channel check to `unread` frames before they ship.
- **[Decision 24](24-a-cursor-that-cannot-be-served-gets-reset.md)** still
  requires a page reload on `reset`; PR 4 provides its random 0–5 s delay.
  **[Decision 32](32-read-state-is-a-set-of-read-ranges-per-member-and-channel.md)**
  is unchanged: read state, its writes and exact page-load SQL stay as they are.

**Why:** while posts flow, recounting every connection's sidebar about once
per second makes SQL work grow with connections. At 5,000 connections,
median message arrival was 22 ms without unread counts at 20 posts/s,
1.2 s with counts at 5 posts/s and 1.5 s at 20 posts/s
([measured results](../architecture/stream-sidebar-results.md)). Small shared
per-post frames keep counts moving without that repeated SQL; resync restores
exact values.

**Considered:** keep recounting every connection (the growing cost and measured
delays above); write-time counters and the hybrid (the maintainer stopped these
and the prior recount investigation on 2026-10-14: #832, #865 stage 2, #952,
#955–#957 and the hybrid prototypes). Their removal is separate, after #1039's
five PRs, from its approved list; this decision removes no code.

## Event and snapshot contract

PR 2 added the contract; PR 3 switches live counts to the client.
For ordinary posts and branch notices, `message.posted` gains `author_id`
resolved from the session, never the request. Missing author or topic in a
legacy payload never produces an increment. After interest and topic filtering,
each sidebar connection gets exactly one frame for a post: `message` if its
message interest selects it, otherwise the shared, once-rendered `unread` frame:

```text
event: unread
data: {"channel":"<id>","topic":"<id>","seq":<n>,"author":"<id>"}
```

It uses the posting-time routing topic and carries no `id:`, so it never
moves `Last-Event-ID`. The `message` frame gains no attributes. The sidebar's
organisation event sequence is read in the same repeatable-read snapshot as
its counts: `data-snapshot-seq` on `#sidebar-channels`. Every badge, including
the heading badge, has `data-unread-count`; a zero badge exists but is hidden.

The one script adds one to the channel badge and the shown topic or heading
badge only for a previously unseen sequence above the snapshot, with a present
author other than the tab's member, while not recovering. It shows `99+` from
100. Snapshots arrive as `sidebar` frames and apply in stream delivery order,
never by choosing the highest sequence: a read changes counts at the same
sequence, and computations complete in start order. Replace the numbers and drop
retained events at or below the new sequence. While not recovering, reapply all
eligible retained events above it, marking them applied; eligibility requires a
present author other than the tab's member. Later events at or below the snapshot
are ignored.

## Resync and recovery

PR 3 provides `POST /organizations/{slug}/sidebar/resync`: a member-scoped 204
that publishes through `unread.Changes` and returns no fragment. The member's
streams recount and deliver the snapshot over SSE. Reads and own posts recount
only that member's tabs; moves or branches dirty only streams showing that
channel. Page navigation is already exact.

Resync also follows reconnect, `reset`, visibility return or back-forward-cache
`pageshow`, malformed frames, failed DOM application, failed resync requests and
losses beyond retention. Above 1,000 retained events or on those failures, stop
incrementing and enter recovery. Merge duplicate requests into one pending
request and retry with jittered backoff. Clear recovery only when a snapshot
arrives after dispatch, with no event lost since the request it answers was
dispatched, and its sequence covers every possibly lost event. Overflow records
the highest dropped sequence; malformed or unapplied frames record the highest
sequence seen, including the failed frame when parsable. Then apply all eligible
retained events above that snapshot. Retained events are not losses.
Otherwise show the snapshot numbers, request another resync and stay recovering,
even if losses recur.
No event-history replay or unbounded retention is added. Extreme sustained load
can keep recovery open, showing the last exact snapshot.

PR 4 now spreads connect recounts by 0–3 s, visibility/pageshow resyncs by 0–2 s
and `reset` reloads by 0–5 s. One bound on concurrent recount computations covers
connect, read, resync and moves, separately
from stream registrations. Its [current sizing](../recount-capacity.md#current-computation-sizing-1046) reserves normal-work pool headroom.
After a bounded slot wait, leave the sidebar dirty and retry with jitter;
established streams do not answer 503. The resync POST only publishes and
answers immediately. The bound delays exactness rather than rejecting work.

Known limits: a mass simultaneous `reset` has only the random reload delay;
ordinary page loads gain no admission limit. Resync wakes all of the member's
tabs (at most 16) in the same process; the member watch remains process-local.
Without JavaScript, counts change only on page load.
