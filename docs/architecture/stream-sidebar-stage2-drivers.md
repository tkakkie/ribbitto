# Stage-2 HTTP drivers (#865, PR 3)

The [stage-2 protocol](stream-sidebar-stage2.md) drives a real server through
authenticated HTTP. These drivers live in `cmd/loadgen` and keep its
[HTTP-only boundary](packages.md): no application imports. Nothing here runs a
measurement; runs start only on the maintainer's explicit instruction.

## Posts and pages (slice 3-2a)

The driver validates the credential version and seed, the channel and topic
counts, and N distinct members and sessions. Metadata cannot attest message
counts. Post and page drivers rotate members by slot ID and named topics by topic
slot, using loopback HTTP/1.1 and `stage2HTTP`. Posts carry the caller's page
cursor for their implicit read.

## Reads (slice 3-2b)

Feed and topic reads send one-attempt `/read` POSTs carrying only the form cursor,
with `HX-Request: true` and the same scheduled-ID rotation.

**Targets.** A read's cursor comes from that member's last complete page of the
same scope. The page must contain exactly one current read form whose committed
snapshot cursor exceeds the member's reserved bounds, and that form's submit
button must be visible, which shows unread work at the snapshot. A larger cursor
alone proves nothing: it may cover only unrelated events or the member's own
posts. Page GETs never read.

**Reservations.** Every attempted post or read reserves its cursor as a bound,
including ambiguous failures, because they may have committed. It also drops all
of that member's cached page snapshots, since topic reads and own posts change
the feed too. A feed bound covers the topics; a topic bound does not cover the
feed. Reservations avoid reusing a target; they are not proof of persisted state.

**Admission.** Each member admits one page, post or read at a time. Concurrent
work for the same member is refused without waiting, so a snapshot taken before
a write never becomes eligible after it. A read without an eligible snapshot is
refused before any HTTP exchange. Neither case is retried or moved to another
member; the scheduled work stays recorded as unsuccessful.

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
