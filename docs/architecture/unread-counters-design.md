# Write-time unread counters: design (#955)

Investigation proposal, fixed before #956's prototype or #957's measurement; not
an adopted architecture. [Operations and adoption](unread-counters-adoption.md)
completes this note. Production, display rules, intervals, async, front-end and #865 remain unchanged.

## Truth and stored state

Read truth remains message sequence containment in the member/channel ranges
plus `[0, joined_event_seq + 1)`, even without read rows. Moves preserve the
sequence; topic floors only accelerate scans, never decide read status
(`docs/domain/unread.md:29`, `docs/domain/unread.md:46`). A channel count covers
**all** topics, including those outside the sidebar's 50 links
(`docs/domain/unread.md:100`). A total minus one cursor cannot express these
rules (`docs/domain/unread.md:127`); a stored read *count* must follow the set.

Proposed uncapped bigint counters, all scoped by organisation:

| Owner | Counter/key | Meaning |
|---|---|---|
| conversation | `T_c`, channel | Number of messages in the channel, notices included |
| conversation | `T_t`, channel/topic | Number currently in the topic |
| unread | `R_mc`, member/channel | Number of those messages read by this member |
| unread | `R_mt`, member/channel/topic | Number of those topic messages read by this member |
| unread | member/channel bundle validity and revision | Whether the channel read count and every topic read count are complete and maintained |

For a valid bundle, `U_mc = T_c - R_mc`, `U_mt = T_t - R_mt`. Store uncapped
values: subtracting from 100 cannot distinguish 101 from 10,000. Display is
`min(U,100)`, 100 means `99+`, zero has no badge
(`internal/realtime/sidebar_oracle_test.go:30`). Check `0 <= R <= T` and `T_c =
sum(T_t)`, `R_mc = sum(R_mt)`; overflow fails the transaction. Validity/revision
share the R_mc row. The proposed persisted read-state revision is independent of
org's event sequence.

Materialisation is lazy by **whole member/channel bundle**, with all current
topics, not just listed ones. A valid bundle's absent row for a newly created,
empty topic means zero; creation and subsequent transfers/posts must maintain
it. An absent or invalid bundle has **no usable R**, not zero: use the existing
capped recount in the caller's snapshot. Before enabling it, initialise from an
exhaustive uncapped message/range count, including the join prefix. Never
initialise R from today's capped SQL (`db/queries/conversation/unread.sql:18`,
`db/queries/conversation/unread.sql:49`). Lazy fallback is exact but saves no
scans for that member; full materialisation increases fan-out and storage.

Existing read-state tables are lock rows, ranges and floors, with organisation
keys and composite foreign keys (`db/migrations/00012_unread.sql:3`,
`db/migrations/00012_unread.sql:11`, `db/migrations/00012_unread.sql:20`). The
proposed counters add member/scope rows, **no member/message rows**. The
subscription-counter fan-out model named Rocket.Chat in #955 is a useful cost
category; the per-member/per-message models named Zulip and Synapse are
excluded.

## Exact transaction increments

