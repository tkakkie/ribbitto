# Stream authorization contention

What #703 changed in the cached-allow check, and what it measured. #695's
profiles (on #696) found the check `org.(*Authorizer).streamMembership`
to be the largest hotspot in both the benchmark and the server, ahead of the
path a shared reader would remove. The rule of the
[decision](stream-authorization.md#decision) does not change; the mechanics
are in [stream limits](stream-limits.md).

- **Fewer lock acquisitions on a hit:**
  - The check captures the cached allow when it registers its `latest`
    token, and proves it against the fresh epoch.
  - A hit therefore takes the authorizer's mutex twice instead of three
    times. It refreshes the LRU order in its cleanup, and only if the same
    entry is still resident.
- **One wake per joiner:** a check that joins an in-flight epoch load waits
  directly for its own result, instead of waking with the first load and
  waiting again for the private second load (#682).

## Results, 2026-10-09

Before is main at `3b50f2b`, and after is this change. Both ran on the same
machine and settings as #671's [benchmark](stream-authorization-results.md#benchmark-2026-10-09)
(`_CACHE=1`, pgx's default pool of 10, 10 posts/s for 10 s), and the server
in #216's disposable container.

**Ceiling, without profiling** (`TestStreamCost`, one member):

| Streams | Before p50 / p95 / p99 | After p50 / p95 / p99 |
|---|---|---|
| 20,000 | 103 / 294 / 892 ms | 67 / 86 / 95 ms |
| 30,000 | 136 / 184 / 203 ms | 97 / 140 / 159 ms |
| 40,000 | 712 / 1,041 / 1,142 ms, **fail** | 175 / 219 / 242 ms |
| 50,000 | — | 309 / 408 / 455 ms |
| 60,000 | — | 650 / 998 / 1,057 ms |

The highest passing step rose from 30,000 to at least 60,000, the ladder's
last step. With distinct members, 5,000 and 10,000 passed both before and
after, with p95 37 → 35 ms and 60 → 53 ms. This rerun stopped at 10,000, so
it did not find the distinct-member ceiling. #671 found that ceiling to be
the [cache's capacity](stream-limits.md).

**Profiles at the same settings.** For the benchmark, one member at 30,000 streams,
`-mutexprofilefraction=5`. For the server, #216's HTTP/1.1 active step at 15,000 streams, through
the [development metrics](dev-metrics.md) listener at fraction 5, with 30 s
captures. Seconds are absolute; shares are of each profile's total.

| Measure | Benchmark before | Benchmark after | Server before | Server after |
|---|---|---|---|---|
| Total CPU | 30.0 s | 22.6 s | 125.6 s | 110.5 s |
| Authorizer CPU | 16.4 s (55%) | 11.4 s (51%) | 29.7 s (24%) | 22.8 s (21%) |
| Authorizer blocking | 1,671 s (45%) | 911 s (41%) | 16,117 s (70%) | 7,222 s (40%) |
| …of which the epoch cache | 1,668 s | 161 s | — | — |
| Reader's path CPU | 1.8 s (6%) | 4.8 s (21%) | 17.6 s (14%) | 15.5 s (14%) |
| Reader's path blocking | 50 s (1%) | 103 s (5%) | 5,919 s (26%) | 7,739 s (43%) |
| Server p50 / p95 | — | — | 131 / 209 ms | 74 / 141 ms |

The reader's path is `CachedEvents.EventsAfter`, `Hub.Wait` and
`Hub.Latest`. Notes:
- **Profiled runs:** both profiled benchmark runs missed posts, one or two
  of 100, because profiling adds load. Their latencies are not ceilings.
- **The harness's collector:** about 40% of the benchmark's mutex total is
  the test harness's collector, which production does not have.
- **Single captures:** each server figure is one 30-second capture.

**What it points to.** The authorizer's blocking fell by about half, and its
CPU by a quarter to a third. As it shrank, the reader's path grew to 43% of
the server's blocking, mostly `Hub.Wait`, and to 21% of the benchmark's CPU.
The shared reader's build decision (held on #696) can be revisited on these
numbers; it is the maintainer's call.
