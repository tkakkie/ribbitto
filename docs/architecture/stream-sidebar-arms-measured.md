# Sidebar recount arms: controlled results (#832)

Measured 2026-10-12 by Claude with the [arms protocol](stream-sidebar-arms-results.md)
on darwin/arm64 (10 CPUs, Go 1.27.2) and PostgreSQL 18.6 in Docker
(`max_connections` 100). Each window was gated on a one-minute load average at
most half the CPUs, with no other heavy work running. Every window completed
every scheduled post with zero loop errors.

- **Ladder** (at e7879c5): four groups, 39 windows of 60 s each.
- **Writer fix**: the [writer](stream-sidebar-writer-results.md) sent a completion
  only when it matched the latest version, which starved recounts at 5,000
  streams. After the fix (a68d61c), `sync40`, `async40` and `async10+30` were
  re-run at 5,000 streams, three rounds each, at d0252bc.
- **Five-minute windows**: three per rate, for `async40` at 5,000 streams.

Values are medians across windows. Targets are `costTargets`: a complete
workload, delivery p95 ≤ 250 ms, recount delay p95 ≤ 2 s and p99 ≤ 5 s.
Eligible adds the queue-growth guard.

### 5 posts/s, 1,000 streams

| arm | windows | posting p99 ms | delivery p95 ms | recount p95 / p99 s | targets | eligible |
|---|---|---|---|---|---|---|
| 10 | 12 | 301 | 356 | 1.34 / 1.37 | 12/12 | 12/12 |
| 20 | 3 | 187 | 240 | 1.21 / 1.22 | 3/3 | 3/3 |
| 30 | 3 | 179 | 189 | 1.20 / 1.22 | 3/3 | 3/3 |
| 40 | 3 | 135 | 128 | 1.13 / 1.17 | 3/3 | 3/3 |
| gate10 | 3 | 10 | 21 | 0.97 / 1.01 | 3/3 | 3/3 |
| sync40 | 6 | 13 | 22 | 0.97 / 1.01 | 6/6 | 6/6 |
| async40 (pre-fix) | 6 | 12 | 20 | 1.23 / 2.02 | 6/6 | 6/6 |
| async10+30 (pre-fix) | 3 | 11 | 18 | 1.23 / 2.02 | 3/3 | 3/3 |

### 5 posts/s, 5,000 streams

| arm | windows | posting p99 ms | delivery p95 ms | recount p95 / p99 s | targets | eligible |
|---|---|---|---|---|---|---|
| 10 | 12 | 1105 | 2913 | 2.32 / 2.58 | 0/12 | 0/12 |
| 20 | 3 | 676 | 1256 | 1.82 / 1.99 | 0/3 | 0/3 |
| 30 | 3 | 488 | 851 | 1.51 / 1.68 | 0/3 | 0/3 |
| 40 | 3 | 447 | 632 | 1.52 / 1.68 | 0/3 | 0/3 |
| gate10 | 3 | 9 | 1624 | 2.62 / 2.74 | 0/3 | 0/3 |
| sync40 (re-run) | 6 | 17 | 288 | 1.29 / 1.36 | 0/6 | 0/6 |
| async40 | 6 | 17 | 38 | 1.22 / 1.29 | 6/6 | 3/6 |
| async10+30 | 3 | 14 | 31 | 1.33 / 1.39 | 3/3 | 0/3 |

### 20 posts/s, 1,000 streams

| arm | windows | posting p99 ms | delivery p95 ms | recount p95 / p99 s | targets | eligible |
|---|---|---|---|---|---|---|
| 10 | 12 | 242 | 277 | 1.25 / 1.39 | 12/12 | 12/12 |
| 20 | 3 | 169 | 160 | 1.16 / 1.22 | 3/3 | 3/3 |
| 30 | 3 | 69 | 63 | 1.02 / 1.11 | 3/3 | 3/3 |
| 40 | 3 | 103 | 91 | 1.09 / 1.17 | 3/3 | 3/3 |
| gate10 | 3 | 7 | 16 | 0.97 / 1.01 | 3/3 | 3/3 |
| sync40 | 6 | 11 | 19 | 0.97 / 1.01 | 6/6 | 6/6 |
| async40 (pre-fix) | 6 | 11 | 16 | 1.81 / 2.01 | 6/6 | 6/6 |
| async10+30 (pre-fix) | 3 | 7 | 15 | 1.79 / 1.99 | 3/3 | 3/3 |

