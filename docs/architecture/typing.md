# Typing

`internal/typing` owns memory-only state per process, with no tables, store,
wiring package, event kinds or HTML. It imports only kernel IDs and realtime's
interest type for generation notifications. Summaries and composer ordering are
current (#784–#785, #813); authorized ingress (#787), delivery
(#788) and page integration (#789–#790) are subsequent parts of #288.

`Start` takes trusted organisation, channel, nonzero composing topic and member
identity (display name and handle); callers must resolve and authorize them.
`Stop` removes exactly that member/topic activity and is idempotent. Repeated
starts extend the five-second deadline, preserving captured identity, order
and generations. Owner timers expire activity without reads; timer identity
under the state lock prevents an already-running callback from clearing a
refresh or a stopped/restarted activity. HTTP ingress uses `Signal`, not `Start`.

`IssueComposer` returns an opaque string signed with HMAC-SHA256 over the
organisation, member, channel, topic and a fresh random nonce. Each rendered
composer gets its own generation; issuance stores nothing. A random signing key
is created with the state, held only in memory, never configured, logged or
persisted. Invalid signatures and reuse in any other scope are refused. Tokens
from before a process restart are refused; typing state is lost then anyway.
The token is ordering evidence, not authorization: scope and identity are trusted
server-resolved inputs, and callers still authorize them.

`Signal` takes the generation and the caller's handling-start time. `Posted`
takes the returned successful message's actual scope and its composer's
generation. Under the same state lock, it closes that generation and stops the
member's activity in exactly that organisation/member/channel/topic. Missing or
invalid generations still stop that scope (posting can precede #789), but cannot
close another scope's composer. Another tab's composer remains usable, and a new
composer rendered after success can start typing again. A failed submission must
not call `Posted`: its 422 re-render carries the submitted generation unchanged,
so the successful retry closes signals sent before the failure (#787 → #789).

Closures stay for `ClosedRetention` (one minute after the latest successful post
with that generation: a double submit restarts it, and a replaced timer that
already fired leaves the renewed closure), then owner timers remove them without
viewer reads. This bounds storage to recent
posts rather than every composer ever rendered. `SignalDeadline` is ten seconds
from handling start to acceptance, checked under the mutation lock, including
time waiting for it; equality is accepted, later or future-start signals change
no activity, expiry deadline or visible generation. Ten seconds allows normal
short handler work and rejects stalled processing; one minute gives a margin
six times longer for post/signal reordering. `NewWithClock` injects the acceptance
clock; owner timers use time's clock, virtual in `testing/synctest` tests.

**Limit:** closed generations are refused only while retained. Once removed,
an old correctly signed composer can signal again. This keeps long-open idle
composers working without an age cutoff or periodic re-render. A pre-post request
held outside the server for longer than a minute can therefore begin handling
after cleanup and show typing until the five-second expiry. A request already
being handled that long is refused by the shorter acceptance deadline.

`Open` counts accepted streams by organisation, member and resolved channel.
A signal without a matching stream creates no state. Interests and selected
topic do not affect keeping state; delivery will separately check interest,
frame scope and authorization (#788). Closing one of up to 16 streams preserves
activity while another matches; last close immediately stops all that member’s
topics in that channel. Cleanup is idempotent and shares the signal/timer lock,
so a racing signal cannot revive activity after last close. Other channels and
organisations, and presence’s independent organisation counts and 30-second
grace, are unaffected. Streams with no channel take no typing count.

Web calls `Streaming.StreamOpened` only after registration and the session
re-check succeed, supplying the resolved channel. `cmd/ribbitto` constructs
one typing state, exposed through `Streaming.Typing` for subsequent ingress
and delivery consumers, and shares it with this lifecycle hook.

Each channel feed and topic has a complete distinct-member set, its count,
four latest visible starts and last-change generation. A member active in two
topics occurs once in each topic and once in the feed. Feed reference counts
preserve the other topic on stop; adding/removing an activity while the member
remains in the feed neither reorders it nor changes its place generation.
A linked order supplies replacement candidates after stops; mutations refresh
only four cached candidates. Empty summaries retain their clearing generation.

`Read` returns the organisation generation and selected place generation under
one lock. A zero topic selects the feed. It checks viewer membership in the
complete set, then examines at most four candidates, returning at most three
other typists, newest first, and the count of all remaining other typists.
Thus a viewer outside the four latest is still subtracted. Reads return copied
identity values and do no expiry, directory query or unrelated-state traversal.

Each distinct-member transition advances its place generation; one start/stop
advances the organisation generation once even when both topic and feed change.
State and candidates are published before raising `InterestTyping`, with the
lock held through notification. A notifier must not call back into state.
An unrelated place's change may advance the returned organisation generation
without changing the selected place generation, allowing subsequent delivery
to acknowledge the wake without repeating an indicator. Durable cursors and
[ephemeral transport](ephemeral-state.md#ephemeral-state) remain unchanged.
