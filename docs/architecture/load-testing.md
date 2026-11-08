# Load testing

What the end-to-end load tests (#216, the connection ceiling; #219, the
reconnect storm) assume about ribbitto's limits before they run. The
in-process cost per post is in [stream cost](stream-cost.md). Server
counters are described in [development metrics](dev-metrics.md).

#216's results and commands are in
[load results](../measurements/load-results.md) and #219's in
[reconnect storm results](../measurements/load-results-restart.md).

## Assumptions and dispositions

Three findings bound what a run measures.

| | What | Disposition |
|---|---|---|
| A4 | The live render cache is bounded by entries (4096, `renderCapacity` in `internal/web/stream_renderer.go`), not by bytes. An entry holds one rendered message; a 4,000-character body escapes and appears twice, so one entry can approach tens of kilobytes. | **Measured; no byte budget for now**. The cache keeps `renderCapacity` (4096) and its one-minute TTL. A full cache of worst-case renders is a theoretical case of about 167 MB of payload plus overhead. #216's worst-case step passed (1,000 streams, 10 posts/s, 300 renders of about 40.8 KB), and nothing measured shows the entry bound to be insufficient, so byte-based eviction would be speculative complexity. Revisit if the render cache proves to be a real memory limit: on small self-hosted machines, with more render variants, at higher event rates, or in profiling. |
| A5 | #216 (2026-10-07; no process cap, per-account 16): 7,000 streams passed, 10,000 failed at 10 posts/s. | **Done (#623):** default 5,000 when `RIBBITTO_MAX_STREAMS` is unset. [Resource limit; reason, override and no guarantee](stream-limits.md). |
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
  the highest passing step. The first failure identifies what limited the
  run (file descriptors, memory, or the database pool).
- **A7:** with #297 merged, `runtime.goroutines` after a step's streams
  close (abandoned loads would remain as goroutines) and, in #219, the time
  from SIGTERM to exit.

## Measurements (#216, 2026-10-07)

See [load results](../measurements/load-results.md), [step results](../measurements/load-results-steps.md)
and [restart results](../measurements/load-results-restart.md#exit-and-readiness).

The A4 short-body payload total was 365 KB; heap in use rose from 21 to
37 MiB. Worst-case payloads totalled 12.3 MB; heap rose from 38 to 98 MiB.
The 600 distinct renders stayed under the 4096-entry bound. These payloads
are lower bounds on cache memory: buffer capacity and entry overhead add
more, and the run did not isolate the cache's share of the heap rise.
