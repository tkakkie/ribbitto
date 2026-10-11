# Sidebar bottleneck results (#826)

Measured 2026-10-11 on MacBook Pro Mac17,2, Apple M5 (10 cores), 32 GB,
darwin/arm64, Darwin 27.0.0; Go 1.27.2; PostgreSQL 18.6
(Debian 18.6-1.pgdg13+2), loopback port 55435. The machine also hosted #789,
which used CPU at times; host load was uncontrolled; CPU and memory were not sampled.

## Reproduce

Export `RIBBITTO_TEST_DATABASE_URL` as `<local test database>` first
([database tests](../database-tests.md)). Put the installed Go 1.27.2 `bin` first
on `PATH`; use `GOTOOLCHAIN=local`. Run sequentially; each invocation creates
a fresh migrated disposable database:

```sh
export GOCACHE=$PWD/bin/.cache/go-build GOMODCACHE=$PWD/bin/.cache/mod
export GOLANGCI_LINT_CACHE=$PWD/bin/.cache/lint GOPATH=$PWD/bin/.cache/gopath
export GOTOOLCHAIN=local RIBBITTO_REQUIRE_DB=1 RIBBITTO_STREAM_COST=1
export RIBBITTO_STREAM_COST_MEMBERS=distinct RIBBITTO_STREAM_COST_CACHE=1
export RIBBITTO_STREAM_COST_STEPS=1,10,100,1000,5000
export RIBBITTO_STREAM_COST_DURATION=4s RIBBITTO_STREAM_COST_ATTRIBUTION=1
unset RIBBITTO_STREAM_COST_STAGGER
run_cost() {
  RIBBITTO_STREAM_COST_SIDEBAR=$1 RIBBITTO_STREAM_COST_RATE=$2 \
  RIBBITTO_STREAM_COST_POOL=$3 RIBBITTO_STREAM_COST_RECOUNTS=$4 \
    go test -count=1 -run '^TestStreamCost$' -v -timeout 10m ./internal/realtime/
}
run_cost 0 20 10 ''
for rate in 5 20; do
  run_cost 1 "$rate" 10 '' # A
  if [ "$rate" = 5 ]; then run_cost 1 "$rate" 30 ''
  else run_cost 1 "$rate" 10 10; fi
  run_cost 1 "$rate" 10 '' # B
  if [ "$rate" = 5 ]; then run_cost 1 "$rate" 10 10
  else run_cost 1 "$rate" 30 ''; fi
  run_cost 1 "$rate" 10 '' # C
done
run_cost 1 5 10 '' # D
run_cost 1 5 30 '' # R
run_cost 1 5 10 '' # E
```

All runs: batch 100, shared event/message caches, authorization capacity 10,000,
no stagger, one organisation/channel/default topic, distinct members, unfragmented
ranges and alternating initial totals 0/1. Fixtures, sidebar seeds, read writes
and verified initial recounts are excluded. The open-loop schedule allows one
second's posts in flight; drain requires each message and both final count totals
per member. Pool30 changes only pool 10 to 30; gate10 changes only unbounded
recount concurrency to 10, with pool 10. Its slot covers the test source call,
ending before authorization. Production interval and wiring are unchanged.
Message-only mode has no sidebar seed; candidate comparisons use sidebar fixtures.

`_ATTRIBUTION=1` enables timings; `_RECOUNTS=<positive n>` bounds test recounts.
Ordinary checks omit the workload. Reuse this command for later optimizations.
Inspect the logged verdicts: `go test` succeeds even on underload or p95 over 1 s.

## Attribution

pgxpool Acquire elapsed is classified by caller context: sidebar snapshots,
actual event reads, authorization loaders and other (posting/message reads).
Shared loaders retain values through WithoutCancel. It includes immediate calls,
connection creation, pgx checks and failures: a queue-wait approximation, not the
independent empty-only pool counter. Sums span concurrent operations, not wall time.
Statement elapsed is pgx Query start to end/rows.Close, including transport and
decoding, not server-only execution. Whole-sidebar covers acquisition, BEGIN,
lists, counts and COMMIT; it excludes slot wait, JSON and authorization.
Sampling ends before cancellation at the completed-message-and-snapshot boundary.
In-flight operations can contribute statements/acquires without a whole snapshot;
the sums are not exact partitions of identical samples. Logs include counts,
errors, sums and p50/p95/p99 per kind and step. Selected topic is nil; no GetTopic
statement runs. Tables supersede the preliminary count-only pass.

Reference C baselines completed all scheduled posts. Acquire = sidebar/event/auth/other summed seconds; whole = n/sum seconds/p95 ms. Times round to three decimals; recorded errors were zero.

