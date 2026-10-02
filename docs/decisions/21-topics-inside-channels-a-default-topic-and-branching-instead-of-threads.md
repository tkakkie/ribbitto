# 21. Topics inside channels, a default topic, and branching instead of threads

**Decided:** every channel message is in exactly one topic. Every channel
has exactly one default topic (UI label *chorus*, #266), created with the
channel, never deleted or replaced, and without a user-defined name; a
message posted without a topic goes there, so nobody has to name a topic to
start talking. A channel opens on the **feed** — all its topics interleaved
by time, each message labelled with its topic — and the **topic view** shows
one topic. **Branching** moves selected messages to another topic of the
same channel, new or existing, in one transaction, and posts a branch notice
in the source topic. Moved messages keep their `id` and original
`event_seq`; the move and the notice are new durable events, so live clients
replay them in order (5). If a selected message is no longer where the
request expected, nothing changes (409). Topics come before M4, whose unread
counts and sidebar build on them. Rules:
[`docs/domain/topics.md`](../domain/topics.md).
**Why:** parallel conversations in one channel should not tangle, and
people start talking before they know a conversation deserves its own
place; "talk first, branch later" keeps both (#274).
**Considered:** Slack-style threads (a second place to post and a second
unread model, and replies leave the stream); copying messages into the new
topic (two copies diverge on edit or delete); one message in several topics
(visibility, unread and delete each gain a second meaning); requiring a
topic before posting (people skip it or give up); topics after the MVP
(unread and the sidebar would be built twice).
