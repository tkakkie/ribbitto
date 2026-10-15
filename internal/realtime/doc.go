// Package realtime is the realtime module's root (decision 26): it delivers
// durable events and current ephemeral frames through authorization, rendering,
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
// lost wakeup. Presence and typing each have a generation per organisation,
// raised through the same compare-and-swap publication. Each level and wake
// channel are published together atomically, without a registry lock on the
// waiter's path. The single stream writer waits on the durable level, only
// its declared kinds' generations, its heartbeat and its context together.
// After draining, only a newer uncovered durable level requires a log read;
// member watches, ephemeral generations and recount deadlines reuse that coverage.
// Owner reads supply bounded frames after batches and on generation wakes, authorized
// immediately before sending without moving the cursor. Sidebar sources supply
// full recounts at connect and on consumer-defined triggers. Production posts
// trigger only the author's streams; moves trigger only current-channel streams.
// Member watches cover reads and resyncs. Posts not selected as messages send a
// shared unread frame through UnreadRenderer for the sidebar interest, even
// with no sidebar source.
// These frames carry no SSE id and pass audience and final bound-member checks.
// Completed unread-only/skipped progress checkpoints at most once per heartbeat
// interval after a fresh organisation/bound-member check, independently of
// browser application; without heartbeats it checkpoints before waiting.
// Optional member watches subscribe before the first recount and release on exit.
// A shared RecountLimit bounds computation independently of stream registrations;
// a bounded slot wait defers busy recounts without losing dirty state.
// Generations captured before fresh snapshots are covered only after send or
// denial; later publications dirty the same coalescer and join the combined wait.
// This is process-local; activation and cross-process recovery are separate work.
// A dirty flag and fixed deadline coalesce recounts between batches; neither a
// quiet period nor a backlog loses pending work. Rendering precedes authorization,
// and sidebar frames check the organisation and bound member, preserving the
// durable cursor.
// Channel-state sources supply bounded whole-state refreshes after cursor
// validation on connect, then on channel-scoped durable triggers independently
// of message/topic selection. A dirty flag and fixed deadline use the same
// writer and combined wait; authorization checks the connection's channel and
// bound member immediately before sending without an SSE id. Web adapters
// supply kinds, rendering and language-specific sharing; none are wired yet.
// Production presence and
// typing are implemented; web renders their indicators.
// Web's hidden sink receives sidebar/presence payloads. The hub is also the
// registry of open connections: Register caps
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
