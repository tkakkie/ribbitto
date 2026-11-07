# Unread counts

[Read state](../domain/unread.md) follows
[decision 32](../decisions/32-read-state-is-a-set-of-read-ranges-per-member-and-channel.md);
see [costs and benchmarks](unread-count-costs.md) and [writes](unread-writes.md).

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
`P` (effective floor `P − 1`); the moved branch remains, excluding
prefix-read messages.

## Counting contract

`unread` never reads `message`, and `conversation` never reads `unread`'s
tables. `compose.PageCountsIn` binds counts to the page or sidebar snapshot
with org's persisted join sequence ([cross-feature access](cross-feature-access.md)).
Production's `cmd/ribbitto` sidebar source binds resolved membership, channel
and selected topic; an open panel keeps the conversation's selection (#874).

Counts take two statements per list (channels or topics), four per page
load, whatever the number of channels or topics; never one per item.
All four share the page or sidebar's read-only repeatable-read snapshot.
A sidebar snapshot takes eight statements for a feed, nine for an open selected
topic, and thirteen for a resolved selection: the latter adds message, member,
display-name and topic-label lookups. These exclude transaction control and
production's optional local timeout statement.
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
- Feed first unread uses the capped channel result; the selected topic's
  first-unread lookup is uncapped. Counts-only sidebar recounts
  skip first unread regardless of selection, keeping the same count branches,
  caps and four statements. `TestSidebarSkipsFirstUnread` checks SQL output,
  skipped moved-candidate materialisation and count parity with a page.

Live-count approximation and recovery follow
[decision 34](../decisions/34-live-unread-counts.md) and the
[domain counts rules](../domain/unread.md#counts).
Accepted SQL expressions and parameter relations belong to the
[query gate](query-gate.md).