### 20 posts/s, 5,000 streams

| arm | windows | posting p99 ms | delivery p95 ms | recount p95 / p99 s | targets | eligible |
|---|---|---|---|---|---|---|
| 10 | 12 | 1255 | 3217 | 2.92 / 2.99 | 0/12 | 0/12 |
| 20 | 3 | 735 | 2500 | 2.11 / 2.33 | 0/3 | 0/3 |
| 30 | 3 | 826 | 1906 | 1.81 / 2.00 | 0/3 | 0/3 |
| 40 | 3 | 644 | 1109 | 1.68 / 1.78 | 0/3 | 0/3 |
| gate10 | 3 | 9 | 2181 | 3.16 / 3.31 | 0/3 | 0/3 |
| sync40 (re-run) | 6 | 17 | 576 | 1.57 / 1.67 | 0/6 | 0/6 |
| async40 | 6 | 16 | 36 | 1.47 / 1.56 | 6/6 | 4/6 |
| async10+30 | 3 | 11 | 29 | 1.59 / 1.69 | 3/3 | 3/3 |

The async 1,000-stream rows predate the writer fix and were not re-run; they
met the targets even so.

## The writer starvation the ladder found

Before the fix, async writers at 5,000 streams sent 0.02–0.03 recounts per
stream per second, while workers computed about 230,000–250,000 recounts per
window. Recount delay p95 was about 57 s, because frames went out only after
posting stopped. A completion waits 0.3–0.5 s in the queue at 5,000 streams,
and every post bumped the desired version in the meantime. After the fix,
writers send 0.66–0.80 recounts per stream per second.

## Queue stability over five minutes

With `async40` at 5,000 streams, the queue's per-minute means are flat after the
first minute:

- **5 posts/s:** length 904–971, oldest wait 223–244 ms.
- **20 posts/s:** length 1,528–1,663, oldest wait 450–514 ms.

All six windows met the targets. Delivery p95 was 35–36 ms, and recount delay
p95/p99 was 1.22/1.29 s at 5 posts/s and 1.48/1.58 s at 20.

The growth guard nevertheless rejected two of the six windows, and some
60-second windows. It requires only that three consecutive 10-second means rise
over the last 30 s of posting, so noise of 1–3 % trips it on a plateau. Before
the guard gates a production decision, it needs a tolerance, or a longer horizon.

## Reading

- **At 1,000 streams, every configuration meets the targets.** A bounded
  recount queue, or #826's 10-slot gate, cuts posting p99 from 135–300 ms
  (plain pools) to 10–13 ms.
- **At 5,000 streams, plain pools fail.** They miss posts, or exceed delivery
  p95 and recount p95. `gate10` and `sync40` keep posting fast, but delay
  deliveries: a writer held by its own recount holds its messages.
- **Writer scheduling is the variable that matters.** With the same 40-worker
  queue and the same shared pool of 40, the async writer meets every target at
  5,000 streams, and `sync40` does not.
- **Partitioning the pool (normal 10 + recount 30) shows no clear gain** over a
  shared pool of 40 in these windows.

## Recommendation

Implement the single-writer design in production: one writer per stream, with
recounts on a bounded, fair, cancellable queue on the shared pool. Keep the
prototype's rules:

- send a completion only when it is newer than the last frame;
- keep the stream dirty while later triggers arrive;
- authorise the bound member immediately before every send.

The [production gaps](stream-sidebar-writer-results.md#production-gaps) remain
the implementation's work. The queue's plateau should be re-checked there under
the real wait and the real wiring.

These results come from one host, with one application process and synthetic
posting. They are not a capacity ceiling.
