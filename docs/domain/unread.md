# Unread rules

The inputs are in place since M3: the join transaction, `joined_event_seq`,
the pairing of each message with its `message.posted` event, and the log
boundary. Read-state tables, locking, range unions, feed, topic and branch-notice
reads exist;
Initial reading POSTs and posting writes are current; unread counts are *planned* for M4
([decision 32](../decisions/32-read-state-is-a-set-of-read-ranges-per-member-and-channel.md));
the counting queries and their cost are in
[unread counts](../architecture/unread-counts.md), the writes in
[unread writes](../architecture/unread-writes.md), the worked examples in
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
  answer.

## Advancing it

Branch-notice reads, initial reading POSTs and posting writes are current. Only a POST advances read state; a GET never changes it
([request flow](../architecture/request-flow.md)). Each reading POST carries its
scope and `S`, the newest durable sequence the page has applied. `S` bounds the
scope, not a list of rendered messages: messages of the scope outside the loaded
window, older history included, are read with it.

- **Feed:** every message of the channel with `event_seq ≤ S` becomes read.
- **Topic view:** every message of that topic with `event_seq ≤ S` becomes
  read, except one moved in by a move after `S` (`moved_event_seq > S`): the
  page's cursor had not reached that move. A message moved in below the
  loaded window is read by the next topic read whose `S` covers the move,
  like the older history an initial reading POST reads (decided 2026-10-10,
  #775). Other topics are untouched.

Posting reads the composer's scope up to the page's `S`, as that POST
would, and adds the new message, in the posting transaction: a member's own
messages are never unread for them. A post from a `?before=` page carries no
cursor and reads only the author's own message, leaving every other message's
read state and topic floors unchanged. Branching already adds its author's notice
as `[p + 1, m + 1)` in its transaction, merged with the persisted join prefix;
the organisation lock keeps the notice newest in its channel. Moving existing
messages changes no member's read state. A sequence
the page has only received, or the post's own response, never counts as
applied.

`S` is the page's snapshot cursor for the POST sent after the page loads,
and later the newest durable sequence the page has applied
(#710), which covers both posts and moves. Hidden tabs and `?before=` pages send no
reading POST.
A cursor above the organisation's committed `event_seq` is refused, so a
later message is never read in advance. A message that is still unread
when the POST runs, but has moved out of the topic since the page showed
it, stays unread: it is no longer there to mark. A message already read
stays read whatever moves. Without JavaScript, nothing advances on load; the feed and topic view
offer a *Mark as read* form that sends the same POST with the page's cursor.

## Counts *(planned, M4)*

A channel's unread count is the number of its unread messages, which is the
sum over **all** its topics, not only the 50 the sidebar lists. A topic's
count is the channel's unread messages in that topic. Counts are shown
capped (`99+`). The cap limits how many unread messages a count returns,
not all the work: a topic count can also read the member's own posts made
while the stream lagged, and the first unread of a topic reads every message
moved in since its floor ([unread counts](../architecture/unread-counts.md)).

## The unread divider *(planned, M4)*

The page computes, in its snapshot and before its own POST, the first
unread message of its scope (the channel for the feed, the topic for a topic
view) and carries its sequence. The divider sits above that message when it
is on the loaded page, and *Load older* passes the sequence along, so the
divider stays where it was for the whole visit while the visit's POSTs
advance the read state. The next page load computes it afresh (#291). Live
items never carry it: their renders are shared between members.

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
