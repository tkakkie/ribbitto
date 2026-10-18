# Unread counts

How read state is stored and counted (what that costs is in
[unread count costs](unread-count-costs.md), writes are in
[unread writes](unread-writes.md))
([decision 32](../decisions/32-read-state-is-a-set-of-read-ranges-per-member-and-channel.md)).
Feed, topic and branch-notice reads, visible-page POSTs, posting writes and
sidebar counts are current. The rules are in
[unread](../domain/unread.md).

## Storage

`unread` owns the read-state tables and uses `conversation`'s API
([decision 27](../decisions/27-channels-topics-and-messages-are-one-conversation-module.md)'s
growth rule):

| Table | Key | Columns |
|---|---|---|
| `channel_read` | `(organization_id, channel_id, member_id)` | none besides the key: the row that serialises one member's writes in one channel |
| `read_range` | `(organization_id, channel_id, member_id, lo)` | `hi bigint`: the channel's messages with `lo ≤ event_seq < hi` are read |
| `topic_read_floor` | `(organization_id, topic_id, member_id)` | `channel_id`, `floor_seq bigint`: the topic's messages with `event_seq ≤ floor_seq` are read, except those with `moved_event_seq > floor_seq` |

Ranges are disjoint and never touch. The first starts at 0; its `hi` is
the **prefix end** `P`: every message below `P` is read. Without rows, the
read state is `[0, joined_event_seq + 1)`: step 1 and step 3 supply that
range for a channel with no rows, and the first write inserts it before its
own ranges, so the first range always starts at 0. A topic without a
`topic_read_floor` row has no floor: its scan starts at `P` and it has no
moved branch, which step 3 expresses as the floor `P − 1`. All keys include
`organization_id`, with composite foreign keys to `member`, `channel` and
`topic`. `conversation` already stores nullable `message.moved_event_seq`,
the sequence of its latest move, which branching writes with `topic_id` in
its transaction. Its partial index is `(organization_id, topic_id,
moved_event_seq) WHERE moved_event_seq IS NOT NULL`.

## The API between the modules

`unread` never reads `message`, and `conversation` never reads `unread`'s
tables. Page and independent sidebar snapshots (`conversation.Reader`) reach
`unread` through an injected, snapshot-bound factory. Pages also reach org's and identity's data
([cross-feature access](cross-feature-access.md)); `unread` in turn calls
`conversation`'s snapshot-bound message queries. The composition root supplies `PageCountsIn`. `unread.PageCounts` combines both count lists;
the adapter supplies org's `Membership.Member.JoinedEventSeq` and converts results;
unread never queries `member`. Feed, topic and `?before=` pages
return `PageCounts`: channel and topic count maps, the feed's first unread
and the selected topic's first unread (zero without a selection).

The channel list page reads `Reader.Sidebar` with no selected topic.
`Reader.Page` and `Reader.Sidebar` share a context loader: current channel,
channels in name/ID order, optional selected topic, first 50 topics (default
first, then case-insensitive name/ID). Its reads use the caller's snapshot.
Pages read history, cursor, then counts; standalone sidebars read sequence,
then counts. Pages render the snapshot sequence from their event cursor;
standalone sidebars keep `SidebarSnapshot.SnapshotSeq`.

`Reader.Sidebar` independently reads that context,
with both count maps in the same read-only repeatable-read snapshot. It validates
the selected topic in that organisation/channel and returns it separately,
including when unlisted, counting at most 51 topics without extending the list. Each call uses
fresh lists: a displaced non-selected topic disappears. Channel totals still
cover all topics; both maps include zero, so whole-list replacement clears badges.
It uses the same `PageCounter` and persisted member join sequence as page reads,
with its organisation sequence, but no history, author directory or writes. Production's
counts-only `SidebarCounter.ReadCounts` extension explicitly disables first
unread; selection still reaches step 3 and step 4. Legacy injected
`PageCounter` adapters retain `Read` as a compatibility fallback. Normal
page entry points still enable first unread, preserving the unread divider.

