# Read-advance cost and acceptance (#711)

Measured 2026-10-12 JST on a MacBook Pro, Apple M5 (10 cores: 4 Super,
6 Efficiency), 32 GB; macOS 27.0.1, Darwin 27.0.0, darwin/arm64; Go 1.27.2;
PostgreSQL 18.6 (Debian 18.6-1.pgdg13+2), local loopback port 55435.
Pool 10 for streams, 1 for matched unions. Another Codex acceptance run could
load this host; sampled load averages were 3.69/4.47/4.42. No exclusive CPU
reservation or continuous CPU attribution. The 16-tab rows and matched unions
below use the final **sequential rerun** after a noisy initial sample. An
intermediate overlapping rerun was discarded. In the initial 100/16 concentrated
step, topic transaction time was 16.936 ms; initial 1,000-fragment baseline
branch p95 was 7.274 ms. Variation is not a regression or equivalence verdict.

## Reproduce

Export `RIBBITTO_TEST_DATABASE_URL` as `<local test database>` first
([database tests](../database-tests.md)). Exact commands, run sequentially:

```sh
export GOCACHE=$PWD/bin/.cache/go-build GOMODCACHE=$PWD/bin/.cache/mod
export GOLANGCI_LINT_CACHE=$PWD/bin/.cache/lint GOPATH=$PWD/bin/.cache/gopath
export RIBBITTO_REQUIRE_DB=1
RIBBITTO_READ_COST=1 go test -count=1 -run '^TestReadAdvanceCost$' -v -timeout 10m ./cmd/ribbitto
RIBBITTO_READ_COST=1 go test -count=1 -run '^TestReadMergeCost$' -v -timeout 5m ./internal/unread/internal/postgres
RIBBITTO_READ_COST=1 RIBBITTO_READ_COST_STEPS=100,1000 RIBBITTO_READ_COST_TABS=16 go test -count=1 -run '^TestReadAdvanceCost$' -v -timeout 5m ./cmd/ribbitto
RIBBITTO_READ_COST=1 go test -count=1 -run '^TestReadMergeCost$' -v -timeout 5m ./internal/unread/internal/postgres
```

The new test files are [read cost](../../cmd/ribbitto/read_cost_test.go) and
[matched union cost](../../internal/unread/internal/postgres/read_merge_cost_test.go).
Both skip without the opt-in gate; normal checks pay no measurement cost.
Fresh disposable migrated databases; seeding and verified initial recounts excluded.
Real transactions, detached bounded Reading commits, stream/coalescer, shared event
cache and cached authorization; HTML/network writes replaced by count witnesses.
No posts, heartbeats or watermark worker during the read phase. Each reader has
four uncapped unread messages, two each in interleaved A/B. Every tab must show
4/2/2 initially, 2/0/2 after A, then 0/0/0 after feed; repeating each read is a no-op.
Channel handoffs drain every expected frame and reach the next watch capture;
15 s is a failure deadline, not synchronization. All calls and drains completed;
no offered-rate scheduler or missed-work metric is used in this finite workload.

## Read transactions

C concentrates reads on one member; D distributes them across up to four members,
concurrently, with the specified matching tabs **per reader**. Remaining connections
alternate same-organisation distinct members and another organisation. Impossible
combinations (16 tabs with only 1 or 10 connections) are omitted. Numbers are
absolute samples or min–max across readers, **not** meaningful tail percentiles.
Transaction time is Reading call time minus timed registry publication; it includes
acquisition, BEGIN, write SQL and COMMIT plus small harness overhead. Publication
wraps `Changes.ReadChanged`, excluding the result-map lock. Delays start at the
successful COMMIT acknowledgement and end at recount entry or collected frame;
frame timing includes recount/authorization but excludes browser/network latency.

