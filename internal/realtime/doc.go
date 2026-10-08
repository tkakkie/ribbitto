// Package realtime is the realtime module's root (decision 26): it delivers
// durable events to open connections through authorization, rendering,
// event-reading and sending interfaces; the SSE framing itself is web's.
//
// Module: realtime (feature map in docs/architecture/features.md). It
// owns event_log and publishes no event kinds: each publisher owns its kinds.
// Its store reads, appends and expires the log; its wiring builds it. The
// stream's authorization and rendering, and org's cursor bounds, come in as
// interfaces it defines, implemented by org's Authorizer, web's renderer
// and org's store (through orgpg.BoundsIn), wired in cmd/ribbitto.
//
// Hub is the process-wide core. Per organisation it keeps the highest
// committed event sequence it has been told about (Raise) and lets a
// connection block until that value passes its cursor (Wait) without a
// lost wakeup. It is also the registry of open connections: Register caps
// connections per process and per account and gives each one a context
// that CancelAccount or CancelSession ends. Events themselves are never
// held here; they are read from event_log, so a hub that loses a
// notification (or restarts) only delays delivery until the next raise.
// Watermark bounds that delay: it periodically raises the hub to the
// committed sequences of the organisations with connections, for commits
// that no Raise announced.
//
// A cursor the log can no longer serve gets a reset event instead of a
// partial replay. Shared reads and renders reduce per-connection work
// (#227; docs/architecture/stream-cost.md measures the effect). Retention
// raises the replay boundary through an injected interface, which org owns
// (#161), so expired events cannot leave a gap in replay.
package realtime
