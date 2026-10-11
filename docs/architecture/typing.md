# Typing

`internal/typing` owns memory-only state per process, with no tables, store,
wiring package, event kinds or HTML. It imports only kernel IDs and realtime's
interest type for generation notifications. The summary contract is current
(#784–#785); authorized ingress (#787), delivery
(#788) and page integration (#789–#790) are subsequent parts of #288.

`Start` takes trusted organisation, channel, nonzero composing topic and member
identity (display name and handle); callers must resolve and authorize them.
`Stop` removes exactly that member/topic activity and is idempotent. Repeated
starts extend the five-second deadline, preserving captured identity, order
and generations. Owner timers expire activity without reads; timer identity
under the state lock prevents an already-running callback from clearing a
refresh or a stopped/restarted activity. HTTP ingress and rendering follow.

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
