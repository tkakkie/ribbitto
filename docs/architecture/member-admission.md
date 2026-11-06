# Member write admission

Posts, pins, resolution and images have independent `Services.MemberLimits` budgets
([wiring](rate-limits.md#member-admission-for-typing)):
**unmeasured, provisional**, per-process:

| Requests | Burst | Refill | Concurrent | Retry-After |
|---|---|---|---|---|
| Channel and topic posts | 20 | 1/s | 4 | 1 s |
| Pin and unpin together | 10 | one per 2 s | unlimited | 2 s |
| Resolve and reopen together | 10 | one per 2 s | unlimited | 2 s |
| Channel image changes | 10 | one per 6 s | unlimited | 6 s |

Session and URL membership resolution precede admission; non-members still
get 404 without organisation data, even when budgets are exhausted. Admission
precedes channel lookup, query/form parsing, feature reads and transactions.
Refusal is 429 with no redirect, write or event; its body and `Retry-After`
follow the [member response rule](rate-limits.md#member-rejection-responses).
Admitted attempts spend tokens, including rejected images,
validation failures and committed-post replays. Posting immediately defers
slot release, covering success, validation, lookup failures, errors and panics.

Image changes (`POST …/overview` from a channel or a topic) write no event,
so admission bounds row rewrites by one member.

The trusted organisation/member pair shares its budget across channels,
topics, sessions and addresses. Other members and organisations are independent.
The [member limiter's bucket bound, eviction and restart rules](rate-limits.md#member-admission-for-reads)
apply. No distributed coordination or new configuration is introduced.

Admission cannot inspect `idempotency_key`: after refill, a refused send can
retry with that same key; an admitted committed replay consumes budget but
adds no message or event. The [composer](web-composer.md) keeps ordinary text
and keys on 429; queued sends keep their text and key in the pending item for
Retry. A refused retry of an unknown outcome keeps that outcome unconfirmed.