| Posts/s | Streams | Acquire sums s | Whole n/s/ms | Empty wait s |
|---|---|---|---|---|
| 5 | 1 | 0.000/0.001/0.000/0.007 | 4/0.017/6.379 | 0.007 |
| 5 | 10 | 0.010/0.001/0.000/0.001 | 40/0.235/7.351 | 0.000 |
| 5 | 100 | 11.549/0.000/0.188/0.053 | 400/13.705/54.502 | 11.759 |
| 5 | 1000 | 515.349/0.615/1.671/1.085 | 4000/531.943/280.808 | 518.718 |
| 5 | 5000 | 4108.037/6.767/8.171/4.738 | 12884/4158.757/714.974 | 4127.708 |
| 20 | 1 | 0.000/0.001/0.001/0.007 | 4/0.020/6.202 | 0.006 |
| 20 | 10 | 0.013/0.000/0.001/0.001 | 40/0.257/7.538 | 0.000 |
| 20 | 100 | 9.364/0.129/0.171/0.104 | 400/11.372/53.123 | 9.748 |
| 20 | 1000 | 362.011/1.521/2.032/2.007 | 4000/378.252/189.631 | 367.567 |
| 20 | 5000 | 4320.769/3.623/6.765/19.937 | 13177/4369.954/839.378 | 4351.088 |

Statements: sum seconds / p95 ms. R/C/S/T = FirstChannelReadRanges / CountChannelUnread / ReadTopicState / CountTopicUnread. n is R samples; other statements can differ at the boundary (logs give each n).

| Posts/s | Streams | n | R s/ms | C s/ms | S s/ms | T s/ms |
|---|---|---|---|---|---|---|
| 5 | 1 | 4 | 0.003/1.495 | 0.003/1.000 | 0.002/0.546 | 0.003/0.863 |
| 5 | 10 | 40 | 0.021/0.865 | 0.032/1.382 | 0.024/1.262 | 0.031/1.391 |
| 5 | 100 | 400 | 0.231/0.907 | 0.292/1.253 | 0.222/0.970 | 0.313/1.468 |
| 5 | 1000 | 4000 | 1.853/0.711 | 2.264/0.897 | 1.728/0.683 | 2.389/0.941 |
| 5 | 5000 | 12886 | 5.620/0.593 | 6.986/0.748 | 5.249/0.563 | 7.365/0.788 |
| 20 | 1 | 4 | 0.002/0.562 | 0.003/0.948 | 0.003/1.184 | 0.003/0.739 |
| 20 | 10 | 40 | 0.027/1.052 | 0.041/1.448 | 0.028/0.984 | 0.037/1.379 |
| 20 | 100 | 400 | 0.225/1.001 | 0.277/1.172 | 0.205/0.874 | 0.289/1.202 |
| 20 | 1000 | 4000 | 1.775/0.629 | 2.295/0.842 | 1.683/0.611 | 2.350/0.858 |
| 20 | 5000 | 13183 | 5.662/0.582 | 7.411/0.769 | 5.302/0.553 | 7.591/0.797 |

G/L/T = GetChannel/ListChannels/ListTopics; B/C = BEGIN/COMMIT, same units. ROLLBACK had zero samples.

| Posts/s | Streams | G s/ms | L s/ms | T s/ms | B s/ms | C s/ms |
|---|---|---|---|---|---|---|
| 5 | 1 | 0.002/0.659 | 0.002/0.510 | 0.001/0.472 | 0.001/0.483 | 0.001/0.389 |
| 5 | 10 | 0.027/1.323 | 0.026/1.224 | 0.023/1.124 | 0.022/1.003 | 0.018/0.926 |
| 5 | 100 | 0.211/0.880 | 0.212/0.813 | 0.215/0.758 | 0.212/0.829 | 0.238/0.823 |
| 5 | 1000 | 1.648/0.638 | 1.677/0.658 | 1.689/0.655 | 1.658/0.663 | 1.621/0.627 |
| 5 | 5000 | 5.074/0.540 | 5.089/0.542 | 5.151/0.548 | 5.090/0.532 | 5.009/0.541 |
| 20 | 1 | 0.002/0.753 | 0.002/0.552 | 0.002/0.522 | 0.002/0.498 | 0.001/0.422 |
| 20 | 10 | 0.023/0.841 | 0.022/0.750 | 0.024/1.017 | 0.020/0.777 | 0.020/0.683 |
| 20 | 100 | 0.204/0.760 | 0.204/0.822 | 0.198/0.878 | 0.205/0.828 | 0.193/0.806 |
| 20 | 1000 | 1.629/0.597 | 1.632/0.598 | 1.623/0.585 | 1.620/0.615 | 1.576/0.573 |
| 20 | 5000 | 5.147/0.529 | 5.141/0.536 | 5.146/0.533 | 5.061/0.522 | 5.018/0.530 |

## Candidate comparisons

Message / recount-delay p95 in ms: reference baseline / pool30 (R at 5 posts/s) / gate10. All comparison steps completed 20 or 80 scheduled posts (4 s posting window); every message and final snapshot arrived, without a drain timeout. Underloaded attempts are listed below.

