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
labels, topic views with live updates (#304), posting into a topic, a
bounded topic list, the branching endpoint (#305) and its selection UI
(#308) and live feed and topic move delivery (#306) exist. `db/migrations/` is the schema source of truth.

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
  per event. A move reads its IDs in one shared snapshot and replaces loaded
  items by stable ID, correcting labels and checkbox sources. It neither
  inserts unloaded history nor changes the paging boundary.
- **Topic view** — one topic's messages, paged by `event_seq` the same way
  as channel history ([`messages.md`](messages.md#older-pages)). Its plain
  composer posts into that topic and redirects back; invalid bodies preserve
  the draft (422). Its latest page updates live through a stream of that
  topic only ([streaming](../architecture/streaming.md)); older pages do not.
  Moves remove loaded source items and insert destination items by `event_seq`,
  only at or above the loaded range's oldest sequence. The bound is zero when
  no older history remains, including an empty page, admitting every moved item.
  The history control preserves that bound across live inserts and removals;
  only Load older replaces it. Older moved items wait for that history read.
  Moves received during the read are reapplied after its items and bound swap,
  so a snapshot taken before the move cannot restore source items or omit
  destination items now in range. Retained payloads are discarded after the
  swap, or when the request ends without one.
  Replays and duplicates use stable IDs; replay matches a reload within this
  loaded range, including labels and checkbox sources.
- The channel sidebar links to at most 50 topics: default first, then
  case-insensitive name order with ID as a tie-breaker.
- A topic in a URL is scoped by the organisation and channel in the same
  URL; a topic of another channel is 404, like a non-member.

## Branching

A member selects one or more messages in any topic of a channel, the
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

Settled in #305:

- **Who may branch:** any member, including other members' messages. In
  the MVP every member reads and writes every public channel; roles and
  private channels may narrow this later.
- **Bound:** one branch moves 1–100 distinct messages
  (`topic.MaxBranchMessages`). A new destination's name follows channel
  names; the source cannot be the destination.
- **Events:** the move takes the next sequence, then the notice the one
  after. The move is `messages.moved`, carrying
  `{"channel_id","from_topic_id","to_topic_id","message_ids":[…]}` (IDs only).
  The notice is an ordinary message by the member who branched, in their
  language, logged as `message.posted`. New posting events, notices included,
  record `topic_id` in the same transaction. This is the topic at posting
  time, never rewritten by a later branch; subscription interest uses it
  before rendering. Old events without it use the shared-render topic.
- **Endpoint:** `POST …/channels/{channelID}/branch` with `message`
  (repeated), `from`, and `to` or `name`. Success is 303 to the destination's
  topic view; a stale selection is 409, an unusable request 422, and an
  unknown or out-of-scope topic 404. The UI pairs each `message` value with
  its expected source UUID (`message/source`); the handler derives `from`
  and rejects mixed sources before calling the use case. Legacy UUID-only
  values with explicit `from` still work. The event reader decodes the move's
  routing and message IDs and validates its payload. The feed delivers moves
  through the render cache shared with topic pages.
- **Selection:** feed and topic views have ordinary branch forms; JavaScript
  disables other sources and the source destination after selection. The
  destination list uses the existing bounded topic list. Plain errors render
  the conversation again (including its topic and paging bound); enhanced
  errors replace only feedback. Success navigates to the destination topic
  (303 plain, `HX-Redirect` enhanced). Live replacements clear the replaced
  checkbox's selection and refresh single-source constraints, including on
  duplicate replay; other items keep their selections.

## Unread

Topics change no unread rule by themselves; see
[`unread.md`](unread.md#topics) for the two tests an unread design must
pass once topics exist.

## Settled later

Each in the issue that implements it: renaming and merging topics;
following and muting a topic; moving messages to another channel.
