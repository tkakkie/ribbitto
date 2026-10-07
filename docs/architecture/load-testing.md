# Load testing

What the end-to-end load tests (#216, the connection ceiling; #219, the
reconnect storm) assume about ribbitto's limits before they run. The
in-process cost per post is in [stream cost](stream-cost.md). Server
counters are described in [development metrics](dev-metrics.md).

**Keep it current:** #216's results and commands are in
[load results](load-results.md), and its measurements for the dispositions
are below. Change a disposition here when a measurement changes it.

## Assumptions and dispositions

Three findings from the M3 security review (#249, tracked in #261) bound
what a run measures. Each has a disposition, agreed by the maintainer on
2026-10-02 (#298).

| | What | Disposition |
|---|---|---|
| A4 | The live render cache is bounded by entries (4096, `renderCapacity` in `web/stream_renderer.go`), not by bytes. An entry holds one rendered message; a 4,000-character body escapes and appears twice, so one entry can approach tens of kilobytes. | **Test with explicit limits.** Each step records total heap and an estimate of the cache's own share (below), from values the run can already read. A byte budget is added only if those numbers show the entry bound is not enough. |
| A5 | There is no process-wide or per-address cap on open streams, only `DefaultMaxStreamsPerAccount` (16) per account ([stream limits](stream-limits.md)). | **Test without a cap**, so the ceiling #216 looks for is not hidden by one. After #216, a process-wide cap is sized from its first failing step and answered like the per-account cap: 429 before the stream starts. |
| A7 | Detached cache loads (`realtime.Cache`) are neither bounded nor cancelled when every waiter leaves, and can delay exit after shutdown. | **Done first:** #297 (#316) bounds and cancels them before either run, so neither measures a pile-up of abandoned loads and #219's SIGTERM-to-exit time measures the fixed behaviour. |

The event cache (`realtime.NewCachedEvents`, 1024 entries, one minute) is
bounded by entries too, but an entry holds IDs-only events and a batch is
bounded by the reader's page size, so it is not a separate finding. Its
numbers are named when #216 tunes them (#261 B13).

## What the runs record for these

- **A4:** at the end of each step, `runtime.heap_inuse_bytes` from
  [development metrics](dev-metrics.md), and approximate indicators of the
  render cache's share, not a bound on it:
  - **observed renders:** the distinct (channel, sequence, language)
    messages the step delivered, and the size of each one's `data:` payload
    (the cached HTML; a lower bound per entry, since a buffer's capacity can
    exceed its length);
  - **retained entries, estimated:** expired entries are dropped only when
    looked up again or evicted at capacity, so the cache may hold every
    distinct render since the process started, up to 4096 shared by all
    languages, whatever the last minute delivered.

  The step's body length and content are recorded with them. A worst-case
  step posts 4,000-character bodies of characters HTML escapes; comparing
  its heap with a short-body step at the same stream count indicates the
  cache's weight, while other allocations and garbage collection also move
  the heap. A heap profile is optional: the server exposes none, and adding
  one is separate work (#216).
- **A5:** attempted versus established streams (the load client and
  `streams.open`) and the server's open file descriptors, read from the
  operating system on the disposable Linux machine (`/proc/<pid>/fd`), at
  the highest passing step. The first failing step's limit (file
  descriptors, memory, or the database pool) sizes the cap.
- **A7:** with #297 merged, `runtime.goroutines` after a step's streams
  close (abandoned loads would remain as goroutines) and, in #219, the time
  from SIGTERM to exit.

## Measurements (#216, 2026-10-07)

From the run in [load results](load-results.md):

- **A4 (render cache):** at 1,000 streams and 10 posts/s for 30 s over
  HTTP/1.1, on a freshly started server, each step delivered 300 renders.
  - **Short bodies:** about 1.2 KB each (365 KB in all); the heap in use rose
    from 22 to 39 MB.
  - **Worst case:** 4,000-character bodies of `&` came to about 40.8 KB each
    (12.3 MB in all); the heap rose from 39 to 103 MB.
  - **What the heap includes:** the cache's own share is at most the
    payloads. The rest of the rise is other allocations, such as each
    connection's writes, not yet collected.
  - **Retained entries:** 600 distinct renders since the server started,
    under the 4096 bound.
  - **Bound:** a cache full of worst-case renders would hold about 4096 ×
    40.8 KB ≈ 167 MB.
- **A5 (process-wide cap):**
  - **Highest passing idle step:** 60,000 streams over HTTP/1.1, the fixtures'
    size. The server held 60,019 file descriptors and 3.3 GB of RSS, about
    50–57 KB per stream.
  - **What fails first:**
    - with 10 posts/s, the database pool, between 7,000 and 10,000 streams;
    - idle through Caddy, the proxy's memory, between 40,000 and 50,000
      streams.
  - A cap sized from these is follow-up work.
- **A7 (detached loads):**
  - After every step, `runtime.goroutines` was back to 11 by the next step's
    first snapshot, so no abandoned loads remained.
  - The snapshot 100 ms after closing can still catch a large step's
    connections being torn down (49,638 goroutines at 40,000 streams).
