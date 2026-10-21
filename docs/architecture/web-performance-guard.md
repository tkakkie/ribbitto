# Web performance guard

The guard in `internal/web` checks markup growth without a database or timing
assertions. CI never judges time.

- The structure test renders a fixed English fixture at 50, 100 and 200
  messages. The 50/200 endpoints separate the fixed part (intercept) from the
  per-message part (slope); 100 checks linearity. Integer counts must lie
  exactly on the line. Bytes allow a 256-byte middle-sample residual because
  event sequence digits and cyclic seed-author name lengths vary.
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
- The source check parses Go declarations and requires an issue reference in
  every budget constant/variable's comment. Its own missing-reference fixture
  proves rejection. A DOM-only regression test adds a submit button to every
  message and proves violations for elements, explicit submits and htmx nodes.

## Budgets and ratchet

The source is `internal/web/feed_guard_budgets_test.go`; each constant cites
an issue and explains its reason. Initial values cite #1478; #1500 ratchets
message actions, including #1461's pin attribute removal. Current measured
values and caps are:

| Metric | Fixed measured / cap | Per message measured / cap | First render measured / cap (50 messages) |
|---|---:|---:|---:|
| Elements | 181 / 181 | 29 / 29 | 1692 / 1692 |
| Forms | 11 / 11 | 1 / 1 | 61 / 61 |
| Explicit type=submit | 7 / 7 | 0 / 0 | 7 / 7 |
| hx-* carriers | 10 / 10 | 0 / 0 | 11 / 11 |
| htmx-selected nodes | 21 / 21 | 1 / 1 | 72 / 72 |
| HTML bytes | 20732.333 / 21769 | 4681.873 / 4916 | 262397 / 275517 |

The structure fixture at 50/100/200 messages measures bytes
254826/488887/957107. Its 50-message htmx mirror is 71 nodes; the
handler first render is 72. Attribute-less buttons do not match htmx
2.0.7's `[type='submit']` selector; the per-message pin form still does.
The older whole-page test remains: 1631 elements, 10 hx carriers and
267568 bytes (5% above 254826, rounded up), one byte stricter than the
standing guard's rounded fixed + 50 × slope byte caps. It checks the
whole total; this guard also checks growth, linearity and handler output.

Counts have zero headroom: elements bound DOM growth, forms/submits and
htmx-selected nodes bound initialization work, and hx carriers independently
ratchet request markup. Bytes have 5% headroom (rounded up) for class names,
text and translations; byte size is a less stable proxy than node counts.
The browser mirror counts body itself plus its selected descendants, rather
than every hx carrier. Template contents live in a separate browser fragment
and are excluded from htmx selection; Go parsed markup counts still include them. The handler fixture includes older-history navigation
and its own sidebar, so its fixed markup differs from the structure fixture.

**Lowering:** a change reducing a count lowers its budget in the same PR.
This ratchet is checked by review.

**Raising:** requires an issue stating the reason and the counted or measured
effect, and the maintainer's written approval on that issue. The constant's
comment points to that issue. The source check fails without an issue
reference; review verifies the reason and approval. A feature needing more
per-message markup states its count in its issue first.

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
