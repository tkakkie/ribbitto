# Measurements

Recorded load tests, benchmarks and investigations. See the
[architecture index](../architecture/README.md) for the designs and measurement harnesses.

| File | Covers |
|---|---|
| [`load-results.md`](load-results.md) | end-to-end load test results: the connection ceiling and what hit the limit first |
| [`load-results-steps.md`](load-results-steps.md) | every step of the load test run: streams, posts, memory and server snapshots |
| [`load-results-restart.md`](load-results-restart.md) | reconnect storm results: restarting with every stream open, exit time, recovery, replay and the comparison |
| [`read-advance-results.md`](read-advance-results.md) | short read-advance costs, member isolation, matched posting/branching unions and #711 acceptance |
| [`sidebar-counts-history.md`](sidebar-counts-history.md) | the stopped sidebar-recount investigation (#812–#957), its findings, and why client-side counts replaced it |
| [`stream-authorization-contention.md`](stream-authorization-contention.md) | the cached-allow check's lock and wake contention (#703): what changed, and the ceilings and profiles before and after |
| [`stream-authorization-results.md`](stream-authorization-results.md) | what #670's cached allows cost, measured by #671: epoch reads, hit rate, queries and latency, and what hit the limit first |
| [`stream-client-counts-results.md`](stream-client-counts-results.md) | measured cost of client-side unread counts, author-only recounts and final resync (#1047) |
| [`stream-sidebar-results.md`](stream-sidebar-results.md) | measured cost of live sidebar recounts per stream with distinct members (#812) |
| [`unread-benchmark-results.md`](unread-benchmark-results.md) | the read-range benchmark's results (#283) and the go/no-go decision |
