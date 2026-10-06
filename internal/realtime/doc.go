// Package realtime is the realtime module's root (decision 26): it delivers
// durable events to open connections through authorization, rendering,
// event-reading and sending interfaces; the SSE framing itself is web's.
//
// Module: realtime (feature map in docs/architecture/features.md). It
// declares the durable event types (Event, an envelope it routes without
// kind names; EventKind, whose values each publisher declares;
// ErrCursorExpired) and
// the kind registry (Kinds of each publisher's Router, merged by MergeKinds), and
// imports only kernel (ID) and platform (the transaction handles). Its store
// (internal/realtime/internal/postgres) reads, appends and expires the event
// log, and its wiring (internal/realtime/realtimepg) builds it. The
// stream's authorization and rendering, and org's cursor bounds, come in as
// interfaces it defines, implemented by org's Authorizer, web's renderer
// and org's store (through orgpg.BoundsIn), wired in cmd/ribbitto.
//
// Hub is the process-wide core. Per organisation it keeps the highest
// committed event sequence it has been told about (Raise) and lets a
// connection block until that value passes its cursor (Wait) without a
// lost wakeup. It is also the registry of open connections: Register caps
// connections per account and gives each one a context that CancelAccount
// or CancelSession ends. Events themselves are never held here; they are
// read from event_log, so a hub that loses a notification (or restarts)
// only delays delivery until the next raise. Watermark bounds that delay:
// it periodically raises the hub to the committed sequences of the
// organisations with connections, for commits that no Raise announced.
//
// Stream.Run is the per-connection delivery loop over the EventReader,
// Authorizer and Renderer interfaces defined here; its doc comment states
// which events are skipped and which failures stop it without advancing the
// cursor; a cursor the log can no longer serve gets a reset event instead
// of a partial replay. Cache and CachedEvents let an organisation's streams
// share reads and renders (#227); docs/architecture/stream-cost.md measures
// the effect. Retention expires old events through EventCleaner (the store's
// Cleaner) and raises the replay boundary through the injected
// RetentionBoundary, which org owns (#161).
package realtime
