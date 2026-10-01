# Product vocabulary

Code uses the neutral terms in the [glossary](README.md#glossary). In the
application, frog-themed product words appear only in UI message files,
never in identifiers, table names or URL segments.

**Keep it current:** update this file in the same pull request whenever a
product label or vocabulary rule changes; keep domain terms in the
glossary current too. The decision is recorded in
[`DECISIONS.md`](../../DECISIONS.md#20-product-labels-and-ordinary-words).

## Product labels

| Identifier | UI label | Status | Meaning |
|---|---|---|---|
| `organization` | marsh | exists | A workspace that owns what its members create. |
| `section` | pond | planned | A group of channels in the sidebar. |
| `channel` | lilypad | exists | A named conversation inside an organisation. |
| `topic` | ripple | planned | A conversation topic within a channel. |
| default topic of a channel | chorus | planned | A channel's default topic, with behaviour still to be decided. |
| `message` | ribbit | exists | A post in a channel, written by a member. |

Status describes the concept, not whether the UI uses its label yet.
Today's UI still says "channel" and "message"; applying these labels is a
separate change, best done with the design pass. The *planned* rows reserve
labels only. Whether channels get topics, how the default topic behaves,
moving messages between topics and a reply view belong to a separate
conversation-model issue and decision.

## Ordinary words

Everything else uses ordinary words, localised normally. Identity, roles,
visibility, permissions, notifications and everyday operations need clear
words: a misread word there becomes a permission or notification mistake.

Anything people say aloud as an action also uses an ordinary word: "DM me",
"mention her", "react to it". A themed word that is awkward to say goes
unused. Direct conversations are **direct message** / **DM**, mentions are
**mention** / **メンション**, and reactions are **reaction** / **リアクション**.
They do not get themed labels such as *peep*, *croak* or *hop*.

## Japanese UI

Product labels use **the same Latin-script word as in English**: `lilypad`,
not 蓮の葉 or リリーパッド. Keeping the spelling makes the labels work as names;
ordinary words are still localised normally.
