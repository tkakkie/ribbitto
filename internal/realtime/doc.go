// Package realtime delivers events over SSE through authorization, rendering, and event-reading interfaces.
//
// Feature: realtime (feature map in docs/architecture/README.md). It
// imports only domain; the stream's authorization, rendering and event
// reading come in as interfaces it defines, implemented by app, web and
// infra/postgres and wired in cmd/ribbitto.
//
// Hub is the process-wide core. Per organisation it keeps the highest
// committed event sequence it has been told about (Raise) and lets a
// connection block until that value passes its cursor (Wait) without a
// lost wakeup. It is also the registry of open connections: Register caps
// connections per account and gives each one a context that CancelAccount
// or CancelSession ends. Events themselves are never held here; they are
// read from event_log, so a hub that loses a notification (or restarts)
// only delays delivery until the next raise.
package realtime
