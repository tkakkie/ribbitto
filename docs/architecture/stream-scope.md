# Stream scope

Page interests, stream parameters and browser swap targets.

## Stream scope

[Decision 30](../decisions/30-one-organisation-wide-stream-per-tab-filtered-by-its-interests.md)
gives every page one stream for its organisation, filtered by the page's
interests; [decision 31](../decisions/31-presence-and-typing-are-current-state-with-a-generation.md)
delivers presence and typing as current state ([ephemeral state](ephemeral-state.md#ephemeral-state)).

`GET /organizations/{slug}/events` accepts these parameters. `messages`
delivers durable frames; `typing` and `presence` read their wired
owners after each durable batch and before waiting, at most one frame per
kind. Presence checks the organisation, typing
the frame's channel, immediately before sending. Bound-member denies and errors stop the stream. Web's presence (#768) and viewer-filtered typing (#788) adapters are wired in production. Sidebar recounts are wired in production (#819); composition details are in [unread counts](unread-counts.md).

The endpoint takes:

| Parameter | Meaning |
|---|---|
| `after` | the durable cursor; `Last-Event-ID` wins |
| `want` | comma-separated interests, each at most once: `sidebar`, `messages`, `typing`, `presence`; anything else is 400 |
| `channel` | the page's channel: required with `messages`, `typing` or `sidebar` (for its initial recount); with `sidebar` it selects whose topics the sidebar counts; a channel the member cannot see is 404 |
| `topic` | optional with `messages`, `typing` or `sidebar`; requires `channel`, narrows messages/typing and retains sidebar selection; another organisation or channel is 404 |
| `panel` | optional `members`; ignored since #874 |
| `presence-after` | required with `presence`: the process instance and presence generation the page was rendered with |

| Page | `want` |
|---|---|
| Feed (latest page) | `sidebar,messages,typing` with `channel` |
| Topic view (latest page) | `sidebar,messages,typing` with `channel` and `topic` |
| `?before=` page | `sidebar` with `channel` and, on topic pages, `topic` |
| Channel list page | `sidebar` with `channel`, the channel whose topics it nests |
| With the members panel | the page's own interests plus `presence`, with `presence-after` |

Every page, including `?before=`, carries a snapshot cursor on
`#organization-stream` and holds a slot under both caps. Only `message`
and `messages-moved` frames advance the browser's durable cursor.

Ephemeral delivery filters organisation, interest and typing channel/topic
before authorization, preserving the durable cursor and heartbeat deadlines.
Typing receives server-resolved member identity and after=-1 on connect,
restoring current content, even empty, in `#typing-status`. Later reads acknowledge the organisation level
without repeating unchanged places ([owner contract](typing.md#production-delivery)).
Presence receives the page
token: one coalesced entry per member, in last-change order, at most 100.
Offline entries stay five minutes; discarding one advances a monotonic boundary.
The state, boundary and generation come from one locked read. Another process's
token, a start below the boundary or over 100 changes require `reset` on connect
or later; equality at the boundary is served. The presence adapter sends
that reset without `id:`, ending the stream so the page reloads.
Sidebars recount on connect, posts/moves and the member's read advances,
regardless of message interests. With a hub, Reading and watches share
one `unread.Changes`; otherwise the publisher is nil. Subscribe before recounting;
release on exit. Capture before reads; send or deny covers that generation.
Scheduled captures are per request. Scheduled tests pin a fresh bound check
before every write, and no write after revocation or cancellation.
Changes dirty a [fixed deadline](stream-limits.md) checked
between batches and in the combined wait. Authorize organisation and bound member
after rendering, before sending. Frames omit `id:`; errors stop delivery. Reconnect recounts.
[Short results](read-advance-results.md) record costs and acceptance. The
[single-process contract](ephemeral-state.md#member-read-change-watches) and
[unknown-COMMIT exception](unread-writes.md) limit recovery.
Swap targets:

| Event | `id:` | Where htmx puts it |
|---|---|---|
| `message`, `messages-moved` | the sequence | `#message-items` |
| `sidebar` | none | whole sidebar lists and selected heading count, out of band (#818) |
| `presence` | none | each member's indicator, out of band by its DOM id (#768) |
| `typing` | none | `#typing-status` content replacement on latest feeds/topics (#790) |
| `reset` | the cursor for durable reset; none for owner reset | the stream script reloads the page |

`#organization-stream`, holding `sse-connect`, wraps the sidebar, conversation and any side panel, because the htmx
SSE extension attaches an `sse-swap` element to its closest ancestor with a
source. Messages have their own `sse-swap` on latest pages; older pages omit it.
Typing replaces content outside the composer. Per-entry events go to the hidden
`#stream-sink` inside this container, `sse-swap="sidebar,presence"` with
`hx-swap="none"`, whose payload holds only `hx-swap-oob` elements: the
extension swaps through htmx's own swap, which applies out-of-band elements
even when the main swap is `none`, and an entry not on the page is ignored.
The sink is hidden from assistive technology and takes no focus.
`TestBrowser` proves replacement and missing-target behavior with
test-only entries rendered with `html/template`, and that native `Last-Event-ID` and the page's
reconnect cursor stay unchanged after ephemeral frames. Production presence and sidebar frames share the page components (#768, #818, #819). Opening, switching or closing a
panel is a navigation. Its bounded, paged list has no live list updates. One
presence snapshot supplies every indicator and the page token; changes replace
only loaded indicators, with visible localized labels and the success dot.
