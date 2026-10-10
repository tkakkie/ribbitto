# Unread examples

Worked examples of the [unread rules](unread.md) under
[decision 32](../decisions/32-read-state-is-a-set-of-read-ranges-per-member-and-channel.md).
They follow one member, Mio, who joined at sequence 5, in one channel with
topics A, B and C. Another member, Kai, writes every message except Mio's
own post below and makes every move, so each move's notice is Kai's and
unread for Mio. Sequences
are the organisation's, so other channels' events fill the numbers between.
`R` is Mio's read set and `[x,y)` includes `x` and excludes `y`; `P` is the
end of its first range.

The channel holds a1 = 10 (A), b1 = 11 (B), a2 = 12 (A), b2 = 13 (B),
a3 = 14 (A) and c1 = 16 (C). Mio has no rows, so `R = [0,6)`: all six are
unread, and the channel counts 6.

## Reading one topic (topic test 2)

Mio opens A's topic view with snapshot cursor 18. The POST reads A's
messages up to 18, each from just after the channel's previous message to
just before its next: a1 adds `[0,11)`, a2 `[12,13)`, a3 `[14,16)`.

`R = [0,11) ∪ [12,13) ∪ [14,16)`, so `P = 11`. The gaps start at b1, b2 and
c1, which stay unread. Counts: A 0, B 2, C 1, channel 3. A's floor is 18.

## Branching (topic test 1)

Kai moves a2 and a3 from A to a new topic D (the move is 20) and the notice
n1 = 21 goes into A. `R` does not change: a2 and a3 are still read, now in
D. Counts: A 1 (n1), B 2, C 1, D 0, channel 4.

## Moving an unread message into a topic read further

Mio reads B's view at cursor 24. B's scan starts at `P = 11`, so it finds
b1 (11) and b2 (13); they add `[11,12)` and `[13,14)`, and `R` merges into
`[0,16)`. B's floor is 24.

Kai moves c1 (16, unread) from C to B (the move is 25; notice n2 = 26 in
C). B's floor is above 16, but c1's `moved_event_seq` 25 is above the
floor, so B's count looks it up: 16 is not in `R`. Counts: A 1 (n1), B 1
(c1), C 1 (n2), channel 3.

## Moving a message more than once

Kai moves c1 from B to D (27; notice n3 = 28 in B), then a2 from D to B
(29; notice n4 = 30 in D). c1 is still not in `R`, so it is unread in D;
a2 is in `R`, so it is read in B. Counts: A 1 (n1), B 1 (n3), C 1 (n2),
D 2 (c1, n4), channel 5.

## Moving read and unread messages together

Kai posts a4 = 31 and a5 = 32 in A, then moves a1 (read) and a4 (unread)
from A to a new topic E (33; notice n5 = 34 in A). E counts 1: a4 stays
unread and a1 stays read. A counts 3 (n1, a5, n5); the channel 8.

## Reading the feed

Mio opens the feed with cursor 36. Its POST adds `[0,37)` (no channel
message lies above 34 yet), so every message so far is read and all counts
are 0. The floors no longer matter: counting starts at `P = 37`.

## Two tabs at once

Kai posts b3 = 38 and a6 = 40. Tab 1 sends A's view POST at 40 while tab 2
sends the feed's POST at 38. Each takes the channel's lock row in turn:
the feed adds `[0,40)` (a6 is the next message) and A's view adds
`[39,41)` for a6 (b3 is the previous one), so either order ends with
`R = [0,41)`. A delayed feed POST at 30 adds nothing and removes
nothing: the read state never moves backwards.

## A message that arrives after the snapshot

The feed renders with cursor 41, and Kai's b4 = 42 commits before the
page's POST arrives. The POST covers sequences up to 41, so b4 stays unread
until the visible page has received it (#710) and posts with cursor 42. In
a topic view, a message moved in by a move after the view's cursor is not
read by its POST either, even though its own sequence is lower.

## Mio posts

Before the feed sends a POST covering 42, Mio switches to A's view, which
has applied up to 42, and posts m1 = 43 in A. The posting reads A
up to 42, adds `[43,44)` for m1 (b4 at 42 is the channel's previous
message, so the gap at 42 stays), and raises A's floor to 43, since no
message of A lies between 42 and 43. m1 is never unread for Mio; b4 stays unread in B.

## A reply chain

Mio opens the reply chain of b2 in a panel. No POST is sent: `R`, the
floors and every count stay as they were.

## The divider

Suppose Mio has read the feed up to 50 and Kai's b5 = 51 is the first
unread message when the feed renders. The page carries 51 and draws the
divider above b5. Its POST then reads b5, but the divider stays for this
visit, including on pages loaded with *Load older*. On the next page load
nothing is unread, so there is no divider.
