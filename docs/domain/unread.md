# Unread rules

The inputs are in place since M3: the join transaction, `joined_event_seq`,
the pairing of each message with its `message.posted` event, and the log
boundary. Read-state tables, locking, range unions, feed, topic and branch-notice
reads exist;
Visible-page reading POSTs, posting writes and snapshot-bound sidebar counts are current
([decision 32](../decisions/32-read-state-is-a-set-of-read-ranges-per-member-and-channel.md));
the counting queries are in
[unread counts](../architecture/unread-counts.md), their cost in
[unread count costs](../architecture/unread-count-costs.md), the writes in
[unread writes](../architecture/unread-writes.md), the reading scopes and posting in
[unread reading](unread-reading.md), the worked examples in
[unread examples](unread-examples.md).

- *(since M3)* **Joining an organisation is one transaction:** take the next
  `event_seq`, insert the `member` with `joined_event_seq` = that value, and
  insert the `member.joined` event with `data = {"member_id":"<uuid>"}` and
  `audience_member_id = NULL`. Setup's first member follows the same rule.
  Posting similarly pairs `message.event_seq` with `message.posted`.
  Logging starts after `organization.event_log_boundary_seq`; migration sets
  it to the current counter without backfill. Read state survives log
  retention: it relies on `message.event_seq`, never on `event_log`.

## Read state

Storage, range unions, message-derived feed bounds, topic-floor writes and
branch-notice reads are current.

- **Read ranges.** A member's read state in a channel is a set of
  `event_seq` ranges (`read_range` rows, `lo ≤ event_seq < hi`). A message
  of the channel is read when its `event_seq` lies in one of them. Without
  rows the set is `[0, joined_event_seq + 1)`, so messages from before a
  member joined are never unread.
- **Moves never change it.** The set names sequences, not topics, and a
  move keeps `event_seq`, so a branch or any later move leaves every
  message read or unread as it was, whatever the destination topic's state
  ([topic tests](#topics)).
- **It only grows.** Every write is a union, so several tabs and reordered
  requests never make a read message unread.
- **Every bounded gap holds an unread message.** A newly read message adds
  the range from just after the channel's previous message to just before
  its next one (or to its own sequence plus one when it is the newest).
  Runs of read messages merge, and each gap between two ranges contains an
  unread message of the channel, so there are never more such gaps than
  unread messages. The open gap after the last range may hold none.
- **Topic floors.** `topic_read_floor.floor_seq` records that every message
  in that topic up to and including it is read, except messages moved in by
  a move with a higher sequence (`message.moved_event_seq`). It only tells
  counting where to start; whether a message is read is always the set's
  answer. A run raises it at most to the run's end, never to or past an
  unread topic message below the run, including one moved in after the floor.

The reading scopes and posting are in [unread reading](unread-reading.md).

## Counts

**Current (#1039 PR 3, [decision 34](../decisions/34-live-unread-counts.md)):**
live counts are eventually exact: exact at page load and every successful
resync, approximate in between, and always exact again after the next successful
resync. Small `unread` frames and browser increments replace all-connection
post recounts; own posts, reads, resyncs and current-channel moves still recount.
Read state and its writes are unchanged. Without JavaScript, counts are exact
at page load and change only on the next page load.

Intended approximations: a `message` frame never raises a count, so a hidden
tab, or a shown scope not yet read, does not rise while messages arrive;
the next resync or read makes it exact. Legacy events and channels or topics
created after the snapshot wait for the next resync. Only `unread` frames for
another member's post newer than the snapshot can increment; own posts and
duplicates never do. Recovery stops increments until a successful resync
recovers any losses ([recovery contract](../decisions/34-live-unread-counts.md#resync-and-recovery)).

A channel's unread count is the number of its unread messages, which is the
sum over **all** its topics, not only the 50 the sidebar lists. A topic's
count is the channel's unread messages in that topic. Counts are shown
as 1–99 or capped (`99+`); zero shows nothing. Feed, topic, older history
and members pages show them on the sidebar links, with semibold labels and
a count badge. An unlisted selected topic shows its count beside the heading,
keeping the sidebar at most 50 topic links. Every topic heading keeps a count
target, including while listed or at zero; fresh sidebar lists and that target
share page markup for recounts. A listed selection leaves the heading target empty. The cap limits how many unread messages a count returns,
not all the work: a topic count also reads the member's own posts above its
floor until a later topic read raises it; a first-unread lookup can scan topic
history ([unread count costs](../architecture/unread-count-costs.md)).

## The unread divider

The page computes, in its snapshot and before its own POST, the first
unread message of its scope (the channel for the feed, the topic for a topic
view) and carries its sequence. The divider sits above that message when it
is on the loaded page, and *Load older* passes the sequence along, so the
divider stays where it was for the whole visit while the visit's POSTs
advance the read state. The next page load computes it afresh. A direct
older-history URL without a carried sequence also uses its snapshot. Zero
is carried explicitly when nothing was unread. Live items never carry it:
their renders are shared between members.

## Topics

Any unread design must pass both tests:

1. **Branching never changes whether a message is read.** Read ranges pass:
   the set names sequences, and moves keep them.
2. **Reading one topic never marks another topic's unseen messages read.**
   Topic A has messages 10 and 12, topic B has 11; reading A in the topic
   view leaves 11 unread. Read ranges pass: the topic view adds only A's
   messages, leaving a gap at 11. A single per-channel position fails this,
   and a per-topic position alone fails test 1.

## Replies

Opening or paging through a [reply chain](replies.md) sends no POST and
leaves read state unchanged. A chain omits other messages in the channel,
so treating it as a read would mark unseen messages read. Replies are
ordinary messages under the same unread rules, with no separate unread
count.
