# Write-time unread counters: operations and adoption (#955)

Continues the [design](unread-counters-design.md). Pre-prototype costs are **estimates**.

## Maintenance cost model

Let M be eligible organisation members, A valid materialised member/channel
bundles (`0 <= A <= M`), K moved messages, N newly read messages, D their
distinct current topics, H all channel messages, Q all channel topics, and L_m
the cost of looking up member m's ranges/join prefix. Let B_m(H,Q) denote the
exhaustive uncapped rebuild cost, including range loads. Formulas count row
mutations and predicate evaluations, not round trips or wall time; indexes,
fragmentation and batching matter. Current steps: `docs/architecture/unread-counts.md:98`.

| Operation | Additional counter maintenance (source writes excluded) |
|---|---|
| Post without scope read | 2 total rows + at most 2 author read rows; O(1), independent of M |
| Post with feed/topic cursor | Above + that read's cost below; no non-author row fan-out |
| Feed read | Aggregate N messages plus coverage/index traversal; at most `1 + D` read rows; O(N + D) after locating coverage, O(1) members |
| Topic read | Record N before union; at most 2 read rows, O(1) extra arithmetic, O(N) existing enumeration |
| Repeated/overlapping read | Search/union remains; count only new messages; N=0 adds zero count |
| Move | 2 total rows, up to `2A` read rows; K scoped sequence loads + `sum(L_m, m in A)` + up to `A*K` containment checks; O(K + sum(L_m) + A*K) |
| Branch | Move cost + notice's post cost (2 totals and at most 2 author read rows) |
| Lazy join | Zero counter rows, O(1) counter work; no change to existing M members |
| Materialisation/repair | `1 + Q` read rows (validity/revision included), B_m(H,Q); shared totals rebuild scans H and writes `1 + Q` totals |

Moves currently mutate K rows and return only a row count
(`db/queries/conversation/topic.sql:12`,
`internal/conversation/internal/postgres/writer.go:105`); A*K classifications
and up to 2A read-count writes are new: per-member **write fan-out**, not A*K
stored rows. Counter storage is O(channels + topics + materialised
member/channel + member/topic pairs); ranges remain.

For the `M-A` missing/invalid bundles, moves write no R and must not assume all
K unread: a prefix or ranges may cover some or all. Their next sidebar read uses
the exact capped recount, and enabling them requires B_m(H,Q). Dense maintenance
has A=M, up to 2M read-row mutations and M*K classifications per move; lazy
maintenance has A smaller but pays recounts for the rest. Eager join needs every
channel/topic bundle plus prefix counts.

If exact aggregation is too costly, invalidate A affected bundles (O(A) validity
writes for a move, O(1) for a read), retain total changes, and leave recounts on
the read path until rebuilt. Include fallback frequency and B_m in database
load. Index valid bundles by channel; enumerating all members
instead adds O(M) lookup cost even when A=0. Include inactive members' valid bundles.

## Concurrency and snapshots

Today range locking is per organisation/member/channel
(`db/queries/unread/ranges.sql:1`); missing lock-row creation handles unique
conflicts with a savepoint (`internal/unread/internal/postgres/writer.go:60`).
Posting and branching lock org first (`internal/conversation/posting.go:68`,
`internal/conversation/branch_tx.go:16`), as does joining
(`internal/org/signup.go:72`). Default write transactions use pgx BeginFunc;
snapshots explicitly use read-only repeatable read
(`internal/platform/postgres/tx.go:28`, `internal/platform/postgres/tx.go:79`).

Proposed conservative baseline: every counter-enabled post/read/move/join,
topic/channel creation, materialisation and repair first takes the existing
organisation row lock. Reads acquire it without incrementing event_seq. Then
lock conversation total rows by channel ID, then topic ID; then unread
member/channel lock rows by `(channel ID, member ID)`; then bundle/topic count
rows by topic ID, followed by range/floor mutations. Create-or-lock in that
order; acquire all affected member locks before classification. Never lock
org/totals while holding a member lock. Hold locks through commit. This extends
current read locking; revisions share R_mc, including floor-only updates.

With all relevant writers following this protocol, READ COMMITTED writes can
classify after acquiring locks and observe earlier committed mutations; no
post/move/join can enter between classification and update. Retain the original
message sequence and validate cursor bounds under the lock. A move and a topic
read resolve in lock order: if the move wins, candidates use the new location
and moved sequence; if the read wins, the later transfer sees its new ranges
(`db/queries/conversation/message.sql:41`). Posts cannot lose increments; tabs
recompute set difference after waiting; joins see an atomic prefix. Removing the
org lock requires a separately proven protocol.

This serialises independent reads and lengthens org locking with
fan-out/rebuilds: measure that adoption risk.

Source, counters, validity/revision and events roll back together on error.
Branch conflicts already roll back moved rows, topic and sequences
(`internal/conversation/branch_tx.go:44`). Retry only whole transactions after
confirmed rollback (bounded deadlock/serialization retries with recomputed
effects). An unknown COMMIT is not permission to replay posting/branching or add
increments again: reconcile persisted state first. Read retries are
union-idempotent, but must recompute N; the current publication is an
invalidation even after an unknown outcome (`internal/unread/reading.go:18`,
`internal/unread/reading.go:64`).

