# Posting, pin-change and image-change admission

Posting, pin-change and channel image budgets are independent within `Services.MemberLimits`
([wiring](rate-limits.md#member-admission-for-typing)). These are
**unmeasured, provisional** per-process budgets:

| Requests | Burst | Refill | Concurrent | Retry-After |
|---|---|---|---|---|
| Channel and topic posts | 20 | 1/s | 4 | 1 s |
| Pin and unpin together | 10 | one per 2 s | unlimited | 2 s |
| Channel image changes | 10 | one per 6 s | unlimited | 6 s |

Session and URL membership resolution precede admission; non-members still
get 404 without organisation data, even when budgets are exhausted. Admission
precedes channel lookup, form parsing, other feature reads and transactions.
A refusal is empty 429 with `Retry-After`, with no redirect, write or event,
for plain and htmx forms. Every admitted attempt spends a token, including
validation failures and committed-post replays. Posting immediately defers
slot release, covering success, validation, lookup failures, errors and panics.

Image changes (`POST …/overview` from a channel or a topic) write no event,
so without a budget one member could rewrite channel rows without limit.
Their admission also precedes query parsing, and rejected images spend a
token.

The trusted organisation/member pair shares its budget across channels,
topics, sessions and addresses. Other members and organisations are independent.
The [member limiter's bucket bound, eviction and restart rules](rate-limits.md#member-admission-for-reads)
apply. No distributed coordination or new configuration is introduced.

Admission cannot inspect `idempotency_key`: after refill, a refused send can
retry with that same key; an admitted committed replay consumes budget but
adds no message or event. The [composer](web-composer.md) keeps ordinary text
and keys on 429; queued sends keep their text and key in the pending item for
Retry. A refused retry of an unknown outcome keeps that outcome unconfirmed.
