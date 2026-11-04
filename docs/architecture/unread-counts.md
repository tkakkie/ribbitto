# Unread counts

The [domain rules](../domain/unread.md) and
[decision 32](../decisions/32-read-state-is-a-set-of-read-ranges-per-member-and-channel.md)
define read state; [unread count costs](unread-count-costs.md) covers its
cost limits and benchmark, and [unread writes](unread-writes.md) covers writes.

## Storage

Table keys, columns and constraints are in the schema pages for
[`channel_read`](../schema/public.channel_read.md),
[`read_range`](../schema/public.read_range.md) and
[`topic_read_floor`](../schema/public.topic_read_floor.md).
The [read-state rules](../domain/unread.md#read-state) define `lo`, `hi`
and `floor_seq`.

Ranges are disjoint and never touch. The first starts at 0; its `hi` is
the **prefix end** `P`: every message below `P` is read. Without rows, the
read state is `[0, joined_event_seq + 1)`; the first write establishes that
prefix before its own ranges. A topic without a floor starts scanning at
`P`, with no moved branch: its effective floor is `P − 1`.

## Counting contract

`unread` never reads `message`, and `conversation` never reads `unread`'s
tables. The [cross-feature access](cross-feature-access.md) rules describe
the snapshot-bound APIs and composition: `compose.PageCountsIn` binds unread
counts to conversation's page or independent sidebar snapshot, using org's
persisted member join sequence.

Counts take two statements per list (channels or topics), four per page
load, whatever the number of channels or topics; never one per item.
All four share the page or sidebar's read-only repeatable-read snapshot.
`TestPageCountsSnapshot` checks the four statements and snapshot consistency.

- Channel and topic counts each cap at 100 messages. Channel totals cover
  **all** topics, not only the listed ones.
- Channel counting reads at most 101 ranges to bound 100 gaps; range 101
  only bounds the last gap. Every bounded gap holds an unread message
  ([domain invariant](../domain/unread.md#read-state)).
- The sidebar lists at most 50 topics. An unlisted selected topic is counted
  separately without extending the list: at most 51 distinct topic IDs.
  Fresh snapshots replace both count maps, including zero counts, so
  displaced non-selected topics disappear and badges can clear.
- Pages compute first unread for the feed and, when selected, the topic;
  first-unread lookups are uncapped. Production counts-only sidebar recounts
  skip first unread regardless of selection, keeping the same count branches,
  caps and four statements. `TestSidebarSkipsFirstUnread` checks SQL output,
  skipped moved-candidate materialisation and count parity with a page.

Live-count approximation and recovery follow
[decision 34](../decisions/34-live-unread-counts.md) and the
[domain counts rules](../domain/unread.md#counts).
Accepted SQL expressions and parameter relations belong to the
[query gate](query-gate.md).
