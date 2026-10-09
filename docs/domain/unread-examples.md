# Unread examples

Worked examples of the [unread rules](unread.md) under
[decision 32](../decisions/32-read-state-is-a-set-of-read-ranges-per-member-and-channel.md).
They follow one member, who joined at sequence 5, in one channel with
topics A, B and C. Sequences are the organisation's, so other channels'
events fill the numbers between. `R` is the member's read set; `[x,y)`
includes `x` and excludes `y`, and `(,y)` starts at the beginning.

The channel holds a1 = 10 (A), b1 = 11 (B), a2 = 12 (A), b2 = 13 (B),
a3 = 14 (A) and c1 = 16 (C). No row exists yet, so `R = (,6)`: everything
is unread, and the channel count is 6.

## Reading one topic (topic test 2)

The member opens A's topic view; its snapshot cursor is 18. The POST adds
A's messages up to 18, each from just after the previous channel message
to just before the next: a1 adds `(,11)`, a2 `[12,13)`, a3 `[14,16)`.

`R = (,11) ∪ [12,13) ∪ [14,16)`. The gaps `[11,12)`, `[13,14)` and
`[16,∞)` start at b1, b2 and c1, which stay unread. Counts: A 0, B 2, C 1,
channel 3. A's floor becomes 18.

## Branching (topic test 1)

Another member moves a2 and a3 from A to a new topic D (the move is 20)
and the notice n1 = 21 is posted in A. `R` does not change: a2 and a3 are
still read, now in D, so D counts 0. A counts 1 (the notice). The channel
counts 4: b1, b2, c1 and n1. `moved_event_seq` of a2 and a3 becomes 20.

## Moving unread messages into a topic read further

The member reads B's view at cursor 24: b1 and b2 join `R`, and B's floor
becomes 24. Then c1 (16, unread) moves from C to B; the move is 25. B's
floor is above 16, but c1's `moved_event_seq` (25) is above the floor, so
the count still looks it up: 16 is not in `R`, and B counts 1. Read state
never depended on the floor.

## Moving a message more than once

c1 then moves from B to D (the move is 27): still not in `R`, so D counts
1 and B 0. a2 moves from D to B (29): it is in `R`, so it stays read in B.
However often a message moves, its read state is whatever `R` says about
its sequence.

## Moving read and unread messages together

The channel gets a4 = 30 (A) and a5 = 31 (A), both unread; a1 (read) is
still in A. One move takes a1 and a4 from A to a new topic E (the move is
33, the notice n2 = 34 in A). E counts 1: a4 stays unread and a1 stays
read. A counts 3: a5 and the notices n1 and n2.

## Two tabs at once

Tab 1 sends the topic view's POST for A at 40 while tab 2 sends the feed's
POST at 38. Each is a union under the row's lock, so the result is the same
in either order; a delayed older POST, say the feed at 30, adds nothing
already read and removes nothing. The read state never moves backwards.

## A message that arrives after the snapshot

The feed renders with cursor 40, and b3 = 41 is committed before the
page's POST arrives. The POST covers sequences up to 40, so b3 stays
unread, and its live delivery shows it on the page. When the visible page
has received it (#710), the next POST with cursor 41 reads it. In a topic
view, a message moved in by a move at 42 is not read by a POST at 40 either,
even though its own sequence is lower.

## The divider

Suppose the member has read the feed up to 40, so b3 (41) is the first
unread message when the feed renders; the page carries 41 and draws the
divider above b3. Its POST then reads b3, but the
divider stays for this visit, including on pages loaded with *Load older*.
On the next page load, nothing is unread, so there is no divider.