| Mode | Total/tabs/readers | Topic ms | Topic no-op ms | Feed ms | Feed no-op ms | Publication µs | Max recount/frame ms |
|---|---|---|---|---|---|---|---|
| C | 1/1/1 | 6.752 | 7.016 | 6.029 | 5.210 | 4.125–5.875 | 999.551/1006.044 |
| C | 10/1/1 | 6.269 | 6.700 | 6.116 | 4.645 | 2.875–4.417 | 992.463/998.354 |
| C | 10/2/1 | 6.468 | 11.167 | 5.102 | 5.200 | 1.791–4.000 | 984.975/989.615 |
| C | 100/1/1 | 7.216 | 6.412 | 7.338 | 3.560 | 1.167–2.875 | 990.043/993.965 |
| C | 100/2/1 | 6.780 | 11.037 | 4.519 | 3.671 | 3.000–7.208 | 987.873/991.774 |
| C | 100/16/1 | 6.827 | 6.843 | 3.249 | 4.545 | 3.583–4.417 | 993.725/997.363 |
| C | 1000/1/1 | 6.844 | 2.778 | 2.819 | 5.295 | 1.209–1.875 | 1001.193/1007.049 |
| C | 1000/2/1 | 6.371 | 5.314 | 2.576 | 4.969 | 1.292–2.667 | 995.022/1000.072 |
| C | 1000/16/1 | 6.321 | 4.815 | 2.266 | 3.806 | 3.625–4.292 | 999.880/1003.693 |
| D | 1/1/1 | 6.623 | 5.372 | 4.294 | 4.459 | 1.125–1.458 | 997.659/1003.027 |
| D | 10/1/4 | 12.078–15.923 | 7.922–8.430 | 6.252–6.546 | 5.388–5.693 | 0.584–20.708 | 988.975/994.991 |
| D | 10/2/4 | 9.047–12.596 | 6.790–9.918 | 4.529–4.904 | 6.415–6.975 | 1.209–5.000 | 990.623/997.538 |
| D | 100/1/4 | 8.553–12.193 | 8.894–9.293 | 6.291–6.778 | 6.405–6.715 | 0.625–6.667 | 989.361/994.805 |
| D | 100/2/4 | 10.155–12.595 | 6.490–9.376 | 5.500–6.225 | 5.872–6.156 | 0.791–4.208 | 988.899/993.175 |
| D | 100/16/4 | 9.270–12.004 | 4.193–6.514 | 3.643–3.825 | 3.402–4.099 | 2.584–5.750 | 990.226/992.196 |
| D | 1000/1/4 | 8.968–12.118 | 4.996–5.681 | 4.303–4.778 | 5.304–5.595 | 0.792–3.083 | 993.672/1003.418 |
| D | 1000/2/4 | 8.219–18.525 | 3.906–6.262 | 3.424–3.705 | 5.338–5.864 | 0.584–6.292 | 993.556/999.628 |
| D | 1000/16/4 | 10.182–13.873 | 4.488–6.840 | 3.527–3.938 | 3.555–5.423 | 2.459–4.500 | 1015.185/1019.544 |
| zero | 0/0/1 | 8.049 | 3.380 | 2.861 | 2.146 | 0.334–0.416 | — |

Write SQL is exactly 18/14/12/11 statements for effective topic, topic no-op,
effective feed and feed no-op here, including BEGIN/COMMIT and first-use lock-row
savepoint work. No-op writes still transact; only effective reads publish.
Zero-subscriber publication retains no registry entry (also `TestChangesLifetime`).

For each matching tab the two advances produced **2 observed invalidation wakes,
2 recounts, 2 frames and 2 authorization checks**, with 18 sidebar SQL statements
including transactions/list lookups, of which **8** were count statements.
Snapshots/loop reads were 10/4 through 100 connections and 8/3 at 1,000: early
members' initial coalescer deadlines had expired while the later tabs connected.
This explains the shorter first delay at 1,000; increasing connections did not
make the write intrinsically faster. A later read still waited roughly one second.
Wakes count generation transitions observed in watch snapshots, not OS scheduling.

The harness logs these counters and SQL separately for every organisation/member.
Across **all unrelated keys**, snapshots, wakes, loop reads, recounts, frames,
checks and SQL were **zero** after setup. Matching event SQL varies with shared
cache winners (four statements per database event-read snapshot); authorization
SQL varies with cached allows/epoch checks. These statements are attributed to the
member whose call starts the shared load, not multiplied by cache recipients.
For example, the rerun C 1,000/16 member totals were snapshots 128, wakes 32,
loop reads 48, recounts/frames/checks 32 each, sidebar SQL 288 (128 count),
event SQL 32, authorization SQL 15. D's four 1,000/16 reader keys each had those
same first six counts and sidebar/count SQL; event SQL was 0–24 and auth SQL 1–13.

