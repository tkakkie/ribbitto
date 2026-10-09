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
| [`features.md`](features.md) | the feature map: each feature's packages and tables, and the known exceptions |
| [`cross-feature-access.md`](cross-feature-access.md) | how features reach each other's data: stores, posting's transaction, history and the page snapshot |
| [`modules.md`](modules.md) | module construction, the modules, what they own and may import |
| [`request-flow.md`](request-flow.md) | the request flow, organisation routes, server timeouts, middleware order |
| [`setup-and-signup.md`](setup-and-signup.md) | first-run setup and sign-up |
| [`identity.md`](identity.md) | sessions, signing in and out, the session cookie |
| [`rate-limits.md`](rate-limits.md) | authentication rate limits and the reverse-proxy contract |
| [`posting.md`](posting.md) | posting a message: the sequence-first transaction, the hub after commit, the page snapshot |
| [`realtime.md`](realtime.md) | the durable event log: payloads, routing, replay boundary and retention |
| [`streaming.md`](streaming.md) | Server-Sent Events: the connection, retention, authorization and revocation, resource limits |
| [`replay.md`](replay.md) | ordering and replay of events, resets, and messages that vanished |
| [`stream-limits.md`](stream-limits.md) | Stream resource limits: cache loads, write deadlines, heartbeats, the stream caps, shutdown, HTTP/2 |
| [`rendering.md`](rendering.md) | templates, assets, the Content Security Policy, languages |
| [`dev-metrics.md`](dev-metrics.md) | the development-only metrics listener for load tests |
| [`stream-authorization.md`](stream-authorization.md) | keeping deliveries authorized without a query per stream per event: the options and the decision (#622) |
| [`stream-authorization-contention.md`](stream-authorization-contention.md) | the cached-allow check's lock and wake contention (#703): what changed, and the ceilings and profiles before and after |
| [`stream-authorization-results.md`](stream-authorization-results.md) | what #670's cached allows cost, measured by #671: epoch reads, hit rate, queries and latency, and what hit the limit first |
| [`shared-reader.md`](shared-reader.md) | *planned* (#232): one reader per organisation, its window, the hand-over from replay to live, slow connections, and what should decide whether to build it |
| [`shared-reader-growth.md`](shared-reader-growth.md) | *planned*: how the shared reader grows into a channel interest index and bounded replay, and its implementation issues |
| [`unread-counts.md`](unread-counts.md) | *planned:* how read state is stored, written and counted, and what the counts cost |
| [`unread-benchmark-results.md`](unread-benchmark-results.md) | the read-range benchmark's results (#283) and the go/no-go decision |
| [`stream-cost.md`](stream-cost.md) | what the delivery loop costs per post as streams grow, and how to measure it |
| [`load-testing.md`](load-testing.md) | what the end-to-end load tests assume about limits, and their dispositions |
| [`load-results.md`](load-results.md) | end-to-end load test results: the connection ceiling and what hit the limit first |
| [`load-results-steps.md`](load-results-steps.md) | every step of the load test run: streams, posts, memory and server snapshots |
| [`load-restart.md`](load-restart.md) | the load client's restart harness: an owned child server, its exit, and the drain to the final watermark |
| [`load-results-restart.md`](load-results-restart.md) | reconnect storm results: restarting with every stream open, exit time, recovery, replay and the comparison |
| [`web-layers.md`](web-layers.md) | what handlers, templ, htmx and JavaScript are each responsible for |
| [`web-layers-checks.md`](web-layers-checks.md) | how the web-layer rules are checked, and the M3 audit |

Generated module API summaries: [identity](../api/identity.txt), [realtime](../api/realtime.txt),
[org](../api/org.txt) and [conversation](../api/conversation.txt).
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
