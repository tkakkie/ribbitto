# 27. Channels, topics and messages are one `conversation` module

**Decided** (#383, superseding [decision 26](26-modules-by-feature-layout-seams-and-order.md)'s
`channel`, `topic` and `message` modules, its graph and its order for them):

- **One module.** `conversation` owns the `channel`, `topic` and `message`
  tables. They share the invariants of
  [decision 21](21-topics-inside-channels-a-default-topic-and-branching-instead-of-threads.md),
  which the database enforces with composite and deferred foreign keys: a
  channel always has its default topic, created in the same transaction;
  every message is in a topic of its own channel.
- **Inside it,** one store, one sqlc entry and one wiring package
  `conversationpg`. A channel's default topic, branching's moves and notice,
  and the channel and topic reads of posting and of the page snapshot are
  direct calls on the same transaction or snapshot, not injected factories.
  Sub-packages may organise the code, but there are no boundaries between
  its channel, topic and message parts. Decision 26's structure still holds
  inside it: the API, store and wiring separation, who may import the store
  and the wiring, and acyclic package imports.
- **Growth rule.** A change that extends a channel, topic or message (a new
  column or state on their rows, or new behaviour on them, such as replies
  per [decision 22](22-replies-stay-in-the-stream-with-a-reply-chain-panel.md),
  editing or deleting) belongs in `conversation`. A feature that owns its own
  tables and only refers to these, such as reactions, unread or search, is
  its own module depending on `conversation`'s API. A feature that does both
  is split along that line.
- **Graph and order.** `conversation` → `identity`, `org`, `realtime`; the
  rest of decision 26's graph is unchanged. Setup's default channel,
  `org`'s sequence and members, `identity`'s authors and `realtime`'s
  operations stay injected, as before. Order: 0 kernel and platform →
  1 `identity` → 2 `realtime` → 3 `org` → 4 `conversation` (several pull
  requests) → 5 removal of the remaining layers and temporary exceptions.

**Why:** the three are one transaction boundary. Two of decision 26's five
injections against the import direction, and several temporary paths, lay
between them; one module makes those seams ordinary calls. It stays small:
at the decision, its application code is 508 lines without tests and its
PostgreSQL code 546. The growth rule keeps it from becoming the large
package decision 14 set out to avoid.

**Considered:** decision 26's three modules (correct boundaries for imports,
but wiring and temporary paths for what is one transaction); merging only
`topic` and `message` (a channel's default topic would still cross modules).
