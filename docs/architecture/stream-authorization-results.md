# Stream authorization results

What #670's cached allows and shared epoch reads
([decision](stream-authorization.md#decision)) cost, measured by #671: the
stream-cost benchmark ([how it runs](stream-cost.md)) and a rerun of #216's
active steps ([how they run](load-results.md#reproducing-it)).

## Benchmark, 2026-10-09

Apple M-series, darwin/arm64, 10 CPUs, Go 1.27.2; PostgreSQL 18 in Docker on
the same machine; pgx's default pool (10); batch 100; 10 posts/s for 10 s;
`_CACHE=1`. Steps 1,000 to 20,000 in four cases; the one-member case went on to
30,000 and 40,000, and the distinct case was rerun at 9,000 and 11,000. Every
step completed its 100 posts. Latency runs from `Post` returning to the send
and includes the stagger wait; *check* is the time spent in `MayReceive`
alone.

| Case | Streams | Queries/post | Epoch reads/post | …/post/stream | Hit rate | p50 / p95 / p99 | Check p95 | Result |
|---|---|---|---|---|---|---|---|---|
| one member | 1,000 | 27.1 | 5.13 | 0.0051 | 0.990 | 12 / 15 / 19 ms | 4.6 ms | pass |
| one member | 5,000 | 16.2 | 4.16 | 0.0008 | 1.000 | 30 / 35 / 37 ms | 12 ms | pass |
| one member | 10,000 | 16.2 | 4.24 | 0.0004 | 1.000 | 47 / 54 / 58 ms | 19 ms | pass |
| one member | 20,000 | 17.1 | 4.73 | 0.0002 | 1.000 | 98 / 135 / 147 ms | 40 ms | pass |
| one member | 30,000 | 19.1 | 5.89 | 0.0002 | 1.000 | 220 / 282 / 301 ms | 73 ms | pass |
| one member | 40,000 | 19.3 | 7.47 | 0.0002 | 1.000 | 1.15 / 1.85 / 1.97 s | 102 ms | fail: p95 |
| distinct | 1,000 | 26.6 | 4.60 | 0.0046 | 0.990 | 12 / 15 / 17 ms | 4.9 ms | pass |
| distinct | 5,000 | 26.0 | 4.03 | 0.0008 | 0.998 | 32 / 38 / 41 ms | 14 ms | pass |
| distinct | 10,000 | 46.2 | 4.18 | 0.0004 | 0.997 | 48 / 60 / 64 ms | 24 ms | pass |
| distinct | 11,000 | 1,060 | 5.08 | 0.0005 | 0.905 | 57 / 91 / 110 ms | 48 ms | pass |
| distinct | 15,000 | 5,307 | 5.57 | 0.0004 | 0.647 | 7.4 / 13.9 / 14.4 s | 407 ms | fail: p95 |
| stagger 10 ms | 1,000 | 78.0 | 56.0 | 0.0560 | 0.990 | 14 / 20 / 24 ms | 1.1 ms | pass |
| stagger 10 ms | 5,000 | 33.3 | 11.3 | 0.0023 | 0.998 | 34 / 40 / 43 ms | 13 ms | pass |
| stagger 10 ms | 10,000 | 48.4 | 6.39 | 0.0006 | 0.997 | 55 / 66 / 70 ms | 24 ms | pass |
| stagger 100 ms | 1,000 | 786 | 724 | 0.7242 | 0.990 | 55 / 100 / 113 ms | 0.4 ms | pass |
| stagger 100 ms | 2,000 | 902 | 811 | 0.4056 | 0.995 | 58 / 104 / 127 ms | 0.6 ms | pass |
| stagger 100 ms | 5,000 | 943 | 737 | 0.1475 | 0.998 | 65 / 117 / 178 ms | 2.6 ms | pass |
| stagger 100 ms | 10,000 | 732 | 558 | 0.0558 | 0.997 | 77 / 235 / 281 ms | 7.7 ms | pass |

Both staggered cases failed at 15,000 like the distinct case (5,534 and 5,712
queries/post, hit rate 0.63). Before #670 a post cost about `N + 10` queries
and the highest passing step was 2,000 ([stream cost](stream-cost.md#with-shared-reads-227-2026-10-01)).

- **Queries per post no longer follow `N`:** about 16 with one member and 26
  to 46 with distinct members, up to 10,000 streams.
- **Overlapping checks share epoch reads:** about 4 to 7 per post at every
  step, so reads per post per stream fall as `N` grows.
- **Staggered checks read more, but not `N`:** a read in flight serves every
  check that arrives during it, so reads per post look bounded by the window
  over one read's time (about 800 at 100 ms, which suggests about 0.1 ms per
  read), not by `N`. At 1,000 streams and 100 ms that is 0.72 `N`; at
  10,000 it is 0.06 `N`. At 10 ms they stay at 6 to 56.
- **In real use** a post's checks arrive as the hub wakes the streams, spread
  over the fan-out: the unstaggered check p95 is 5 to 40 ms between 1,000 and
  20,000 streams, closer to the 10 ms window than to 100 ms, which spreads
  every check over the whole posting interval. Sharing should therefore help
  in practice; the 100 ms case is the pessimistic bound, where a small
  organisation pays up to about 0.7 `N` primary-key reads instead of `N`
  membership joins. Whether that meets the decision's revisit condition
  ("reads near `N`") is the maintainer's call, raised on #671.

**What hit the limit first.**
- **Distinct members: the authorization cache's capacity.**
  `org.DefaultAuthorizationCapacity` is 10,000 entries. Above it, entries are
  evicted before they are reused: the hit rate falls to 0.905 at 11,000 and
  0.647 at 15,000, each miss loads a membership, and the pool saturates
  (529,050 empty acquisitions in the 15,000 step, the first by +100 ms).
- **One member: not the pool.** At 40,000 no acquisition found the pool
  empty; the first delivery over 1 s came at +4.5 s. CPU and memory are not
  visible to the benchmark, so it does not show which of them it was.

## #216's active steps, rerun 2026-10-09

The same disposable container as #216 (10 CPUs, 7.7 GiB), main at `d40b4dd`,
a fresh database seeded the same way, pgx's default pool (10) and
`RIBBITTO_MAX_STREAMS=60000`; 10 posts/s for 30 s, from 7,000 streams until
a step did not pass. CPU is per interval from `/proc/<pid>/stat`, in CPUs.

| Streams | HTTP/1.1 p50 / p95 | HTTP/2 p50 / p95 | Step queries (HTTP/1.1) | Server CPU (HTTP/1.1) | Result |
|---|---|---|---|---|---|
| 7,000 | 33 / 50 ms | 43 / 67 ms | 42,415 | 2.1 | pass |
| 10,000 | 51 / 74 ms | 67 / 105 ms | 47,474 | 3.1 | pass |
| 15,000 | 138 / 198 ms | 1.15 / 2.03 s | 71,094 | 4.1 | HTTP/2 fail: p95 |
| 20,000 | 0.78 / 1.77 s | — | 92,702 | 4.1 | HTTP/1.1 fail: 11 posts missed |

- **Queries:** a step's `database.queries` difference, from before the page
  request to after the drain, so stream set-up is included (#216 measured
  about 4.06 queries per stream). Before #670 the 7,000 step made 2,133,026;
  this one made 42,415. Less #216's set-up figure, posting costs about 23 to
  47 queries per post; this rerun ran no idle step to measure set-up again.
- **No step lost a stream or a delivery;** none reset or was refused.

**What hit the limit first.**
- **HTTP/1.1: the server, not the pool.** The mean empty-acquire wait was
  2.5 ms. The server's CPU stopped at about 4.1 CPUs at both 15,000 and
  20,000 while the VM was about 65% busy (the client 2.4 CPUs, PostgreSQL
  under 1), and available memory fell to 0.9 GiB. The run does not isolate
  whether the server is bound by its own CPU work or serialised.
- **HTTP/2: the machine's CPU.** At 15,000 the VM was 86–88% busy, with
  Caddy at about 4 CPUs, the server 2.6 and the client 2.3.
