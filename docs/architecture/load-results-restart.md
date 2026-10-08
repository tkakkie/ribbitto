# Load test results: a reconnect storm

End-to-end results of #219: the server restarts with every stream open while
posts continue, and every client reconnects and replays from its
`Last-Event-ID`. The harness is in [restart](load-restart.md), the
reconnect model, receipts and comparison in [load client](load-client.md),
and the expected set in [seed data](../seed-data.md#expected-messages-after-a-load-run).

## Run of 2026-10-08

**Machine:** the disposable, loopback-only container of #216's run
([load results](load-results.md#run-of-2026-10-07)), with the same database
and fixtures. The binaries were linux/arm64 builds of `main` at 4cdb4aa;
pgx's default pool of 10 connections and `RIBBITTO_MAX_STREAMS=5000`.

**Scenario:** each run is one `loadgen` invocation that starts its own child
server, sets up every stream from the channel page's cursor, posts 10 posts/s
to the first fixture channel, and sends the child SIGTERM `-restart-after`
into observation, then relaunches it. Posts continue on their schedule
through the restart and until observation ends. The new child inherits the
environment, so `streams.open` and the counters start again with it.

- **Reconnect model:** the load client's fixed delay plus uniform jitter; no
  browser or htmx scheduling. *Default* is 250 ms plus 0–250 ms; *tight* is
  10 ms plus 0–10 ms, so that clients hit the server while it is down.
- **Runs** (60 s with a restart at 20 s, unless noted):

| Run | Streams | Transport | Model |
|---|---|---|---|
| sanity | 300 | HTTP/1.1 | default; 30 s, restart at 10 s |
| A, B | 3,000 | HTTP/1.1 | default |
| C | 3,000 | HTTP/2 through Caddy | default |
| D | 3,000 | HTTP/1.1 | tight |
| E | 3,000 | HTTP/2 through Caddy | tight |
| F | 5,000 (the default cap) | HTTP/1.1 | default; 30 s, restart at 10 s |

3,000 streams is well below #216's limit at 10 posts/s (7,000 passed, 10,000
failed). A and B are the same scenario run twice.

## Results

### Nothing lost, nothing repeated

In every run the comparison of receipts with the expected set gave:
`missing` 0, `unexpected` 0, `replayed_duplicates` 0, `duplicate_posts` 0,
`resets` 0, and `watermark_differs` 0. Every logical post committed exactly
once (each expected set matched `Sent`), every stream received every one
of them once, and no stream got a `reset`
(decision 24: none is expected on a plain restart).

Every run above 300 streams had one or two POSTs that failed around SIGTERM
and were retried with the same marker (`PostAttemptsMade` one or two above
`Sent`). Each retried post committed exactly once, in A, B, C and F after
the next posts, so with a later sequence; nothing was posted twice.

### Exit and readiness

| Run | SIGTERM to exit | SIGTERM to ready |
|---|---|---|
| sanity | 23 ms | 84 ms |
| A / B | 80 / 75 ms | 118 / 112 ms |
| C | 78 ms | 116 ms |
| D / E | 50 / 73 ms | 151 / 151 ms |
| F | 72 ms | 110 ms |

No exit overran the 10 s `-exit-deadline`. This verifies A7's fix (#297,
#316) under load: with thousands of streams open and posts in flight, the
process exits within 80 ms ([load testing](load-testing.md#assumptions-and-dispositions)).

### Client recovery

Times from SIGTERM over the streams, p50 / p95 / max: reconnected and outage
recovered in milliseconds, fully caught up in seconds. No stream was
incomplete for any of the three, and no stream was reset.

| Run | Reconnected (ms) | Outage recovered (ms) | Fully caught up (s) |
|---|---|---|---|
| sanity | 373 / 492 / 506 | 373 / 492 / 506 | 19.906 / 19.910 / 19.910 |
| A | 415 / 595 / 606 | 441 / 607 / 621 | 39.927 / 39.950 / 39.951 |
| B | 425 / 599 / 614 | 450 / 615 / 627 | 39.924 / 39.943 / 39.945 |
| C | 459 / 636 / 680 | 474 / 639 / 686 | 39.933 / 39.951 / 39.952 |
| D | 236 / 407 / 416 | 268 / 420 / 425 | 39.931 / 39.944 / 39.946 |
| E | 216 / 374 / 389 | 240 / 384 / 393 | 39.926 / 39.940 / 39.942 |
| F | 564 / 1,047 / 1,082 | 602 / 1,071 / 1,140 | 19.937 / 19.959 / 19.962 |

- **Reconnected** is also the reconnect-delay distribution. With the default
  model the server was ready (about 0.12 s) before any client's first retry
  (at least 0.25 s), so no attempt saw the outage: every reconnect attempt
  established. The time is the model's delay plus registering on the new
  process. At 5,000 streams the slowest reconnects took about 0.5 s longer
  than the model's 0.5 s maximum delay, waiting for the pool with the posts'
  membership checks (#622).
- **Tight model:** over HTTP/1.1 7,695 attempts were refused (the listener
  was closed); through Caddy 3,856 attempts got another 5xx from the proxy.
  No attempt got 503: no client hit the short window between ending the
  streams and closing the listener. Clients still reconnected within 0.42 s.
- **Outage recovered** (holding the new process's cursor at readiness)
  follows reconnecting within 60 ms.
- **Fully caught up is the last post's arrival, not a catch-up time.** Posts
  continue until observation ends, so the final watermark is the last
  scheduled post, one interval (1/rate, 0.1 s) before the end: 39.9 s after
  SIGTERM (19.9 s for sanity and F), plus its delivery. Outage recovered and
  the post latencies below are the measures of catching up (#668).
- **Post-to-receipt latency over the whole run** stayed in #216's range: p50
  28–30 ms and p95 44–79 ms at 3,000 streams, 43 / 80 ms at 5,000. Every
  drain completed in under 0.1 s.

### Replay load

| Run | Through the recovery cursor | All deliveries after reconnecting |
|---|---|---|
| sanity | 300 | 60,000 |
| A–E | 3,000 | 1,200,000 |
| F | 5,000 | 1,000,000 |

- `deliveries_through_recovery_cursor_after_reconnect` was exactly one per
  stream: the post issued at SIGTERM committed before the new process was
  ready, after the old one had ended its streams, so every stream received
  it only by replay. The other counts are the posts after reconnecting (400,
  or 200 in sanity and F) times the streams, with no repeated arrival.
- **Limitation:** the cursor count is bounded: it leaves out backlog
  committed after readiness and before a delayed stream reconnects. Here
  that window was up to about 0.6 s (1 s in F), so up to six (ten) more
  posts reached the latest streams by replay but appear only in the total.
- **Database work per process** (separate process lifetimes, no subtraction
  across the restart). At 3,000 streams, A–E alike:
  - old process, read just before SIGTERM: 615,270–615,506 queries for
    setup and 200 posts, about 3,015 per post as in #216; mean empty-acquire
    wait 18–24 ms;
  - new process, after drain: 1,218,266–1,218,416 queries for 400 posts and
    the reconnects. Less 400 × 3,015 for the posts, that leaves about 4.1
    queries per reconnected stream, the same as initial setup (#216's 4.06):
    the storm cost no more than opening the streams did. Mean empty-acquire
    wait 18–20 ms.
  - F: 521,811 and 1,023,522 queries, waits 28.5 and 29.5 ms.
  - Both processes held 6,012–6,013 goroutines at 3,000 streams (10,012 at
    5,000): two per stream.

### CPU and memory

Sampled about every second from `/proc` (the server as the load client's
child, PostgreSQL as PID 1 and its children). CPU in CPUs, steady state
outside the storm; the VM's busy share is of 10 CPUs.

| Run | Server | PostgreSQL | Generator | Caddy | VM busy | VM busy, peak near SIGTERM |
|---|---|---|---|---|---|---|
| A / B / D | 1.5 | 0.8 | 0.6 | — | 30% | 37–45% |
| C / E | 1.3 | 0.7 | 0.5 | 0.9 | 35% | 47–50% |
| F | 2.3 | 1.2 | 1.0 | — | 46% | 53% |

- **Sampling gap at the restart:** the old process's CPU from its last
  sample (0.02–0.74 s before SIGTERM) until it exited was not observed, so
  these figures give no server maximum. Counted from the new process's start
  (taken as SIGTERM plus the exit time, so a lower bound) to its first
  sample, 0.23–0.93 s later, the new server averaged 0.4–1.6 CPUs with the
  default model at 300 and 3,000 streams, 2.5–2.65 with the tight model, and
  2.5 at 5,000. Its later one-second intervals reached 2.1 CPUs at 3,000
  streams and 2.8 at 5,000.
- The VM's busy share stayed at or below 54% in every sample of every run.
  That aggregate does not show that no single resource saturated briefly;
  the pool's waits are in the database figures above.
- Server RSS: 224–231 MiB for both processes at 3,000 streams, 356 at 5,000;
  at most 3,019–3,020 and 5,019 open file descriptors.
- The generator was the largest process: 3.7–4.1 GiB RSS at 3,000 streams over
  60 s, leaving 1.3–1.7 GiB available in the VM; 3,437 MiB at 5,000 streams
  over 30 s (F). Its receipt state grows with
  streams × deliveries; after #667, about 0.7 KB per delivery
  (1,221 MiB generator peak at 3,000 streams).

## What this means

- **Nothing lost or wrongly repeated** at 300, 3,000 and 5,000 streams, over
  HTTP/1.1 and HTTP/2, with both reconnect models: every client caught up
  from its `Last-Event-ID` with no gap, duplicate or `reset`, and retried
  posts never committed twice.
- **A7:** SIGTERM to exit took 23–80 ms; nothing held the process.
- **Recovery** is bounded by the client's own delay: 0.6–0.7 s at 3,000
  streams with the default model, 0.4 s with the tight one, and 1.1 s at the
  5,000 cap, where the database pool adds waiting.
- Out of scope, as on #219: browser and htmx reconnect scheduling,
  presence, and runs across machines.

## Follow-ups

- #668: the fully-caught-up time measures the last post, not catching
  up, while posts continue to the end.

## Reproducing it

In the container of [load results](load-results.md#reproducing-it), with
`RIBBITTO_DATABASE_URL` pointing at its `ribbitto` database on `127.0.0.1`
and no other server on `127.0.0.1:8080` (the harness refuses an occupied
address), run, for A (outputs go outside any Git checkout: `seed -expected`
refuses an output path inside one):

```sh
export RIBBITTO_DEV_METRICS_ADDR=127.0.0.1:9090 RIBBITTO_MAX_STREAMS=5000
loadgen -tokens /root/loadtest.json -metrics http://127.0.0.1:9090 \
  -server ribbitto -server-arg serve -restart-after 20s \
  -streams 3000 -rate 10 -duration 60s -setup 5m -drain 60s \
  -dial-concurrency 256 -receipts /tmp/storm.receipts.json \
  -source 127.0.0.2,127.0.0.3,127.0.0.4,127.0.0.5,127.0.0.6,127.0.0.7,127.0.0.8,127.0.0.9 \
  > /tmp/storm.json
seed -expected -tokens /root/loadtest.json -after "$(jq .Cursor /tmp/storm.json)" \
  -output /tmp/storm.expected.json
loadgen -compare /tmp/storm.receipts.json /tmp/storm.expected.json
```

- **HTTP/2:** replace `-source …` with `-target https://localhost:8443 -ca
  /root/caddy-data/caddy/pki/authorities/local/root.crt`; Caddy keeps
  proxying to the child's default address.
- **Tight model:** add `-reconnect-delay 10ms -reconnect-jitter 10ms`.
- `-restart-after` turns on `-reconnect`. The JSON's `Verdict` is `fail` in
  every restart run, because each ended connection counts in `Failed`; read
  `Restart` and the comparison instead.
