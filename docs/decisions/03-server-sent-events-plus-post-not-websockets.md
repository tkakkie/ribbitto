# 3. Server-Sent Events plus POST, not WebSockets

**Decided:** the server pushes events over SSE; the browser sends actions as
ordinary requests.
**Why:** chat needs frequent server→client updates and occasional
client→server actions. SSE works with plain HTTP, reconnects on its own with
`Last-Event-ID`, and htmx supports it.
**Considered:** WebSockets — only needed for high-frequency two-way traffic
(collaborative editing, calls). The hub design does not depend on the
transport.
