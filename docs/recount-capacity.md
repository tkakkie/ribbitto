# Recount capacity

## Current computation sizing (#1046)

One process-wide `RecountLimit` covers connect, read, resync and move recounts,
separately from stream registrations. It is sized as `max(1, floor(C/2))`, where
C is the effective pgx pool maximum (including the database URL's
`pool_max_conns` and pgx defaults). This reserves `ceil(C/2)` connections for
normal work when C >= 2. A one-connection pool permits one recount and cannot
reserve a separate normal connection; deployments needing headroom must provide
at least two connections. No separate configuration is required. Startup logs
the effective pool maximum, computation limit and stream registration limit.

Slot acquisition waits at most 25 ms. Expiry defers the computation: dirty state
and watch coverage remain pending, and a new attempt follows after 250–1,000 ms
of jitter. Slots are released only when the actual computation returns.
Established streams do not return 503 on deferral.

Connect sets a recount deadline randomly within 0–3 s. Durable frames, owner
frames and authorization continue during that delay; no computation slot is
occupied until the deadline. This bounds sidebar computation only: ordinary
page loads retain their exact SQL and gain no admission limit.