Read totals, validity/R, join state, lists and validated selection in **one**
repeatable-read snapshot. Never subtract R from a later T. Sidebar
already shares a snapshot and includes unlisted selection
(`docs/architecture/unread-counts.md:58`). Missing/invalid fallback recounts
must use that same snapshot; first-unread queries remain range/message queries
(`db/queries/conversation/unread.sql:58`).

## Drift, backfill and safe repair

Messages/ranges are authoritative. Periodically or on samples,
compare raw T/R/U, not capped badges, to an exhaustive recount at the same
pinned snapshot. #950 specifies that comparison
(`docs/architecture/sidebar-hybrid-contract.md:67`); its oracle excludes floors
and adapters and uses the persisted prefix plus containment
(`internal/realtime/sidebar_oracle_test.go:61`,
`internal/realtime/sidebar_oracle_test.go:81`). #956 reuses it only after #954
is merged; no prototype runs here. Check conservation, bounds, all topics,
missing rows and revision agreement.

On disagreement, do not send the disagreeing counter: serve the recount result
from that snapshot. Repair under the full lock order, recomputing from current
truth rather than copying an old sample. Invalidate atomically; rebuild T
through conversation and R through unread, verify and mark valid in one commit.
A total mismatch invalidates **all** channel bundles. Recount until repaired.

Sampling cannot prove unsampled counts correct. Exact service requires verified
atomic maintenance and full writer coverage. During backfill/shadow verification
or unexplained drift, suspend counter serving for affected channels (globally if
scope is unknown) and recount every result. Never serve known wrong counts.

Backfill with counter serving disabled: inventory every message/read writer,
build shared totals, then each selected member/channel bundle under the locks,
counting **uncapped** against prefix plus ranges and current topic locations. Do
not replay retained events to reconstruct reads: read state survives retention
and depends on message sequences (`docs/domain/unread.md:20`). Publish complete
bundles atomically; missing ones fall back. Before enabling, verify exhaustive
equality for no-row joins, fragmented/overlapping ranges, mixed-read moves,
floors, notices and caps, plus concurrent post/read/move/join histories at the
same boundary. Bound and measure batches; expose no half-backfill.

## Alone and with the hybrid

Counters replace valid-bundle count scans with batched reads; lists, selection
validation, first-unread and per-stream snapshot/render costs remain
(`docs/architecture/unread-counts.md:50`,
`docs/architecture/unread-counts.md:65`). Savings exchange read work for exact
aggregation, move fan-out, storage, locking, backfill and verification/repair.

Combined with #950–#952, reconciliation reads counters instead of scanning gaps;
deltas still require author, scope/read effects, moved sequences, list metadata
and committed read revisions missing from today's events/publications. The
contract requires reconciliation for these cases
(`docs/architecture/sidebar-hybrid-contract.md:18`). Preserve its coverage,
unknown-outcome handling, authorization and reconnect rules
(`docs/architecture/sidebar-hybrid-contract.md:33`); richer delta metadata
remains a hypothetical production change. Count fallback/repair in both arms;
attribute hybrid/counter savings separately and report missing bundles.

## Adoption criteria fixed before measurement

- **Always exact:** prove uncapped equality and the unchanged 99+ display,
  including concurrent histories, rollback/retry, joins without rows, moves and
  all tabs. Any wrong count rejects adoption; known-invalid state must recount.
- **Read-SQL savings alone never justify adoption.** Evaluate correctness,
  posting/read-update latency (p50/p95/p99), database reads **and writes**,
  CPU/I/O/WAL/storage, lock wait/hold time, contention and overall stability together.
- Compare matched baseline, counters alone and counters plus hybrid at the same
  workloads/resources; include member cardinality, A/M, K, fragmented ranges,
  active/inactive members, fallbacks, repair and backfill. Report changed reads
  separately from no-ops, completions, throughput, queue/pool growth,
  timeouts/errors and freshness. #950's model checks raw values even above the cap
  (`internal/realtime/sidebar_oracle_test.go:226`).
- **Do not adopt if reads improve but write-side performance or overall stability
  gets worse.** Pre-register workloads, latency/error/stability budgets, uncertainty and window
  counts in #957 before execution. Regressions or unresolved contention fail
  despite fewer read statements. Preserve #950–#952's independent gates
  when evaluating a combined arm (`docs/architecture/sidebar-hybrid-contract.md:87`).
- Keep **measured** results and **estimated** formulas/projections labelled and
  separate; never extrapolate a short smoke run into stability proof. Record every gate and maintenance/recovery cost.
- Insufficient/conflicting evidence or unresolved noise means **inconclusive**.
  Parts 2 and 3 supply correctness/measurement; this part adopts nothing.

Part 2's results are recorded in [prototype verification](unread-counters-verification.md).
