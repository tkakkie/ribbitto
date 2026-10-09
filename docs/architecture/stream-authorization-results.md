# Stream authorization results

What #670's cached allows and shared epoch reads
([decision](stream-authorization.md#decision)) cost, measured by #671: the
stream-cost benchmark ([how it runs](stream-cost.md)) and a rerun of #216's
active steps ([how they run](load-results.md#reproducing-it)).

## Benchmark, 2026-10-09

Apple M-series, darwin/arm64, 10 CPUs, Go 1.27.2; PostgreSQL 18 in Docker on
the same machine; pgx's default pool (10); batch 100; 10 posts/s for 10 s;
`_CACHE=1`. Each case ran the steps `1000,2000,3000,4000,5000,7000,10000,15000,20000`
until one failed; the one-member case went on to 30,000 and 40,000, and the
distinct case was rerun at 9,000 and 11,000. Every step completed its 100
posts. Latency runs from `Post` returning to the send and includes the stagger
wait; *check* is the time spent in `MayReceive` alone. Durations are in ms
unless marked.

| Case | Streams | Queries/post | Epoch reads/post | …/post/stream | Hit rate | p50 / p95 / p99 | Check p50 / p95 / p99 | Result |
|---|---|---|---|---|---|---|---|---|
| one member | 1,000 | 27.1 | 5.13 | 0.0051 | 0.990 | 12 / 15 / 19 ms | 2.8 / 4.6 / 6.5 ms | pass |
| one member | 2,000 | 15.9 | 3.88 | 0.0019 | 1.000 | 20 / 24 / 26 ms | 5.2 / 7.6 / 8.8 ms | pass |
| one member | 3,000 | 16.3 | 4.27 | 0.0014 | 1.000 | 26 / 32 / 34 ms | 7.5 / 12 / 14 ms | pass |
| one member | 4,000 | 16.1 | 4.07 | 0.0010 | 1.000 | 27 / 32 / 33 ms | 7.6 / 10 / 12 ms | pass |
| one member | 5,000 | 16.2 | 4.16 | 0.0008 | 1.000 | 30 / 35 / 37 ms | 8.7 / 12 / 14 ms | pass |
| one member | 7,000 | 16.1 | 4.10 | 0.0006 | 1.000 | 38 / 45 / 49 ms | 12 / 16 / 20 ms | pass |
| one member | 10,000 | 16.2 | 4.24 | 0.0004 | 1.000 | 47 / 54 / 58 ms | 15 / 19 / 22 ms | pass |
| one member | 15,000 | 17.5 | 5.47 | 0.0004 | 1.000 | 59 / 72 / 78 ms | 19 / 27 / 34 ms | pass |
| one member | 20,000 | 17.1 | 4.73 | 0.0002 | 1.000 | 98 / 135 / 147 ms | 34 / 40 / 52 ms | pass |
| one member | 30,000 | 19.1 | 5.89 | 0.0002 | 1.000 | 220 / 282 / 301 ms | 59 / 73 / 85 ms | pass |
| one member | 40,000 | 19.3 | 7.47 | 0.0002 | 1.000 | 1.15 / 1.85 / 1.97 s | 82 / 102 / 113 ms | fail: p95 |
| distinct | 1,000 | 26.6 | 4.60 | 0.0046 | 0.990 | 12 / 15 / 17 ms | 3.0 / 4.9 / 6.9 ms | pass |
| distinct | 2,000 | 25.9 | 3.92 | 0.0020 | 0.995 | 20 / 24 / 27 ms | 6.0 / 8.5 / 10 ms | pass |
| distinct | 3,000 | 26.3 | 4.28 | 0.0014 | 0.997 | 26 / 32 / 35 ms | 8.2 / 12 / 14 ms | pass |
| distinct | 4,000 | 26.0 | 4.04 | 0.0010 | 0.998 | 28 / 33 / 35 ms | 8.7 / 12 / 15 ms | pass |
| distinct | 5,000 | 26.0 | 4.03 | 0.0008 | 0.998 | 32 / 38 / 41 ms | 11 / 14 / 17 ms | pass |
| distinct | 7,000 | 36.1 | 4.08 | 0.0006 | 0.997 | 39 / 45 / 48 ms | 13 / 18 / 22 ms | pass |
| distinct | 10,000 | 46.2 | 4.18 | 0.0004 | 0.997 | 48 / 60 / 64 ms | 18 / 24 / 30 ms | pass |
| distinct | 15,000 | 5,307 | 5.57 | 0.0004 | 0.647 | 7.43 / 13.88 / 14.36 s | 217 / 407 / 445 ms | fail: p95 |
| distinct, rerun | 9,000 | 106 | 4.20 | 0.0005 | 0.990 | 47 / 66 / 262 ms | 17 / 23 / 33 ms | pass |
| distinct, rerun | 11,000 | 1,060 | 5.08 | 0.0005 | 0.905 | 57 / 91 / 110 ms | 20 / 48 / 63 ms | pass |
| stagger 10 ms | 1,000 | 78.0 | 56.02 | 0.0560 | 0.990 | 14 / 20 / 24 ms | 0.59 / 1.1 / 7.1 ms | pass |
| stagger 10 ms | 2,000 | 75.0 | 52.97 | 0.0265 | 0.995 | 21 / 28 / 32 ms | 0.74 / 2.9 / 7.8 ms | pass |
| stagger 10 ms | 3,000 | 49.0 | 27.05 | 0.0090 | 0.997 | 25 / 31 / 33 ms | 3.1 / 7.0 / 8.9 ms | pass |
| stagger 10 ms | 4,000 | 42.8 | 20.77 | 0.0052 | 0.998 | 28 / 34 / 38 ms | 4.0 / 7.5 / 10 ms | pass |
| stagger 10 ms | 5,000 | 33.3 | 11.30 | 0.0023 | 0.998 | 34 / 40 / 43 ms | 8.6 / 13 / 16 ms | pass |
| stagger 10 ms | 7,000 | 42.0 | 9.98 | 0.0014 | 0.997 | 40 / 47 / 49 ms | 9.3 / 14 / 18 ms | pass |
| stagger 10 ms | 10,000 | 48.4 | 6.39 | 0.0006 | 0.997 | 55 / 66 / 70 ms | 19 / 24 / 30 ms | pass |
| stagger 10 ms | 15,000 | 5,534 | 7.84 | 0.0005 | 0.632 | 8.07 / 15.11 / 15.60 s | 226 / 418 / 455 ms | fail: p95 |
| stagger 100 ms | 1,000 | 786 | 724 | 0.7242 | 0.990 | 55 / 100 / 113 ms | 0.21 / 0.41 / 0.84 ms | pass |
| stagger 100 ms | 2,000 | 902 | 811 | 0.4056 | 0.995 | 58 / 104 / 127 ms | 0.26 / 0.62 / 1.8 ms | pass |
| stagger 100 ms | 3,000 | 949 | 816 | 0.2720 | 0.997 | 60 / 109 / 143 ms | 0.29 / 1.1 / 2.6 ms | pass |
| stagger 100 ms | 4,000 | 966 | 790 | 0.1974 | 0.998 | 62 / 113 / 159 ms | 0.32 / 1.8 / 3.8 ms | pass |
| stagger 100 ms | 5,000 | 943 | 737 | 0.1475 | 0.998 | 65 / 117 / 178 ms | 0.35 / 2.6 / 4.8 ms | pass |
| stagger 100 ms | 7,000 | 858 | 732 | 0.1046 | 0.997 | 70 / 221 / 229 ms | 0.35 / 4.3 / 6.9 ms | pass |
| stagger 100 ms | 10,000 | 732 | 558 | 0.0558 | 0.997 | 77 / 235 / 281 ms | 0.47 / 7.7 / 26 ms | pass |
| stagger 100 ms | 15,000 | 5,712 | 162 | 0.0108 | 0.633 | 8.29 / 15.82 / 16.32 s | 182 / 355 / 404 ms | fail: p95 |

Before #670 a post cost about `N + 10` queries and the highest passing step
was 2,000 ([stream cost](stream-cost.md#with-shared-reads-227-2026-10-01)).

- **Queries per post no longer follow `N`:** in the unstaggered ladders,
  about 16 to 19 with one member and 26 to 46 with distinct members up to
  10,000 streams. The distinct rerun's first step, 9,000, made 106.
- **Overlapping checks share epoch reads:** about 4 to 7 per post at every
  step, so reads per post per stream fall as `N` grows.
- **Staggered checks read more, but in these runs not `N`:**
  - 10 ms window: 56 reads per post at 1,000 streams (0.056 `N`), 6.4 at
    10,000.
  - 100 ms window: 724 at 1,000 (0.72 `N`), 811 and 816 at 2,000 and 3,000,
    then fewer, down to 558 at 10,000 (0.056 `N`).
  - A check that arrives while an epoch read is in flight waits for it and
    then shares the next fresh read with the others that arrived meanwhile,
    so the count depends on how the checks are spread in time, not on `N`
    itself. Neither one read's duration nor the spread of the checks' start
    times was measured, so these runs do not establish a fixed bound.
- **Real use is not measured here.** Without a stagger, a post's checks
  start as the hub wakes the streams; the runs do not record how far apart
  those starts are (the check-time percentiles measure how long each check
  took, not when it started). The 10 ms and 100 ms windows bracket two
  spreads; which one real traffic resembles is open. In the 100 ms case a
  small organisation paid up to 0.72 `N` primary-key reads instead of `N`
  membership joins. Whether that meets the decision's revisit condition
  ("reads near `N`") is the maintainer's call, raised on #671.

**What hit the limit first.**
- **Distinct members: the authorization cache's capacity.**
  `org.DefaultAuthorizationCapacity` is 10,000 entries. 11,000 distinct
  members still passed (p95 91 ms), but the hit rate fell to 0.905 and
  queries per post rose to 1,060. At 15,000 the hit rate was 0.647, each
  miss loaded a membership, and the pool saturated (529,050 empty
  acquisitions, the first by +100 ms). Both staggered cases failed at 15,000
  the same way.
- **One member: not the pool.** At 40,000 no acquisition found the pool
  empty; the first delivery over 1 s came at +4.5 s. The benchmark does not
  see CPU or memory, so it does not show what the limit was.

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

**What hit the limit first.** Not the pool; the runs do not isolate what
did.
- **HTTP/1.1:** the mean empty-acquire wait was 2.5 ms. The server's CPU
  stayed at about 4.1 CPUs at both 15,000 and 20,000 while the VM was about
  65% busy (the client 2.4 CPUs, PostgreSQL under 1), and available memory
  fell to 0.9 GiB. That points at the server, but not at whether its own CPU
  work, serialisation inside it, or memory came first.
- **HTTP/2:** at 15,000 the VM was 86–88% busy, with Caddy at about 4 CPUs,
  the server 2.6 and the client 2.3: CPU pressure on the shared machine,
  without showing which process limited first.
