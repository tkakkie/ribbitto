# 30. One organisation-wide stream per tab, filtered by its interests

**Decided** (#296; supersedes nothing; [decision 31](31-presence-and-typing-are-current-state-with-a-generation.md)
settles the ephemeral signals it carries). *Planned* for M4; details in
[streaming](../architecture/streaming.md#stream-scope-planned-m4).

- **One stream per tab.** Every page of an organisation holds one
  Server-Sent Events stream, `GET /organizations/{slug}/events`: the feed,
  a topic view, the members panel and a directly opened `?before=` page.
  Older history loaded into a page by htmx keeps that page's stream. It
  replaces M3's per-channel and per-topic streams.
- **Interests.** The connection declares what its page shows: the sidebar
  (every page with one), messages and typing for one channel or one topic
  of it, and presence (only views that show it: the members panel in M4).
  The URL carries them; the organisation and account still come from the
  URL and the session.
- **A pre-filter, never authorization.** Delivery filters each event and
  signal by the connection's interests before rendering, so nothing goes to
  every connection unconditionally. Declaring an interest grants nothing:
  the per-connection check (`org.Authorizer.MayReceive`, #670) still runs
  after rendering and immediately before every send, durable or ephemeral.
  A channel or topic the member cannot see is still 404 before anything is
  sent.
- **Cost.** An interest adds no database query per post per connection: a
  post's work per connection stays a shared render, a cached allow and a
  write ([stream cost](../architecture/stream-cost.md)). Output that differs
  per member, such as unread counts, is recomputed from the database only
  on coalesced triggers (page load, a read advance, at most one recount per
  connection per interval), never once per post per connection; its issue
  (#286) measures it with `TestStreamCost` and distinct members.
- **The sidebar is current state.** Its frames are recounts, sent whole at
  every connect and after coalesced triggers, and carry no `id:`: only
  `message` and `messages-moved` frames move `Last-Event-ID`, as today. A
  recount lost to a disconnect is redone at the reconnect, and a recount
  never skips the browser past a message it has not received.
- **Caps.** A tab still holds one stream, so the per-account cap (16) and
  [decision 25](25-production-serves-streams-over-http2.md)'s reason stand;
  pages that held none (older history, the members panel) now hold one,
  which the process cap (5,000) counts.
- **Growth.** The interests are the keys of #232's
  [channel interest index](../architecture/shared-reader-growth.md#a-channel-interest-index).
  M4 still wakes every connection of the organisation and filters after
  waking, as M3 does; the index, which wakes only the connections a change
  touches, is built when a measurement calls for it.

**Why:** M4's signals differ in scope: the sidebar, unread counts and
presence are organisation-wide, while typing and messages belong to one
channel or topic. With M3's streams on the latest channel and topic pages
only, sidebar updates and presence would reach no other page, and a second
stream per tab would double what the caps count. Separating the transport
(the organisation) from the delivery interest (the page) gives each tab one
connection carrying what its page shows (maintainer's direction,
2026-10-02).

**Considered:** a second, organisation-wide stream beside each page's
stream (two connections and two cursors per tab); streams on latest pages
only (the sidebar and presence go stale elsewhere); sending every event to
every connection and filtering in the browser (authorization must decide on
the server); sharing one stream between tabs in the browser (decision 25's
deferred option, more client code).
