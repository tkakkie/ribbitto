# Replies *(planned)*

How an answer links to a message and how its conversation can be read on
its own. Read this before designing or changing replies or the reply-chain
view. It was settled in [decision 22](../decisions/22-replies-stay-in-the-stream-with-a-reply-chain-panel.md) (#275);
implementation follows [topics](topics.md) and belongs to the `conversation` module.

**Keep it current:** update this file in the same pull request whenever
these rules change. Nothing here is implemented yet; `db/migrations/` is
the source of truth for what exists.

## Model

- A **reply** is an ordinary message with a link to the message it answers.
  It stays in the stream in `event_seq` order, with a one-line preview of
  that message. The preview comes from the referenced message, not a copy
  of its text.
- A reply's topic defaults to the topic of the message it answers, but the
  author may choose another topic in the same channel.
- There is no separate place to post and no separate unread count.

## Field and invariants

| Entity | Planned field |
|---|---|
| `message` | adds nullable `reply_to_message_id`; NULL means it is not a reply |

- **Same organisation and channel, enforced by the database.**
  `message (organization_id, channel_id, reply_to_message_id)` references
  `message (organization_id, channel_id, id)`, with a unique key on the
  latter columns for the composite foreign key. A reply can never link to
  a message in a channel its reader cannot read.
- **No cycles.** The answered message already exists when the reply is
  posted and has a lower `event_seq`. The reference never changes after
  posting. Branching preserves `event_seq`, so it cannot break this rule.
- **Normal channel authorization comes first.** Reading the chain is
  scoped by the organisation and channel in the URL. A member who cannot
  read the channel gets **404**; no chain lookup bypasses that check.
- **Links survive branching.** Moving messages to another topic keeps
  their ids, so a reply still links to its original even if their topics
  differ. Once deletion exists, a reply whose original has been deleted
  shows a placeholder for it.

## Reply-chain view

Selecting the preview or the reply opens the **reply chain** in a side
panel, or a full-screen sheet on narrow screens. The chain contains the
path from the first message (one with no reply reference) to the selected
message, plus every reply below the selected message, including indirect
replies. Other branches off its ancestors are not included. Messages form
one list in `event_seq` order, each labelled with its topic.

The chain is paged like [channel history](messages.md#older-pages): it
opens at the selected message, shows a bounded page, and offers links to
earlier and later parts of the chain. Depth and branching never require
the whole chain in one response; paging reaches messages beyond the first
page. Exact limits are left to implementation. Without JavaScript the
chain is an ordinary page and the same links work; URLs follow
[decision 17](../decisions/17-full-english-words-and-stable-identifiers-in-page-urls.md) and the enhancement rule in [decision 19](../decisions/19-web-layers-server-owned-html-htmx-swaps-javascript-as-enhancement.md).

Opening or paging through the chain leaves read state unchanged
([`unread.md`](unread.md#replies)).

## Settled later

Branching a reply chain into a topic in one action needs its own issue:
a chain may span topics and pages, so the moved set, a notice per source
topic and the conflict rule must be decided together. Ordinary branching
still follows [topics](topics.md#branching).

Cross-channel replies are out of scope. Reply notifications belong with
mentions (#117). Editing and deleting messages are separate work; this
decision specifies only the placeholder once deletion exists.
