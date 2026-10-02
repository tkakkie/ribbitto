# Topics

How messages inside a channel are grouped into topics, what the default
topic is, and how messages move between topics. Read this before
designing or changing topics, the channel page, branching, or anything
that counts messages per topic. It was settled in
[decision 21](../decisions/21-topics-inside-channels-a-default-topic-and-branching-instead-of-threads.md) (#274).

**Keep it current:** update this file in the same pull request whenever
these rules change. The *Model* and *Tables* below exist (#301, #307):
every channel has its default topic and every message a topic, and posting
goes to the default topic, and a topic view posts into its topic. Feed
labels, topic views with live updates (#304), posting into a topic and a
bounded topic list exist; branching (#305) follows. `db/migrations/` is the
schema source of truth.

## Model

- Every channel message is in **exactly one topic**, a named conversation
  inside its channel.
- Every channel has **exactly one default topic** (UI label *chorus*,
  [`vocabulary.md`](vocabulary.md)). It
  is created in the same transaction as its channel, is never deleted, and
  no other topic can become the default. It has no user-defined name: the
  UI shows its label from the message files. A message posted without a
  topic goes there.
- The default topic is not the default channel
  ([`channels.md`](channels.md#the-default-channel)): every channel has a
  default topic, while an organisation has one default channel.

## Tables

| Entity | Key columns |
|---|---|
| `topic` | `id` (UUIDv7), `organization_id`, `channel_id`, `name` (NULL only for the default topic), `is_default`, `created_at` |
| `channel` | adds `default_topic_id`, not null, and `default_topic_is_default`, always true |
| `message` | adds `topic_id`, not null |

- **Names** are unique per channel, ignoring case, among named topics: a unique
  index on `(organization_id, channel_id, lower(name))`. Validation of the
  name follows channel names ([`validation.md`](validation.md)) unless the
  implementing issue says otherwise.
- **One default topic per channel, enforced by the database.** `is_default`
  is true exactly when `name` is NULL (a `CHECK`), and a partial unique
  index on `(organization_id, channel_id) WHERE is_default` allows one per
  channel — a plain unique index would not, since NULL names never collide.
- **Same channel, enforced by the database.** `topic` has a unique key on
  `(organization_id, channel_id, id)`, the target composite foreign keys need
  (as `channel (organization_id, id)` is for messages today).
  `message (organization_id, channel_id, topic_id)` references it, so a
  message can never be in another channel's topic. `channel
  (organization_id, id, default_topic_id, default_topic_is_default)`
  references a second unique key, `topic (organization_id, channel_id, id,
  is_default)`, where `default_topic_is_default` is a column fixed to true by
  a `CHECK`: so the pointer can only name the channel's own default topic,
  never a named one. The foreign key from `channel` also makes the default
  topic undeletable. Channel and topic refer to each other, so one of the
  two keys is deferred to commit.
- **Migration:** every existing channel gets a default topic, and every
  existing message moves into its channel's default topic, in the same
  migration that makes `topic_id` and `default_topic_id` not null.

## Views

- **Feed** — the channel page: every topic's messages interleaved by
  `event_seq`, each labelled with its topic. It is what the channel page
  shows today. Labels are escaped and isolated with `<bdi>` and link to the
  topic view; the default label comes from both language catalogues. Topic names
  are resolved through `topic.Directory` in one batch per page, in the same
  snapshot as the messages and authors. Live labels come from the shared
  `MessageReader.One` load through the existing render cache, keyed by
  organisation, channel, sequence and language, with no extra read per stream
  per event. A moved message's label is corrected by #306's move event.
- **Topic view** — one topic's messages, paged by `event_seq` the same way
  as channel history ([`messages.md`](messages.md#older-pages)). Its plain
  composer posts into that topic and redirects back; invalid bodies preserve
  the draft (422). Its latest page updates live through a stream of that
  topic only ([streaming](../architecture/streaming.md)); older pages do not.
- The channel sidebar links to at most 50 topics: default first, then
  case-insensitive name order with ID as a tie-breaker.
- A topic in a URL is scoped by the organisation and channel in the same
  URL; a topic of another channel is 404, like a non-member.

## Branching

*Planned (#305).* A member selects one or more messages in any topic of a channel, the
default topic included, and moves them to another topic of the same
channel, a new one or an existing one. In **one transaction**:

1. the destination topic is created if it is new;
2. the selected messages move into it, keeping their `id` and their
   original `event_seq`;
3. a **branch notice** is posted in the source topic, naming the
   destination and the number of messages moved. It is an ordinary new
   message, so it appears at the time of branching;
4. the move and the notice are recorded as new durable events
   ([decision 5](../decisions/05-one-event-sequence-per-organisation.md)), so live clients see them in
   order and a reconnecting client replays them.

If any selected message is no longer in the topic the request expected —
someone else branched it first — the whole operation fails with **409**
and nothing changes. A message is never in two topics and never copied.

Because moved messages keep their `event_seq`, the feed shows them where
they were, now labelled with the destination topic; the destination's
topic view shows them in their original order among its own messages.

## Unread

Topics change no unread rule by themselves; see
[`unread.md`](unread.md#topics) for the two tests an unread design must
pass once topics exist.

## Settled later

Each in the issue that implements it: who may branch other members'
messages; renaming and merging topics; following and muting a topic;
moving messages to another channel; the event kinds and payloads of a
move and a branch notice.