All increments below occur with the source mutation in one transaction, under
the [proposed lock
order](unread-counters-adoption.md#concurrency-and-snapshots). They describe
valid bundles; absent bundles stay absent and recount on reads.

**Post:** add 1 to `T_c` and the posting topic's `T_t`. For each non-author, R
is unchanged: the new sequence follows committed events and cannot already be in
a validated read range. Current posting takes org's next sequence before
inserting (`internal/conversation/posting.go:68`); cursors cannot read future
events (`internal/unread/feed.go:64`, `internal/unread/topic.go:52`). Add 1 to
the author's `R_mc` and `R_mt` for the own message, plus the exact newly read
**old** messages in the composer's scope; advance its read revision. Do not
count that own message twice when combining the unions.

Feed-cursor posts apply the grouped read-count increment below; topic-cursor
posts apply only that topic's candidates. Cursorless older-page posts add only
the own message, leaving other read status and floors alone
(`docs/domain/unread.md:69`, `cmd/ribbitto/read_adapters.go:79`). The adapter
currently discards changed booleans, then unions the own-message bounds; topic
posts reuse a prepared topic (`cmd/ribbitto/read_adapters.go:82`,
`cmd/ribbitto/read_adapters.go:105`). Counter-aware APIs must retain the exact
old-message effects. Keep the floor optimisation's intervening-post/move check
(`internal/conversation/posting.go:105`); raising a floor adds no count itself.
Non-page posting permits no read writer (`internal/conversation/posting.go:96`):
any future counter-enabled message writer must establish own-message read truth
too; otherwise keep counter serving disabled; incrementing R alone is invalid.

**Feed advance:** `MergeCovered` returns new coverage after establishing the
join prefix (`internal/unread/internal/postgres/writer.go:42`). The current feed
path converts it to bounds but asks only `HasMessages`, an EXISTS probe
(`internal/unread/feed.go:78`, `internal/unread/feed.go:89`,
`db/queries/conversation/message.sql:57`). Interval width is not a message
count: events and messages of other channels leave sequence holes. Add a
transaction-bound conversation query counting messages in that coverage,
**uncapped and grouped by current topic**. Increase R_mc by the group sum and
each R_mt by its group count. Include off-list topics. If exact aggregation is
too costly or unavailable, invalidate the entire bundle in the same transaction;
serve a capped recount, then rebuild uncapped before reuse. Never guess from
EXISTS or merely clear the displayed topics.

**Topic advance:** the locked candidate query returns one bounds row per unread
message of the current topic; it excludes reads, the prefix and moves later than
the cursor (`internal/unread/topic.go:59`,
`db/queries/conversation/message.sql:32`). Thus the extra exact count is `N =
len(ranges)` **before** coalescing, not the number or width of merged intervals.
Increase R_mc and this R_mt by N. No additional message count query is necessary
while that one-row-per-message contract holds. Batch Add merges ranges and
raises the floor even for an empty batch
(`internal/unread/internal/postgres/topic.go:80`,
`internal/unread/internal/postgres/topic.go:100`). N=0 changes no count;
revision tracks any range/floor change.

**Repeated/overlapping tabs:** compute new coverage/candidates after taking the
same member/channel lock. A repeat then contributes zero; overlapping reads
contribute only set difference, not each request's cursor distance. The union
subtracts stored neighbours before widening
(`internal/unread/internal/postgres/writer.go:107`); topic preparation loads
ranges under that lock (`internal/unread/internal/postgres/topic.go:39`).
Counter and range changes commit together, retaining union-only semantics
(`docs/domain/unread.md:38`).

**Move K messages from a to b:** channel T and R stay unchanged. Topic totals
change `T_a -= K`, `T_b += K`. For each valid member bundle count `K_read(m) =
number of selected original sequences in ranges OR <= join sequence`. Transfer
`R_ma -= K_read(m)`, `R_mb += K_read(m)`; consequently unread transfers are `K -
K_read(m)`. A fully read member transfers zero unread; an unread member
transfers K. Ignore destination floors, even when moved messages fall below them
(`docs/domain/unread.md:34`, `docs/domain/unread.md:46`).

Current MoveMessages returns only affected-row count, not sequences or member
effects (`internal/conversation/internal/postgres/writer.go:103`); its SQL
changes topic and move sequence, preserving original sequence
(`db/queries/conversation/topic.sql:12`). Retrieve the scoped selected sequences
in the locked transaction (or extend RETURNING), verify all K still belong to a,
then ask unread's API for each valid bundle's containment count. Existing scoped
message loading is available (`db/queries/conversation/message.sql:17`), but no
cheap exact per-member transfer aggregate exists in this path. A batch aggregate
still performs member-dependent membership work. If unavailable/over budget,
atomically invalidate affected bundles and recount; missing bundles also
recount. Never transfer K unread for every member.

**Branch:** the above move plus a new source-topic notice. The transaction
allocates move then notice sequences, verifies the moved count, appends the
move, inserts the notice and reads only its author's notice bounds
(`internal/conversation/branch_tx.go:16`,
`internal/conversation/branch_tx.go:40`,
`internal/conversation/branch_tx.go:54`,
`internal/conversation/branch_tx.go:64`). The notice is in the source topic: add 1 to `T_c` and
the source `T_t`, and, for the author only, to `R_mc` and the source `R_mt`
(`T_c = sum(T_t)` holds). Moving existing
messages implicitly reads none, including for the branch author
(`docs/domain/unread.md:73`). Failed selection, notice or event append must roll
back all counter changes too.

**Join:** sign-up takes org's sequence, stores it on the member and appends the
join event atomically (`internal/org/signup.go:72`, `docs/domain/unread.md:15`).
Create no bundle initially. Logical R already includes every message with
sequence <= joined_event_seq in each current topic; setting R=0 would be wrong
even before any read rows exist. First materialisation counts that prefix plus
ranges against **current** message locations, including subsequent moves. Later
posts increment T, not this member's R until read; joins never change T.

## Ownership of the proposed writes

Today's owners and injected atomic exceptions are in
`docs/architecture/features.md:21`, `docs/architecture/features.md:25` and
`docs/architecture/features.md:44`; modules do not query each other's read
tables (`docs/architecture/unread-counts.md:39`). Keep that direction:
conversation writes totals and supplies scoped message aggregates/sequences;
unread writes read counts, validity and ranges, consuming injected conversation
APIs. Org supplies join sequences, eligible member scope and a transaction-bound
lock API.

New cross-feature flow: conversation's move transaction calls unread to transfer
or invalidate read-count bundles, because read status must follow the moved
messages atomically. Posting/branch-notice writes extend their existing injected
read writers with exact count effects. Read transactions call org's lock API
without allocating an event sequence, to prevent a move racing classification.
Lazy joins need no counter write; an eager join variant would require an
injected unread initialiser in org's transaction. Backfill/repair orchestrates
totals and read bundles through their owners' APIs for the same atomicity.
Implementation must register these flows and query-gate changes; this note
authorises no cross-owner SQL or migration trigger.
