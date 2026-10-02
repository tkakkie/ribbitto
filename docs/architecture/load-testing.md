# Load testing

What the end-to-end load tests (#216, the connection ceiling; #219, the
reconnect storm) assume about ribbitto's limits before they run. The
in-process cost per post is in [stream cost](stream-cost.md). Server
counters are described in [development metrics](dev-metrics.md).

**Keep it current:** #216 adds its results, commands and the measurements
below; change a disposition here when a measurement changes it.

## Assumptions and dispositions

Three findings from the M3 security review (#249, tracked in #261) bound
what a run measures. Each has a disposition, agreed by the maintainer on
2026-10-02 (#298).

| | What | Disposition |
|---|---|---|
| A4 | The live render cache is bounded by entries (4096, `renderCapacity` in `web/stream_renderer.go`), not by bytes. An entry holds one rendered message; a 4,000-character body escapes and appears twice, so one entry can approach tens of kilobytes. | **Test with explicit limits.** Each step records the process's heap (`runtime/metrics`, or `pprof` where available) next to #218's counters, so the cache's share is visible. A byte budget is added only if the numbers show the entry bound is not enough. |
| A5 | There is no process-wide or per-address cap on open streams, only `DefaultMaxStreamsPerAccount` (16) per account ([streaming](streaming.md#resource-limits)). | **Test without a cap**, so the ceiling #216 looks for is not hidden by one. After #216, a process-wide cap is sized from its first failing step and answered like the per-account cap: 429 before the stream starts. |
| A7 | Detached cache loads (`realtime.Cache`) are neither bounded nor cancelled when every waiter leaves, and can delay exit after shutdown. | **Implement first:** #297 bounds and cancels them before either run, so neither measures a pile-up of abandoned loads and #219's SIGTERM-to-exit time measures the fixed behaviour. |

The event cache (`realtime.NewCachedEvents`, 1024 entries, one minute) is
bounded by entries too, but an entry holds IDs-only events and a batch is
bounded by the reader's page size, so it is not a separate finding. Its
numbers are named when #216 tunes them (#261 B13).

## What the runs record for these

- **A4:** heap in use and the render cache's entry count at the end of
  each step. The count is the capacity once a step posts more than 4096
  distinct messages per language.
- **A5:** attempted versus established streams and open file descriptors
  at the highest passing step. The first failing step's limit (file
  descriptors, memory, or the database pool) sizes the cap.
- **A7:** with #297 merged, the number of loads in flight at the end of
  each step and, in #219, the time from SIGTERM to exit.
