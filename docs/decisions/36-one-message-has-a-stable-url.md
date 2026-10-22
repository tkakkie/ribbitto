# 36. One message has a stable URL that resolves to its current place

**Decided** (#1150, approved by the maintainer on 2026-10-16). The
`?around=` page (#1182), the route and the highlight (#1170) are
implemented; no page links to a message yet. One message is reached at
`GET /organizations/{slug}/messages/{message-id}`, where `message-id` is the
message's UUID in its canonical text form. The URL names no channel or topic:
the server finds where the message is now and answers `303 See Other` to that
conversation. Pins (#889), "Copy link" (#1159) and quoting (#1160, #1161) use
this URL, and later the "for me" view (#433), jumping to the first unread
message and search results.

**Why:** a link must keep working while the message stays readable.
Branching moves messages between topics and keeps their identity
([decision 21](21-topics-inside-channels-a-default-topic-and-branching-instead-of-threads.md)),
so a topic in the URL would go stale. The UUID is the message's identity;
`event_seq` is unique per organisation
([decision 5](05-one-event-sequence-per-organisation.md)) but orders events,
so it stays out of the URL. The URL is built from the slug and the message ID
alone, so a list of links (the pins overview) needs no lookup per entry, and
it holds nothing member-specific, so a shared render can contain it.

## Resolution

1. Membership comes from the slug, as on every organisation route: a
   non-member or signed-out request gets 404.
2. `conversation` finds the message's **current** channel, topic and
   `event_seq`. An unknown ID, a message of another organisation and one in
   a channel the member may not read answer the same 404, so the URL never
   reveals whether a message exists.
3. Once messages can move to another channel, a moved message resolves to
   its destination when the member may read the destination, and is 404 when
   they may not. Once messages can be deleted, a deleted message is 404.
   A dedicated "no longer available" page is a separate, deferred decision
   for the first feature that deletes or moves messages between channels;
   [replay's *Vanished messages*](../architecture/replay.md#vanished-messages)
   covers only how the stream skips events whose message is gone.

## Where it lands

The 303 goes to the ordinary conversation URL of the message's current
topic, with the fragment `#message-<hex>` (the element ID the message list
already uses); the page scrolls to that message and highlights it instead of
scrolling to the newest entry.

- **On the topic's latest page:** the plain topic URL. It is a latest page,
  live and reading as today.
- **Older:** `?around=<event_seq>`, a page that contains the message with
  context on both sides. **An `around` page is an older page:** like a
  `?before=` page its stream wants only `sidebar`, so no live messages or
  typing are appended across the gap; it sends **no reading POST**, a post
  from it carries no cursor, and it links to the latest page. Opening an old
  message therefore never marks newer, undisplayed messages read.

A reply opens in the stream like any other message
([decision 22](22-replies-stay-in-the-stream-with-a-reply-chain-panel.md));
the link does not open the reply-chain panel. A GET never changes read
state, and a link grants no read scope
([unread](../domain/unread-reading.md#advancing-it)). A permalink pasted into a
message is plain text; how a future message format renders it belongs to
that format's decision
([decision 18](18-stored-message-formats-never-change-meaning.md)):
[decision 39](39-message-bodies-use-a-small-frozen-markup.md) makes it an ordinary link (rendered since #1202; posted as `chat_v1` since #1203).

**Considered:** `/channels/{channel-id}/messages/{message-id}` (readable, but
stale after a move to another channel and repeating what the message row
holds); `event_seq` as the identifier (short, but event order, not
identity); rendering the conversation at the message URL itself (one request
fewer, but a second URL for the same page complicates stream interests,
history and the sidebar's selected row); reusing `?before=` with a computed
bound (one paging mode, but the message lands at the page's edge with no
context after it).