## MergeCovered in posting and branching

Both arms run identical real committed transactions on restored fragmented ranges.
The test-local baseline uses the union from `c283544^` (before #834), with the
same queries, lock and join prefix but no sorting/difference construction.
One sparse message follows the prefix; non-message event rows fill a sequence gap.
Both posting's own-message range (history posting, nil page cursor) and branching's
own-notice range absorb all fragments. Feed/topic page writes are measured above;
this is a deliberately fragmented union stress case, not typical dense history.
The persisted union is checked after every arm. Two warmups, ten samples per arm,
alternating order; setup/restoration excluded. Bytes are process TotalAlloc deltas,
including pgx/harness/background allocations, not isolated sorting allocations.
Times include transaction completion; p50 uses the upper middle sample; p95
is the maximum of only ten samples.

| Fragments | Flow | Baseline p50/p95 ms | Current p50/p95 ms | Baseline/current bytes/op | Statements each |
|---|---|---|---|---|---|
| 1 | merge | 1.694/1.919 | 1.734/1.815 | 8963/8984 | 9 |
| 1 | posting | 2.487/2.815 | 2.669/2.837 | 16678/16464 | 14 |
| 1 | branching | 3.372/6.114 | 3.552/7.051 | 22086/22096 | 18 |
| 100 | merge | 1.833/2.105 | 1.858/2.084 | 17745/21776 | 9 |
| 100 | posting | 2.799/2.958 | 2.733/2.912 | 25449/29535 | 14 |
| 100 | branching | 3.666/4.311 | 3.656/3.796 | 30852/34880 | 18 |
| 1000 | merge | 2.184/2.330 | 2.252/2.502 | 107296/157670 | 9 |
| 1000 | posting | 3.421/3.731 | 3.494/4.132 | 115048/165168 | 14 |
| 1000 | branching | 4.036/4.687 | 4.213/4.543 | 120433/170801 | 18 |

At 1,000 fragments sorting/subtraction adds about 50 KB per transaction with no
additional SQL. These short samples cannot establish posting latency equivalence,
sustained throughput, connection ceilings, network/HTML cost or cross-process
behaviour. #832's maintainer-started overnight comparison follows separately.
The maintainer accepted these short runs for #711 condition 5.

## Acceptance and recovery boundaries

#828's changed-after-commit reporting is completed by #834/#837; #829's bounded
member registry and coalesced stream delivery by #835/#842. #711 has three stacked
PRs: production wiring/core HTTP acceptance; race/isolation/recovery/browser
acceptance (parallel handoff); this measurement/documentation PR closes the parent.
`TestReadSidebarHTTPAllTabs` covers A=2, B=3, channel=5 across five page variants;
`TestReadSidebarHTTPIsolationAndNoop` counts member-scoped queries and frames.
`TestReadPublicationConcurrent` and `TestReadAdaptersDoNotPublish` cover effective
publication and posting/branching adapters. Realtime's `TestSidebarWatchRaces`,
`TestSidebarWatchCoalescingAndIsolation` and `TestSidebarWatchLifetime` cover the
watch boundaries/coalescer; `TestSidebarReconnectAfterMessageBeforeRecount` covers
reconnect recounts. The second #711 PR adds the remaining production race,
recovery and browser evidence; its browser validation belongs to that handoff,
not to this machine's measurements.

Healthy single-process streams converge by captured generations. No-op reads do
not publish; posting reuses `message.posted`, with no duplicate read signal.
Read-only publications do not move durable cursors. Lost process-local signals
have no bounded repair without reconnect; the durable watermark cannot help.
An unknown-COMMIT recount may precede persistence and miss the eventual commit:
[unread writes](unread-writes.md) records the exception, spurious requests and
failure limits. Multiple processes need shared member invalidation and a
subscribe/snapshot handshake; bounded loss recovery needs durable revisions/replay
([member contract](ephemeral-state.md#member-read-change-watches)).