| Posts/s | Streams | Message p95 B/P/G ms | Recount p95 B/P/G ms |
|---|---|---|---|
| 5 | 1 | 7.022/8.188/7.971 | 1006.643/1009.795/1005.992 |
| 5 | 10 | 7.437/7.777/6.521 | 1007.013/1006.319/1003.218 |
| 5 | 100 | 6.015/32.254/9.312 | 1057.709/1044.441/1003.995 |
| 5 | 1000 | 293.969/128.762/17.566 | 1314.923/1127.126/974.510 |
| 5 | 5000 | 910.397/850.890/1398.143 | 2464.094/2115.430/2404.041 |
| 20 | 1 | 4.677/5.227/4.364 | 962.166/968.338/961.424 |
| 20 | 10 | 5.736/5.971/5.829 | 962.906/965.226/962.379 |
| 20 | 100 | 5.453/5.661/5.773 | 1041.432/1025.814/968.755 |
| 20 | 1000 | 76.566/125.059/12.234 | 1258.383/1126.816/969.489 |
| 20 | 5000 | 2511.908/841.051/1786.816 | 2576.343/1821.586/2757.619 |

Absolute work at 5,000 (SQL = attempted query/transaction statements; count SQL is a subset).

| Posts/s | Mode | Completed/missed | SQL q/tx | Count SQL/recounts | Wall s | Empty wait s | Slot p95 ms |
|---|---|---|---|---|---|---|---|
| 5 | baseline-c | 20/0 | 90683/25993 | 51544/12864 | 6.099 | 4127.708 | 0.000 |
| 5 | pool30-repeat | 20/0 | 112329/32192 | 63848/15956 | 5.987 | 4366.739 | 0.000 |
| 5 | gate10 | 20/0 | 105691/36711 | 50009/12496 | 6.207 | 27.648 | 1554.075 |
| 20 | baseline-c | 80/0 | 93595/26859 | 52728/12525 | 6.821 | 4351.088 | 0.000 |
| 20 | pool30 | 80/0 | 123164/35378 | 69504/17376 | 6.090 | 4230.813 | 0.000 |
| 20 | gate10 | 80/0 | 100971/37228 | 46263/11558 | 6.453 | 36.831 | 2009.249 |

Repeated full ladders at 5,000, including the first pool30 underload and its repeat. R ran between D/E; its full ladder was rerun unchanged. Variation cannot be assigned to #789; latencies cover completed posts.

| Posts/s | Run | Scheduled/completed/missed | Message p95 ms | Empty wait s |
|---|---|---|---|---|
| 5 | A | 20/17/3 | 1684.552 | 6128.066 |
| 5 | POOL30 | 20/19/1 | 322.425 | 6153.526 |
| 5 | B | 20/18/2 | 1383.541 | 6261.762 |
| 5 | C | 20/20/0 | 910.397 | 4127.708 |
| 5 | D | 20/17/3 | 1462.022 | 6424.192 |
| 5 | POOL30-REPEAT | 20/20/0 | 850.890 | 4366.739 |
| 5 | E | 20/18/2 | 1368.893 | 4952.086 |
| 20 | A | 80/69/11 | 2004.521 | 9925.665 |
| 20 | B | 80/69/11 | 2052.410 | 8604.495 |
| 20 | C | 80/80/0 | 2511.908 | 4351.088 |

Message-only 20 posts/s completed 80 posts at every step. Message p95 at 1/10/100/1,000/5,000 was 9.747/5.804/6.319/17.510/76.903 ms; 5,000 empty wait was 516.975 s.

## Interpretation and limits

Sidebar acquisition dominates: the 5 posts/s reference summed
4108.037 s acquiring versus 4158.757 s for whole snapshots;
event/auth acquisition was 6.767/8.171 s. At 20 posts/s,
sidebar acquisition was 4320.769 of 4369.954 whole-snapshot seconds.
The count and list/transaction timings above are much smaller. This identifies
per-member snapshot queueing as the dominant measured cost, without proving
PostgreSQL CPU is idle. Initial recounts are excluded but set each stream's next
deadline: their phase spread still affects latency, as do uncontrolled host load
and scheduling. These results establish no production ceiling.

Pool30 message p95 was 850.890/841.051 ms; completion counts above constrain
that comparison. Gate10 cut pool wait to 27.648/36.831 s, but moved
16427.742/18320.786 summed seconds into slot waits, leaving each writer blocked.
Neither candidate eliminates per-member work; two pool30 runs at 5 posts/s and
one candidate run otherwise cannot select an optimal size or guarantee latency.

Recommendation: pursue pool sizing for follow-up validation with controlled load
and larger sidebars; do not adopt this synchronous gate as a standalone latency
fix. The maintainer must choose that follow-up or a design preventing queued
recounts blocking writers, and set the connection budget and recount-delay target.

Other limits: no HTTP/HTML/network, slow clients, presence, typing, moves, read
advancement, fragmented ranges, deep history or large lists; no member output
sharing. No interval, production code or wiring changed.
