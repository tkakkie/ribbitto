# Architecture

How ribbitto is put together and why. Read this before changing package
boundaries, the request flow or anything real-time.

**Keep it current:** update these files in the same pull request whenever
package responsibilities, allowed imports, the request or data flow, or the
real-time design change. Parts marked *planned* describe agreed designs that
are not implemented yet; move them out of *planned* when they land.

## Index

Read the file for the area you change:

| File | Covers |
|---|---|
| [`packages.md`](packages.md) | packages, their responsibilities and allowed imports |
| [`import-checks.md`](import-checks.md) | how the module manifest test (`module_imports_test.go`), depguard (non-module rules) and `make check` enforce imports and `doc.go` |
| [`query-gate.md`](query-gate.md) | the rules tablecheck and scopecheck apply to production queries: organisation scope, accepted expressions and relation shapes |
| [`features.md`](features.md) | the feature map: each feature's packages and tables, and the known exceptions |
| [`cross-feature-access.md`](cross-feature-access.md) | how features reach each other's data: stores, posting's transaction, history and the page snapshot |
| [`modules.md`](modules.md) | module construction, the modules, what they own and may import |
| [`request-flow.md`](request-flow.md) | the request flow, organisation routes, server timeouts, middleware order |
| [`setup-and-signup.md`](setup-and-signup.md) | first-run setup and sign-up |
| [`identity.md`](identity.md) | sessions, signing in and out, the session cookie |
| [`rate-limits.md`](rate-limits.md) | authentication and member limits; the reverse-proxy contract |
| [`posting.md`](posting.md) | posting a message: the sequence-first transaction, the hub after commit, the page snapshot |
| [`realtime.md`](realtime.md) | the durable event log: payloads, routing, replay boundary and retention |
| [`ephemeral-state.md`](ephemeral-state.md) | presence and typing as current state: generations, owners, delivery and reset |
| [`typing.md`](typing.md) | memory-only typing summaries, lifetime, composer ordering, signal ingress, delivery and the page's status region |
| [SSE](streaming.md), [scope](stream-scope.md) | Server-Sent Events: the connection, retention, authorization and revocation, resource limits |
| [`recount-scheduler.md`](recount-scheduler.md) | bounded scheduler core, eligibility, completion mailbox and cancellation; explicit opt-in only |
| [`replay.md`](replay.md) | ordering and replay of events, resets, and messages that vanished |
| [`pins.md`](pins.md) | pin ownership and live/reconnect delivery (planned, #880) |
| [`stream-limits.md`](stream-limits.md) | Stream resource limits: cache loads, write deadlines, heartbeats, the stream caps, shutdown, HTTP/2 |
| [`rendering.md`](rendering.md) | templates, assets, the Content Security Policy, languages |
| [`dev-metrics.md`](dev-metrics.md) | the development-only metrics listener for load tests |
| [`stream-authorization.md`](stream-authorization.md) | keeping deliveries authorized without a query per stream per event: the options and the decision (#622) |
| [`stream-authorization-contention.md`](stream-authorization-contention.md) | the cached-allow check's lock and wake contention (#703): what changed, and the ceilings and profiles before and after |
| [`stream-authorization-results.md`](stream-authorization-results.md) | what #670's cached allows cost, measured by #671: epoch reads, hit rate, queries and latency, and what hit the limit first |
| [`shared-reader.md`](shared-reader.md) | *planned* (#232): one reader per organisation, its window, the hand-over from replay to live, slow connections, and what should decide whether to build it |
| [`shared-reader-growth.md`](shared-reader-growth.md) | *planned*: how the shared reader grows into a channel interest index and bounded replay, and its implementation issues |
| [`unread-counts.md`](unread-counts.md) | read-state storage and the counting statements between the modules |
| [`unread-count-costs.md`](unread-count-costs.md) | what the counts cost, expected load, fragmentation and the unread benchmark |
| [`unread-writes.md`](unread-writes.md) | how read ranges are written: the current feed, topic-view, branch-notice and posting writes |
| [`unread-counters-design.md`](unread-counters-design.md) | #955: proposed write-time counter maintenance; linked operations, concurrency, repair and pre-measurement adoption criteria |
| [`unread-counters-verification.md`](unread-counters-verification.md) | #956: what the test-only counter prototype has verified against #950's oracle, and its limits |
| #957: [SQL adapter](unread-counters-sql-adapter.md), [workload](unread-counters-workload.md) | test-only SQL, accounting, recounts/repair and fixed mixed schedule; unmeasured |
| [`sidebar-hybrid-coverage.md`](sidebar-hybrid-coverage.md) | #961 part 2c-a: pinned coverage, stale-input rejection and retained exact replay |
| [`sidebar-hybrid-progress.md`](sidebar-hybrid-progress.md) | #961 part 2c-b2: bounded recount/repair delivery under continuous posts, pinned full-model equality |
| [`sidebar-hybrid-delivery.md`](sidebar-hybrid-delivery.md) | #961 part 2c-b1: stream lifecycle, authorization, send order and reconnect recovery |
| [`sidebar-hybrid-hypothetical.md`](sidebar-hybrid-hypothetical.md) | #960b: separate hypothetical metadata, exact deltas and reconciliation limits |
| [`sidebar-hybrid-contract.md`](sidebar-hybrid-contract.md) | #950: production-only derivability, snapshot coverage and exact model/comparison for the test-only hybrid investigation |
| [`unread-benchmark-results.md`](unread-benchmark-results.md) | the read-range benchmark's results (#283) and the go/no-go decision |
| [`stream-cost.md`](stream-cost.md) | what the delivery loop costs per post as streams grow, and how to measure it |
| [`read-advance-results.md`](read-advance-results.md) | short read-advance costs, member isolation, matched posting/branching unions and #711 acceptance |
| [`stream-sidebar-results.md`](stream-sidebar-results.md) | measured cost of live sidebar recounts per stream with distinct members (#812) |
| [`stream-sidebar-bottleneck-results.md`](stream-sidebar-bottleneck-results.md) | where live sidebar latency goes at thousands of streams, and two test-only experiments (#826) |
| [`stream-sidebar-pool-results.md`](stream-sidebar-pool-results.md) | #832 part 1: protocol, freshness and synchronous writer witness, connection budget; controlled results linked, production validation open |
| [`stream-sidebar-queue-results.md`](stream-sidebar-queue-results.md) | #832 PR 2: bounded fair scheduler, cancellation, completion mailbox and mutation evidence; test-only writer composition in PR 3 |
| [`stream-sidebar-writer-results.md`](stream-sidebar-writer-results.md) | #832 PR 3: test-only single-writer composition, delivery between batches, audience/cursor safety, revocation and mutation evidence; production gaps |
| [`stream-sidebar-arms-results.md`](stream-sidebar-arms-results.md) | #832 PR 4: fixed-budget comparison arms, aggregate pool/queue telemetry and backlog exclusion |
| [`stream-sidebar-arms-measured.md`](stream-sidebar-arms-measured.md) | #832 controlled results: completion, writer fix, five-minute queue evidence, pool tradeoff and candidate pending stability/production validation |
| [`stream-sidebar-arms-tables.md`](stream-sidebar-arms-tables.md) | #832 all arm/group workload totals, latency medians and min–max spreads, target/eligibility passes |
| [`stream-sidebar-arms-accounting.md`](stream-sidebar-arms-accounting.md) | #832 all arm/group SQL/count/recount/acquisition/wait totals, peak leases, elapsed time and throughput |
| [`stream-sidebar-stability.md`](stream-sidebar-stability.md) | #832 stability work parts 1–2: pre-registered queue stability criterion and complete regression evidence |
| [`stream-sidebar-stage1.md`](stream-sidebar-stage1.md) | #832 stability work part 3: 36-connection configurations, 24-window runner and pre-registered stage-1 plan |
| [`stream-sidebar-stage2.md`](stream-sidebar-stage2.md) | #865 PR 3: fixed mixed-load protocol, gates and versioned result contracts; execution requires explicit instruction |
| [`stream-sidebar-stage2-drivers.md`](stream-sidebar-stage2-drivers.md) | #865 PR 3: authenticated post, page, read and stream drivers; evidence, lifecycle and proof limits |
| Stage-2 slices 5–6a: [telemetry](stream-sidebar-stage2-telemetry.md), [headroom](stream-sidebar-stage2-headroom.md), [stability](stream-sidebar-stage2-stability.md), [capture](stream-sidebar-stage2-witness.md) | #865 PR 3: continuous evidence and gate evaluators |
| [`load-testing.md`](load-testing.md) | what the end-to-end load tests assume about limits, and their dispositions |
| [`load-results.md`](load-results.md) | end-to-end load test results: the connection ceiling and what hit the limit first |
| [`load-results-steps.md`](load-results-steps.md) | every step of the load test run: streams, posts, memory and server snapshots |
| [`load-restart.md`](load-restart.md) | the load client's restart harness: an owned child server, its exit, and the drain to the final watermark |
| [`load-results-restart.md`](load-results-restart.md) | reconnect storm results: restarting with every stream open, exit time, recovery, replay and the comparison |
| [Web layers](web-layers.md) | what handlers, templ, htmx and [JavaScript](web-javascript.md) are each responsible for |
| [`web-layers-checks.md`](web-layers-checks.md) | how the web-layer rules are checked, and the M3 audit |

Generated module API summaries: [identity](../api/identity.txt), [realtime](../api/realtime.txt),
[org](../api/org.txt), [conversation](../api/conversation.txt), [typing](../api/typing.txt) and [unread](../api/unread.txt).
Run `make api` after an API or doc-comment change; `make check` compares them
without rewriting files. Entries and members are sorted by name, with signatures
and first doc sentences; unexported declarations and their members are omitted.
Loading uses `go/packages` for Linux/amd64 with cgo disabled.

## See also

- [`docs/domain/README.md`](../domain/README.md) — glossary and index; [`entities.md`](../domain/entities.md), [`invariants.md`](../domain/invariants.md), [`unread.md`](../domain/unread.md)
- [`docs/domain/names.md`](../domain/names.md) — display names, handles, how members are shown
- [`docs/database.md`](../database.md) — local database, migrations; [`docs/database-tests.md`](../database-tests.md) — integration tests and fixtures; [`docs/seed-data.md`](../seed-data.md) — development seed data
- [`docs/worktree-env.md`](../worktree-env.md) — dev databases, port allocation, health checks and cleanup
- [`docs/schema/README.md`](../schema/README.md) — generated reference for the current schema
- [`DECISIONS.md`](../../DECISIONS.md) — why things are the way they are: the index of [`decisions/`](../decisions/)