**Current (#1039 PR 3, [decision 34](../decisions/34-live-unread-counts.md)):**
Posts send shared `unread` frames instead of recounting every sidebar; client
increments are approximate between exact snapshots. Resync goes through SSE.

Production streams bind the already-resolved membership, channel and selected
topic to one `cmd/ribbitto` sidebar source (#819); a page with a panel open
binds the same selection as the conversation beside it (#874). Own posts
and current-channel moves trigger snapshots through this page-count adapter;
resync wakes the member's streams through `unread.Changes`. Web's `RenderSidebar`
shares one sidebar view builder with channel and topic pages, with or without a
side panel, preserving the feed and topic selection. It uses page components for the whole channel
list, with the current channel's topics nested (#872), and the selected heading (#818); member-dependent output bypasses the shared
message cache. Realtime retains its render-then-authorize
order immediately before sending, coalescing and cursor/heartbeat rules (#810).
Every connect recounts without `id:`, so reconnecting at a delivered message's ID
recovers a recount lost after that message. Production stream tests compare
recounts with reload targets, distinguish members with the same join sequence,
and revoke access during a gated recount. `TestLiveSidebarBranchDisplacement`
checks a real mixed-read branch displacing the selected source from 50 links;
`TestLiveSidebarBrowser` checks production replacement, retained-link focus and
the unlisted heading against a reload (#811). Post/move costs are in [sidebar results](../measurements/stream-sidebar-results.md).
Read advances now trigger this same full snapshot only for the same member and
organisation: reading A clears A and reduces the channel while preserving B;
reading the feed clears its related displayed topics in every tab. Posting's
read writes use the existing durable trigger, without a duplicate read signal.
[Read results and acceptance](../measurements/read-advance-results.md) cover the per-tab cost;
browser badge increments follow the [script contract](web-javascript.md).

Read sets and gaps use `conversation.SequenceRange`; unread owns the counters,
conversation's wiring returns concrete stores. SQL receives bound arrays or one
`int8multirange` parameter. Statements share their page or sidebar snapshot, with none per channel or topic.

| Step | Owner | Input | Statement | Output |
|---|---|---|---|---|
| 1 (current) | `unread` | member, the sidebar's channel IDs | for each channel, its first 101 ranges by `lo` (`LATERAL … ORDER BY lo LIMIT 101`) | `P` and the first gaps per channel |
| 2 (current) | `conversation` | parallel arrays of channel ID, gap `lo`, gap `hi` | for each channel, its messages in its gaps in order, at most 100 rows per channel (a `LATERAL` per channel over its gaps, `LIMIT 100`), counted per channel; `ORDER BY … LIMIT 1` returns its first unread when requested | channel counts, the feed's first unread |
| 3 (current) | `unread` | member, current channel, its listed topic IDs plus the selected topic when the list omits it | its ranges at or above `P`, and the topics' floors | the read set above `P` and a floor for every topic (`P − 1` without a row) |
| 4 (current) | `conversation` | the read set including `[0, P)` as one `int8multirange`, `P`, per-topic `(id, floor)` arrays | per topic: messages above `P − 1` and its floor, plus messages at or below its floor with `moved_event_seq > floor`; both exclude read-set containment and share an ordered cap of 100; when requested, only the selected topic returns its lowest unread `event_seq`, uncapped | topic counts, a topic view's first unread |

Step 1 uses `unreadpg.ChannelRangesIn` in the caller's snapshot. Go derives
`P` and at most 100 gaps; range 101 only bounds gap 100. Channels without
rows retain the supplied join prefix and one open gap. `unread.ChannelCounts`
deduplicates sidebar IDs and builds gap IDs and bounds together. Step 2 uses
`conversationpg.ChannelUnreadIn`: ordinal joins pair length-checked arrays;
Go supplies inclusive lower bounds as their predecessors. The channel cap orders
by gap ordinal and message sequence, returning at most 100 rows across all topics,
including an unread branch notice. Incremental sorting may finish a gap and read
ahead before stopping. Both statements share the caller's snapshot.

Step 3 uses `unreadpg.TopicStateIn` in the caller's snapshot. `ReadTopicState`
combines channel ranges and requested floors with `UNION ALL`, returning each
range once. Go separates the prefix, retains the join prefix without rows and
supplies `P − 1` for missing floors. Listed and selected topic IDs are
deduplicated before the statement, with at most 51 distinct IDs. The stored
prefix starts at zero; every other disjoint, non-touching range lies above `P`.

Step 4 uses `conversationpg.TopicUnreadIn`. Ordinal joins pair IDs and floors;
Go checks equal lengths, computes `P − 1` and adds `[0, P)` to the multirange.
All branches exclude read-set containment; only above-floor branches have a
lower sequence bound. Ordered count branches each cap at 100, then share an
ordered cap of 100.
When first unread is enabled, only the selected topic probes above the floor and materialises its unread
moved candidates before choosing the lowest `event_seq`, without a candidate
cap. `unread.TopicCounts` combines steps 3–4 in the page snapshot
([wiring](cross-feature-access.md)). The channel first-sequence expression
and topic first-unread expression each have an explicit boolean SQL guard.
Recounts pass false, so the extra channel bounded-CTE scan/sort and both topic
first-unread branches (including the moved CTE) do not execute. The count
branches, caps, four statements and their order are unchanged.

Steps 1–2 serve the channel list and steps 3–4 the topic list: two
statements per list, four per page load, whatever the number of channels or
topics. The cap applies inside steps 2 and 4, per channel and per topic;
the first-unread lookups take no cap. The [query gate](query-gate.md) accepts
`unnest` of `bigint[]` (#727) and `uuid[]` (#740) parameters, parallel arrays
joined `WITH ORDINALITY` (#749), `int8multirange` `@>` (#743) and `LATERAL`
(#741) and `UNION ALL` (#742).

## Costs

The cost of each step, the expected load, fragmentation, the benchmark and
its results are in [unread count costs](unread-count-costs.md).

## Writes

Feed, topic-view, branch-notice and posting writes are current; their range
unions are in [unread writes](unread-writes.md). A topic post with a cursor
reuses one transaction-local prepared topic for the page and own-message unions;
its ranges and final floor retain the same read-state rules.

