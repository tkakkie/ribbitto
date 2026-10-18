# Posting and pin-change admission

`Services.PostLimits` and `Services.PinLimits` each require their own
`middleware.MemberRateLimiter`, distinct from every other member limiter;
missing or shared wiring fails startup. These are **unmeasured, provisional**
per-process budgets:

| Requests | Burst | Refill | Concurrent | Retry-After |
|---|---|---|---|---|
| Channel and topic posts | 20 | 1/s | 4 | 1 s |
| Pin and unpin together | 10 | one per 2 s | unlimited | 2 s |

Session and URL membership resolution precede admission; non-members still
get 404 without organisation data, even when budgets are exhausted. Admission
precedes channel lookup, form parsing, other feature reads and transactions.
A refusal is empty 429 with `Retry-After`, with no redirect, write or event,
for plain and htmx forms. Every admitted attempt spends a token, including
validation failures and committed-post replays. Posting immediately defers
slot release, covering success, validation, lookup failures, errors and panics.

The trusted organisation/member pair shares its budget across channels,
topics, sessions and addresses. Other members and organisations are independent.
The [member limiter's bucket bound, eviction and restart rules](rate-limits.md#member-admission-for-reads)
apply. No distributed coordination or new configuration is introduced.

Admission cannot inspect `idempotency_key`: after refill, a refused send can
retry with that same key; an admitted committed replay consumes budget but
adds no message or event. The [composer](web-composer.md) keeps ordinary text
and keys on 429; queued sends keep their text and key in the pending item for
Retry. A refused retry of an unknown outcome keeps that outcome unconfirmed.
