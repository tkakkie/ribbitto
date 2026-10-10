# Typing

`internal/typing` owns memory-only state per process, with no tables, store,
wiring package, event kinds or HTML. It imports only kernel IDs and realtime's
interest type for generation notifications. The summary contract is current
(#784); expiry/stream lifetime (#785), authorized ingress (#787), delivery
(#788) and page integration (#789–#790) are subsequent parts of #288.

`Start` takes trusted organisation, channel, nonzero composing topic and member
identity (display name and handle); callers must resolve and authorize them.
`Stop` removes exactly that member/topic activity and is idempotent. Repeated
starts preserve the captured identity and order and raise nothing. No timer,
stream count, HTTP signal or rendering is implemented here.

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
[ephemeral transport](realtime.md#ephemeral-state) remain unchanged.
