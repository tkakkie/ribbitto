# Load testing

What the end-to-end load tests (#216, the connection ceiling; #219, the
reconnect storm) assume about ribbitto's limits before they run. The
in-process cost per post is in [stream cost](stream-cost.md). Server
counters are described in [development metrics](dev-metrics.md).

**Keep it current:** #216's results and commands are in
[load results](load-results.md), #219's in
[reconnect storm results](load-results-restart.md), and their measurements
for the dispositions are below. Change a disposition here when a measurement changes it.

## Assumptions and dispositions

Three findings from the M3 security review (#249, tracked in #261) bound
what a run measures. Each has a disposition, agreed by the maintainer on
2026-10-02 (#298).

| | What | Disposition |
|---|---|---|
| A4 | The live render cache is bounded by entries (4096, `renderCapacity` in `internal/web/stream_renderer.go`), not by bytes. An entry holds one rendered message; a 4,000-character body escapes and appears twice, so one entry can approach tens of kilobytes. | **Measured; no byte budget for now** (maintainer, 2026-10-07, after #216). The cache keeps `renderCapacity` (4096) and its one-minute TTL. A full cache of worst-case renders is a theoretical case of about 167 MB of payload plus overhead. #216's worst-case step passed (1,000 streams, 10 posts/s, 300 renders of about 40.8 KB), and nothing measured shows the entry bound to be insufficient, so byte-based eviction would be speculative complexity. Revisit if the render cache proves to be a real memory limit: on small self-hosted machines, with more render variants, at higher event rates, or in profiling. |
| A5 | #216 ran without a process-wide cap, with only `DefaultMaxStreamsPerAccount` (16) per account ([stream limits](stream-limits.md)), so its ceiling was not hidden by one. | **Done (#623):** `RIBBITTO_MAX_STREAMS` caps streams per process, defaulting to 5,000 when unset (maintainer, 2026-10-07). #216 passed 7,000 and failed 10,000 active streams at 10 posts/s, so 5,000 leaves headroom. An explicit value must be a positive integer; empty, zero, negative or unparsable values fail at start. Either cap answers 429 before registration or SSE headers. No per-address cap. |
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

From the run in [load results](load-results.md); every step's figures are in
[load results by step](load-results-steps.md).

- **A4 (render cache):** at 1,000 streams and 10 posts/s for 30 s over
  HTTP/1.1, on a freshly started server, each step delivered 300 renders.
  - **Short bodies:** about 1.2 KB of payload each (365 KB in all); the heap
    in use rose from 21 to 37 MiB.
  - **Worst case:** 4,000-character bodies of `&` came to about 40.8 KB of
    payload each (12.3 MB in all); the heap in use rose from 38 to 98 MiB.
  - **The cache's memory is not measured.** A cached buffer can have more
    capacity than its length, and entries carry overhead, so the cache can
    hold more than the payloads. The run does not tell how the rest of the
    heap's rise divides between the cache and other allocations.
  - **Retained entries:** 600 distinct renders since the server started,
    under the 4096 bound.
  - **Full cache:** 4096 worst-case renders come to about 4096 × 40.8 KB ≈
    167 MB of payload for this workload. That is a lower bound on such a
    cache's memory, not a ceiling.
- **A5 (process-wide cap):**
  - **Highest passing idle step:** 60,000 streams over HTTP/1.1, the fixtures'
    size. The server held 60,019 file descriptors and a peak RSS of 3,349 MiB.
    Over the HTTP/1.1 idle steps, peak RSS per stream was 45.7–61.0 KiB.
  - **What fails first:**
    - with 10 posts/s, the database pool, between 7,000 and 10,000 streams;
    - idle through Caddy, the proxy's memory, between 40,000 and 50,000
      streams.
  - The process cap now defaults to 5,000 from these measurements (#623).
- **A7 (detached loads):** within each series, on one server process, each
  step's first snapshot showed 11–15 goroutines after the previous step's
  thousands of streams had closed, which remained near the baseline of 11. The run saw no pile-up of abandoned loads; these counts alone
  cannot prove that none remained. The snapshot 100 ms after closing can still
  catch a large step's connections being torn down (49,638 goroutines at
  40,000 streams).
- **A7, SIGTERM to exit (#219, 2026-10-08):** 23–80 ms with 300 to 5,000
  streams open and posts in flight, well within the 10 s exit deadline
  ([reconnect storm results](load-results-restart.md#exit-and-readiness)).
