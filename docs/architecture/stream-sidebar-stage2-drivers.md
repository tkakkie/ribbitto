# Stage-2 HTTP drivers (#865, PR 3)

The [stage-2 protocol](stream-sidebar-stage2.md) drives a real server through
authenticated HTTP. These drivers live in `cmd/loadgen` and keep its
[HTTP-only boundary](packages.md): no application imports. Nothing here runs a
measurement; runs start only on the maintainer's explicit instruction.

## Posts and pages (slice 3-2a)

The driver validates the credential version and seed, the channel and topic
counts, and N distinct members and sessions. Metadata cannot attest message
counts. Post and page drivers rotate members by slot ID and named topics by topic
slot, using loopback HTTP/1.1 and `stage2HTTP`. Posts carry the member's newest
applied durable cursor for their implicit read, and a marker identifying the logical post's
author and scope. Responses do not advance that applied cursor.

## Reads (slice 3-2b)

Feed and topic reads send one-attempt `/read` POSTs carrying only the form cursor,
with `HX-Request: true`. Slot times, operations, feed/topic split and named topic
rotation stay exactly as `newStage2Schedule` specifies.

**Stream scopes.** Each member establishes its one stream from
`stage2StreamPage(member)`: even indices use the feed, odd indices alternate the
two named topics. The pools are N/2, N/4 and N/4, proportional to read demand.
The establishment page's current read form supplies initial evidence, with no
extra GET. Members precede seeded messages, so these snapshots have unread work.
The [stream intake](#streams-slice-4a) applies complete durable frames through `applyFrame`;
receipt alone, ephemeral frames and POST responses never advance the cursor.
Feed streams receive hot-channel posts from all topics; topic streams receive
only their named topic's posts ([streaming](streaming.md#stream-scope)).

**Targets and selection.** A read scans at most N members in rotation order,
starting at scheduled ID modulo N. It takes the first member on the slot's scope
page whose admission and short evidence locks can be acquired with `TryLock` and
whose evidence is eligible. Reading stays on that member's stream page. The
single pure eligibility function, shared with the deterministic simulation,
accepts either:

- An applied marked post by another member in that scope, with sequence above
  the maximum of the feed and that post's topic reservations, and at or below
  the member's applied durable cursor. A feed can use any qualifying topic. The
  read submits the newest applied cursor; evidence is compared with bounds,
  never consumed as a one-use token.
- A valid same-scope page snapshot, with exactly one current read form, a
  committed cursor above the reserved bounds and a visible submit button. A
  larger cursor alone may cover unrelated events or own posts. Page GETs never
  read and their cursors never move the stream backwards.

The observation retains the selected member separately from the slot ID; the
HTTP verifier receives both. `stage2MemberReadCounts` reports attempted reads per
member, including ambiguous failures. Paired windows keep identical slots and
scopes but can select different members as delivery timing changes.

**Reservations.** Every attempted post or read reserves its cursor as a bound,
including ambiguous failures, because they may have committed. It also drops all
of that member's cached page snapshots, since topic reads and own posts change
the feed too. A feed bound covers the topics; a topic bound does not cover the
feed. Reservations avoid reusing a target; they are not proof of persisted state.

**Admission.** Each member admits one page, post or read at a time. Posts and
pages retain scheduled-member rotation and refuse overlapping work without
waiting. Reads skip busy or ineligible members; if the bounded scan finds none,
they fail without HTTP. No refused slot waits, retries or changes scope. It stays
recorded as unsuccessful. Frames can update evidence during HTTP, under a short
separate lock; a page GET cannot install a pre-write snapshot after that write.

**Deterministic proof.** The virtual-time test uses the driver's eligibility,
selection, frame intake and reservations over all four fixed 600 s schedules,
seeds 0/1/42/865 and delivery lag 0/1/5 s (including the freshness p99). It excludes
own posts and uses only establishment snapshots and stream evidence, with no
scheduled pages. Every scheduled read must find a member; each scenario reports
the smallest eligible pool and min/max reads per member. A page-only control
retains scheduled pages and shows the original supply shortfall. This proves
evidence supply under those delivery assumptions, not HTTP latency, absence of
runtime contention, fairness gates or persisted advancement.

**Proof.** A post response confirms the submitted implicit-read bound but exposes
no new message sequence (the feed returns a message ID, a topic a redirect).
Full read ranges, other writers, moves and unseen commits stay unknown, so other
activity can invalidate a target and an offered read may not advance anything.
A 204, or a badge that is capped or changing concurrently, is not proof. An
injected verifier classifies a read as `changed` (persisted advancement) or
`noop` (nothing newer) from HTTP evidence, and anything else stays `unverified`.
Verification runs after the HTTP clock stops. No verifier exists yet: reads stay
`unverified`, and the stage-2 gates reject them until the witness slice supplies
the proof.

## Streams (slice 4a)

`establishStream` owns one stream per member until its caller closes it. It
uses `stage2StreamPage(member)` once through the page driver, installing that
read form as initial evidence and taking its positive cursor for Last-Event-ID.
It authenticates the scoped `/events` GET with that member's session and asks
for sidebar, messages and typing on the hot channel and, for topic members,
their named topic. Admission requires HTTP/1.1, 200 and `text/event-stream`.
The 10 s header deadline ends at admission; the stream retains the caller's
context. Page and stream attempts keep their statuses as establishment evidence,
outside scheduled page totals. Failed establishment releases ownership without
retrying or waiting for capacity.

The complete-frame parser calls an explicit application callback. Only an
accepted `message` or `messages-moved` frame with a positive signed-63-bit ID
advances the stream cursor and feeds `applyFrame`, including any post markers.
Partial payloads, declined application, ephemeral/unknown events and responses
cannot acknowledge durable progress. Later page evidence may improve read
eligibility without moving the stream cursor forward and skipping replay.
The callback is a seam for the later oracle; it is not itself a freshness proof.

Close cancels and closes the response body, joins frame intake, then releases
the member's stream ownership exactly once. Reset and unexplained EOF/read
failure remain lifecycle errors; caller cancellation is planned shutdown.
Fake-server tests gate frame delivery and cancellation without sleeps. Scheduled
churn and refusal accounting belong to slice 4b; there is still no runner.
