# Web performance guard

The guard in `internal/web` checks markup growth without a database or timing
assertions. CI never judges time.

- A fixed English fixture at 50/100/200 messages separates fixed cost from
  per-message slope; 100 checks linearity. Counts must lie exactly on the line.
  Bytes allow a 256-byte residual for sequence digits and cyclic author names.
- The real first-render test requests the channel through the handler with a
  session and a fake history reader holding 120 messages. It serves the newest
  `conversation.PageSize` entries oldest-first, with older history available,
  and checks that many message items plus the served counts and bytes. Changing
  the first-render count (#1477) is a visible, justified budget change; it does
  not change the independent 50/100/200 structure test.
- The existing browser pool's `htmx-load-count` scenario counts
  `htmx:beforeProcessNode` events from document start, freezing at the first
  body `htmx:load`. It compares that count with the Go mirror of the exact
  served markup. A gated stream frame then proves that including later work
  gives a larger count. Wait deadlines bound tests; no duration is judged.
- Budget comments require issue references, tested with a missing-reference
  fixture. Adding a submit button per message must violate element, submit
  and htmx-node budgets.

## Budgets and ratchet

The authoritative fixed, per-message and first-render limits are in
[`feed_guard_budgets_test.go`](../../internal/web/feed_guard_budgets_test.go),
with approval issues. Whole-page byte and hx-carrier
caps are in [`feed_budget_test.go`](../../internal/web/feed_budget_test.go);
per-fixture element caps are in
[`feed_guard_test.go`](../../internal/web/feed_guard_test.go).
Channel and resolved-topic fixtures run all guards; the selected topic is unlisted
([approval](https://github.com/tkakkie/ribbitto/issues/2183#issuecomment-6687852298)).

Counts have zero headroom: elements bound DOM growth, forms/submits and
htmx-selected nodes bound initialization work, and hx carriers independently
ratchet request markup. Bytes have 5% headroom (rounded up) for class names,
text and translations; byte size is a less stable proxy than node counts.
The guard checks each fixed, per-message and first-render count budget equals
the value measured in that run (#1536): above measured fails the headroom
check; below measured fails the existing at-or-under check. Each byte budget
must lie between measured and `ceil(measured * 1.05)`, inclusive. A mutation
table raises each of the 15 count limits by one and proves rejection, including
zero slopes; predicate tests isolate headroom and at-or-under failures.
The browser mirror counts body itself plus its selected descendants, rather
than every hx carrier. Template contents live in a separate browser fragment
and are excluded from htmx selection; Go parsed markup counts still include them. The handler fixture includes older-history navigation
and its own sidebar, so its fixed markup differs from the structure fixture.

**Lowering:** a change reducing a count lowers its budget in the same PR.
The new measured value and budget then agree, so CI accepts both reductions
together; leaving the old count budget fails CI.

**Raising:** requires an issue stating the reason and the counted or measured
effect, and the maintainer's written approval on that issue. The constant's
comment points to that issue. The source check fails without an issue
reference; CI rejects a raised count budget unless the measured value rises
in step. Review verifies the reason and approval. A feature needing more
per-message markup states its count in its issue first.

Attribute-less buttons miss htmx 2.0.7's `[type='submit']` selector;
forms match its form selector.

## Mirror maintenance

The plain Go predicates are pinned to htmx 2.0.7 and the SSE extension 2.2.4.
The pin test checks the verb list, boost/form/submit/extension/trigger
selectors, SSE selectors and disable selector in the vendored sources;
`web/static/vendor_test.go` also pins their SHA-256 hashes. Re-check the mirror
when updating them under the [vendor procedure](../../web/static/vendor/README.md).
It asserts the page has no hx-disable/data-hx-disable, so disabled ancestors
need no special traversal. The separate hx-on:* pass emits no
beforeProcessNode event and is excluded.

## Milestone timing reviews

At milestone reviews, on desktop and phone, measure switching at 50 and 200
messages against the previous milestone's build. Report the per-message slope
and fixed part with their uncertainty. Apply #1163's criteria without relaxing
them: a cell fails if its median is worse than base by more than 10% or 5 ms,
whichever is larger, or its p90 by more than 15%. Use #1149's method: keep
headless and headed results separate and alternate builds in one session.

Measurement runs follow [host measurements](../workflow/investigations.md#host-measurements)
and start only on the maintainer's instruction. Whether the measurement
harness moves into the repository is a separate proposal, not decided here.
