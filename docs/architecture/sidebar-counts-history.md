# Sidebar counts: investigation history (2026-10)

The server refreshed each connection's sidebar from its member's read state
while posts arrived. Coalescing reduced recount frequency, but database work
still grew with connections. This note preserves findings and limits from the
stopped investigation. Source titles and dates identify evidence retained in
git history after the investigation documents are removed.

## Server recount cost

In *Live sidebar cost results (#812)*, measured 2026-10-11, distinct-member
streams used a shared pool of 10 and short posting windows. At 5,000 streams:

| Workload | Message arrival median | Message arrival p95 |
|---|---:|---:|
| No unread counts, 20 posts/s | 22.031 ms | 49.370 ms |
| Server recounts, 5 posts/s | 1,211.275 ms | 2,401.015 ms |
| Server recounts, 20 posts/s | 1,474.731 ms | 2,517.563 ms |

*Live sidebar cost results (#812)*, measured 2026-10-11, supplies the table
and records 60,018 attempted count statements and 14,964 sent recounts for its
high-rate sidebar step, drain included and initial snapshots excluded.
Its no-unread baseline remains in [the original results](stream-sidebar-results.md).
These were synthetic loop measurements, without HTTP, HTML, slow clients,
fragmented ranges, moves or read advancement; they establish no production ceiling.

*Sidebar bottleneck results (#826)*, measured 2026-10-11, attributed most
whole-snapshot elapsed time to acquiring a connection, rather than executing
the individual count statements. Enlarging the pool improved some runs, but
completion varied under uncontrolled host load. A synchronous concurrency gate
reduced database-pool waiting by moving waiting into the recount slot; the
stream's writer still waited. Neither experiment removed member-specific work.

Counts-only recounts also stopped computing first-unread positions.
*Unread count costs*, measured 2026-10-12, records a listed-topic statement
falling from 0.291 to 0.047 ms, but an unlisted selection from 29.801 to
29.485 ms in warm plans. This demonstrated skipped work, not an end-to-end
throughput guarantee. [Unread count costs](unread-count-costs.md) retains the
query costs and qualifications; [Stream cost](stream-cost.md) explains the
benchmark's accounting and clocks.

## Pool, queue and writer experiments

*Sidebar pool comparison (#832, part 1)*, verified 2026-10-11, established
with gated tests that a synchronous recount blocks its own stream's writer.
Splitting pools cannot fix that. Required host probes prevented its initial
controlled ladder; its smoke proved function, not pool sizing or latency.

*Sidebar recount scheduler (#832, PR 2)* and *Sidebar single-writer prototype
(#832, PR 3)* supplied test-only FIFO admission, replaceable pending work,
bounded workers and completion mailboxes, cancellation and a sole sender.
Gated tests checked progress during recounts, completions between event batches,
pre-send authorization, revocation, cursor order and id-free sidebar frames.
Production gaps included replay/interests, ephemeral owners, watches and HTTP.

*Sidebar comparison arms and queue telemetry (#832, PR 4)* separated plain
synchronous pools, a synchronous source gate, queue-backed synchronous delivery,
and an asynchronous sole writer. Fixed queue/pool comparisons isolated writer
scheduling; equal connection budgets isolated pool partitioning. Queue length
and oldest pending age could disqualify a candidate despite passing latency.

*Sidebar recount arms: controlled results (#832)*, measured 2026-10-12,
compared plain pools of 10/20/30/40, a ten-slot gate, shared
40-connection queue arms and a normal10/recount30 partition. At 5,000 streams
plain pool sizing failed the targets; synchronous queue delivery still held
messages behind recounts. Some plain-pool windows also missed offered posts.
Companions *Sidebar arms: latency and workload tables (#832)* and *Sidebar arms:
accounting totals (#832)* retain every window, spread and SQL/pool total.

The asynchronous writer discarded useful completions after newer invalidations.
Sending completions newer than the last sent version while retaining later dirt
restored progress. Corrected shared40 and normal10/recount30 passed latency/workload
targets, but queue eligibility was not universal. Partitioning improved posting
and delivery but worsened freshness/throughput (*Sidebar recount arms: controlled
results (#832)*, measured 2026-10-12).

*Sidebar recount arms: controlled results (#832)*, measured 2026-10-12,
records five-minute shared40 runs at 5,000 streams: median post-return-to-send
p95 was 25.25/25.84 ms at 5/20 posts/s; freshness p95 was 1,224.77/1,476.25 ms.
All passed latency/workload targets, but two low-rate windows failed the old
queue-growth guard. Minute means suggested a plateau over the observed horizon,
without establishing long-run stability or selecting a production pool layout.

## Stability and validation plans

*Sidebar queue stability criterion and evidence (#832, parts 1–2 of 3)*,
fixed 2026-10-12 before new measurement, required longer, repeated independent
windows. It compared adjacent and widely separated means after warm-up, with
tolerances for both pending length and age. Short archived windows remained
inconclusive under the new rule; their old growth failures were not erased.

*Sidebar stage-1 plan (#832, stability work part 3)*, pre-registered 2026-10-12,
compared shared and partitioned pools with fixed connection and worker budgets.
Deployment overlap, external clients and normal work headroom constrained sizing.
The plan records assumptions and smokes, not production defaults or load results.

*Sidebar stage-2 protocol (#865, PR 3)*, fixed 2026-10-13 before measurement,
specified authenticated mixed HTTP traffic: posts, changed reads, page loads
and stream replacement. It required normal-work latency/headroom and refusal
checks alongside delivery, freshness, complete work and queue stability.
The driver, telemetry, headroom and stability notes describe implemented
test infrastructure, not load results. The snapshot-witness note records
capture inside the counts' repeatable-read transaction; emission and the runner
were still missing. Stage-2 validation and production activation were separate.

## Hybrid and write-time counter alternatives

The hybrid contract and hypothetical, coverage, delivery and progress notes
explored exact deltas plus reconciliation. Production post events lacked
author/read effects; read publications were invalidations. Relevant operations
generally still needed recounts. Richer author or committed-read metadata was
hypothetical, not evidence of exact deltas from existing production events.

Tests compared full models with an independent oracle at the same persisted
snapshot, including uncapped counts, lists, selection and empty badges. They
checked replay after snapshot coverage, stale completions, reconnects,
revocation and repair progress during continuing posts. Matched cost arms,
changed-read driving, SQL accounting and bounded send witnesses were added,
but the delivery note still left freshness/drift evaluation and adoption
gates unfinished. It records no hybrid measurement or demonstrated savings.

*Write-time unread counters: design (#955)* and its operations/adoption note
proposed shared message totals minus each member's read totals, retaining
ranges as truth. Missing or invalid bundles would recount. Moves required
member-dependent classification and writes; exact read effects, conservative
organisation locking, materialisation, backfill and repair added costs.
The adoption rule rejected read savings accompanied by worse writes or stability.

*Write-time unread counters: prototype verification (#956)* checked arithmetic,
production-writer histories, rollback/retry and pinned snapshots. The SQL-adapter
and fixed-workload notes added test-only maintenance, accounting, contention
observation, fallback and repair. Their protocol remained unmeasured; stream
integration and completion/drain checks were pending. Correctness evidence and
estimated maintenance costs did not establish a performance win or adoption.

## Replacement by client counts

[Decision 34: Live sidebar unread counts are eventually exact](../decisions/34-live-unread-counts.md),
decided 2026-10-14, records the maintainer stopping the recount, stage-2, hybrid
and counter investigations. It chose exact page-load/resync counts with
approximate live badges between successful resyncs. Small shared post frames
replace all-connection post recounts; reads and own posts recount the member's
tabs. Read truth and exact page-load SQL stay unchanged.

*Client unread count cost (#1047)*, measured 2026-10-15, then recorded
28.373 ms median message arrival at 5,000 streams and 20 posts/s, with half
the streams receiving shared unread frames. The simultaneous final resync took
5.035 s. [Client count results](stream-client-counts-results.md) retain the
comparison: a short single run, an older no-unread baseline and different frame
placement. Delivery improved in that test; resync still incurs snapshot work.
