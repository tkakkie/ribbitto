# Typing

`internal/typing` owns memory-only channel/topic summaries. Its
[package contract](../../internal/typing/doc.go) defines expiry, composer
lifetimes and bounded viewer-filtered reads. The transport and publish-before-raise
rules belong to [decision 31](../decisions/31-presence-and-typing-are-current-state-with-a-generation.md).

Activity requires an accepted stream for the same organisation, member and
resolved channel, independent of interests and selected topic. Signals without
one create no state; streams without a channel take no typing count. Last close
immediately clears that member's topics in the channel, even against a racing
signal; other channels, organisations and presence's independent counts/grace
are unaffected. Web calls `Streaming.StreamOpened` with the resolved channel
only after registration and the session re-check succeed.

Starts refresh the five-second expiry without changing captured identity,
order or generations; stops are scoped and idempotent. A member in two topics
counts once in each and once in the feed. Adding/removing a topic while that
member remains in the feed does not reorder it or change its place generation.
Each distinct-member transition advances its place generation; one start/stop
advances the organisation generation once even when both topic and feed change.
Reads copy captured identity, exclude the viewer even outside the four cached
candidates, and return at most three others, newest first, plus the remaining
count. They do no expiry, directory query or unrelated-state traversal
(`TestCandidatesAndReadWork`). Empty summaries retain their clearing generation.
Notifiers must not call back into state.

## Composer ordering

Each composer has its own signed generation bound to server-resolved scope.
The token is ordering evidence, not authorization: callers still authorize
scope and identity. The random signing key stays in memory, never configured,
logged or persisted; restart refuses old generations because typing state is
lost then anyway.

`Posted` closes the submitted generation and stops the member's requested
posting place atomically with respect to signals. A repeat uses its requested
place even if the message has since moved. Missing/invalid generations still
stop that scope without closing another scope's composer; other tabs and newly
rendered composers remain usable. Closures last `ClosedRetention` (one minute
from the latest successful post with that generation), bounding storage to
recent posts, not every rendered composer. `SignalDeadline` permits ten seconds
from handling start through locked acceptance, including lock waits; equality
is accepted, later or future-start signals change no state or expiry.
Ten seconds allows short handler work but rejects stalled processing; one minute
gives six times that margin for post/signal reordering.

**Limit:** after closure cleanup, an old correctly signed composer can signal
again. This keeps long-open idle composers usable without an age cutoff or
periodic re-render. A pre-post request held outside the server longer than a
minute can begin handling after cleanup and show typing until five-second
expiry; a request already being handled that long fails the shorter deadline
(`TestComposerOrdering`).

## HTTP ingress and posting

`POST /organizations/{slug}/channels/{channelID}/topics/{topicID}/typing` uses
`Signal`, never `Start`, after [member admission](rate-limits.md#member-admission-for-typing).
Non-membership or bad scope returns 404; form parsing requires exactly one
nonempty `composer` generation (400, or 413 over the body limit). Accepted and
refused generations (invalid, closed or overdue) return empty 204 with no swap.
Only URL organisation and resolved member/account identity reach state. Refusals
change no activity, deadline or generation (`TestTypingRefusals`).

Plain and enhanced posts call `Posted` only on success or a repeat, with the
returned message's organisation/channel/author and the requested posting topic.
Failure preserves activity and ordering: 422/409 renders keep the submitted
composer generation exactly, including empty/invalid values or absence, so the
successful retry closes signals sent before the failure. Success issues a fresh
generation. Feed composers use the snapshot's default topic;
topic composers use its resolved selected topic, without extra issuance queries.
Hidden composer/signal controls and the two-second unqueued throttle follow the
[CSP contract](csp.md#content-security-policy). Posting works without JavaScript;
input sends no typing signals without it. Production shares the stream state
and one limiter.

## Production delivery

Viewer identity comes from stream membership, never the query. Delivery uses
captured signal-time identity without membership/directory reads. Latest
feeds/topics keep `#typing-status` under `#organization-stream`, outside composer
replacement and history, with the [polite, focus-preserving status contract](../accessibility.md).
Members and directly opened older pages keep their interests and have no typing
region.

Connect/reconnect reads with after=-1 and restores the complete indicator,
including empty generation-zero state. Later frames require `PlaceGeneration > after`;
reads acknowledge the consistently read organisation generation even
when the selected place is unchanged. Raises during rendering remain pending,
so unrelated wakes neither repeat indicators nor spin
(`TestTypingDeliveryGenerations`). Interest and organisation/channel/topic
filters precede authorization after rendering immediately before send; denial
or error stops the stream. Decision 31's no-SSE-ID rule preserves durable replay
and its cursor.
