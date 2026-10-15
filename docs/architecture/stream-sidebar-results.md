# Live sidebar cost results (#812)

Measured 2026-10-11 on MacBook Pro Mac17,2, Apple M5 (10 cores), 32 GB,
darwin/arm64, Darwin 27.0.0; Go 1.27.2; PostgreSQL 18.6
(Debian 18.6-1.pgdg13+2), local loopback port 55434. Pool 10, batch 100,
shared event/message render caches on, authorization capacity 10,000, no stagger.
One organisation, one channel and its default topic; one distinct member per
stream, unfragmented read ranges; sidebar steps alternate initial totals 0/1.
Fresh disposable migrated database per run; fixtures and initial recounts excluded.

Export `RIBBITTO_TEST_DATABASE_URL` as `<local test database>` first
([database tests](../database-tests.md)). Historical commands (the server-recount mode has been removed):

```sh
export GOCACHE=$PWD/bin/.cache/go-build GOMODCACHE=$PWD/bin/.cache/mod
export GOLANGCI_LINT_CACHE=$PWD/bin/.cache/lint GOPATH=$PWD/bin/.cache/gopath
export RIBBITTO_REQUIRE_DB=1 RIBBITTO_STREAM_COST=1
export RIBBITTO_STREAM_COST_MEMBERS=distinct RIBBITTO_STREAM_COST_CACHE=1
export RIBBITTO_STREAM_COST_POOL=10 RIBBITTO_STREAM_COST_STEPS=1,10,100,1000,5000
export RIBBITTO_STREAM_COST_DURATION=4s
RIBBITTO_STREAM_COST_RATE=20 go test -count=1 -run '^TestStreamCost$' -v -timeout 10m ./internal/realtime/
RIBBITTO_STREAM_COST_SIDEBAR=1 RIBBITTO_STREAM_COST_RATE=5 go test -count=1 -run '^TestStreamCost$' -v -timeout 10m ./internal/realtime/
RIBBITTO_STREAM_COST_SIDEBAR=1 RIBBITTO_STREAM_COST_RATE=20 go test -count=1 -run '^TestStreamCost$' -v -timeout 10m ./internal/realtime/
```

All scheduled posts completed: baseline/sidebar20 80 per step, sidebar5 20;
all messages and each member's final snapshot arrived. SQL is attempted query/tx
statements. Count SQL is a subset of queries. Time includes drain. Wait is empty
acquisitions/total seconds, summed across acquisitions, not wall time. Latency
triplets are p50/p95/p99 in milliseconds; definitions are in [stream cost](stream-cost.md#sidebar-mode-812).

| Mode | Streams | SQL query/tx | Count SQL | Recounts | Time | Recounts/connection/s | Count SQL/recount /post | Pool empty/wait s | Message ms | Recount delay ms |
|---|---|---|---|---|---|---|---|---|---|---|
| baseline | 1 | 881/480 | 0 | 0 | 3.964s | 0 | —/0 | 0/0.000 | 3.185/3.783/4.891 | — |
| baseline | 10 | 1129/640 | 0 | 0 | 3.966s | 0 | —/0 | 8/0.037 | 3.380/3.745/11.347 | — |
| baseline | 100 | 1212/640 | 0 | 0 | 3.968s | 0 | —/0 | 81/0.272 | 3.557/4.460/6.502 | — |
| baseline | 1000 | 2237/640 | 0 | 0 | 3.965s | 0 | —/0 | 882/12.076 | 6.884/10.055/18.260 | — |
| baseline | 5000 | 5346/640 | 0 | 0 | 3.987s | 0 | —/0 | 3990/206.612 | 22.031/49.370/86.006 | — |
| sidebar5 | 1 | 260/136 | 16 | 4 | 4.02s | 0.995 | 4.00/0.80 | 1/0.004 | 3.563/5.793/5.793 | 602.714/1007.954/1007.954 |
| sidebar5 | 10 | 635/282 | 160 | 40 | 4.04s | 0.990 | 4.00/8.00 | 0/0.000 | 3.724/5.525/5.736 | 598.558/1006.485/1009.481 |
| sidebar5 | 100 | 3211/1018 | 1600 | 400 | 4.113s | 0.973 | 4.00/80.00 | 333/3.198 | 3.588/22.164/22.514 | 488.884/1016.917/1028.651 |
| sidebar5 | 1000 | 28520/8256 | 16000 | 4000 | 5.335s | 0.750 | 4.00/800.00 | 3955/548.138 | 6.147/19.754/20.692 | 865.781/1329.092/1350.036 |
| sidebar5 | 5000 | 112828/32328 | 64218 | 16052 | 7.703s | 0.417 | 4.00/3210.90 | 16131/5703.474 | 1211.275/2401.015/3003.472 | 1540.934/2678.375/2887.517 |
| sidebar20 | 1 | 918/494 | 16 | 4 | 4.026s | 0.993 | 4.00/0.20 | 1/0.003 | 3.998/4.554/7.610 | 509.478/960.456/1002.335 |
| sidebar20 | 10 | 1470/758 | 160 | 40 | 4.045s | 0.989 | 4.00/2.00 | 0/0.000 | 4.547/5.804/6.768 | 514.064/962.824/1009.661 |
| sidebar20 | 100 | 3982/1464 | 1600 | 400 | 4.245s | 0.942 | 4.00/20.00 | 373/12.066 | 4.755/6.084/65.578 | 612.707/1053.071/1067.287 |
| sidebar20 | 1000 | 29589/8772 | 16000 | 4000 | 5.09s | 0.786 | 4.00/200.00 | 3998/366.167 | 9.555/149.148/288.537 | 818.348/1277.564/1344.142 |
| sidebar20 | 5000 | 106376/30531 | 60018 | 14964 | 7.643s | 0.392 | 4.01/750.23 | 15109/5646.843 | 1474.731/2517.563/2528.164 | 1663.775/2526.792/2825.186 |

At 1–1,000 streams, quadrupling posts from 20 to 80 left recounts and count SQL
unchanged: four recounts/member, four count statements/recount. At 5,000,
recounts fell to 3.21 and 2.99/member over longer drains; attempted count SQL
also includes in-flight recounts at the sampling boundary. This supports interval
coalescing under these conditions, not independence from all posting loads.

Baseline message p95 stayed below 50 ms. Sidebar message p95 at 5,000 was
2.401/2.518 s (5/20 posts/s), exceeding the benchmark's 1 s criterion; recount
delay p95 was 2.678/2.527 s. These ladders' last passing step was 1,000 and
first failing step 5,000; they establish no production ceiling. Pool waits and
sidebar SQL increased together; investigating that contention needs a follow-up.
No optimisation or interval change was made.

The stopped test-only attribution and candidate comparisons for #826 are
summarised in [sidebar counts history](sidebar-counts-history.md).

Limits: single short runs, growing warm authorization caches, no controlled host
load or CPU/memory sampling. No HTTP, HTML, network, slow clients, presence,
typing, read advancement or production post-read writes; baseline has no unread
seed, sidebar adds one excluded seed per step. No fragmented ranges, many lists,
moves or deep history. These historical runs used a <=99-post guard so both totals proved freshness
within the 100 cap. The removed #832 harness replaced
that guard with a same-snapshot sequence witness for longer runs
([history](sidebar-counts-history.md)). Recount output is per member
and never shared; this does not guarantee performance for larger data sets.
