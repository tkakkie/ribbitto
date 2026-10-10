# Domain

The words ribbitto uses, the things it stores and the rules that must always
hold. Code identifiers use the terms in the glossary; the frog-themed product
words (ribbit, pond, marsh, …) appear only in UI message files.

**Keep it current:** update the relevant file in this directory in the same
pull request whenever a term, an entity, a relation, an invariant, a
validation rule or the unread rules change: this README for terms,
[`entities.md`](entities.md) for entities and relations,
[`invariants.md`](invariants.md) for invariants, and the topic file in the
index below for everything else.

## Index

| File | Covers |
|---|---|
| `README.md` (this file) | glossary, MVP scope |
| [`entities.md`](entities.md) | entities, ER diagram, key columns |
| [`invariants.md`](invariants.md) | rules every piece of code and every migration must keep |
| [`vocabulary.md`](vocabulary.md) | product labels, ordinary words and Japanese UI vocabulary |
| [`validation.md`](validation.md) | input validation rules |
| [`unread.md`](unread.md) | unread rules: read ranges, reading scopes, counts and the divider (storage, feed, topic and branch-notice reads and the initial reading POST current; posting current; counts planned, M4) |
| [`unread-examples.md`](unread-examples.md) | worked examples of the unread rules: topic reads, moves, concurrent tabs, late messages; initial reading POSTs and posting current; counts planned, M4 |
| [`names.md`](names.md) | display names, handles, how members are shown |
| [`channels.md`](channels.md) | channel identity, names and the default channel |
| [`topics.md`](topics.md) | topics, the default topic, the feed and branching |
| [`messages.md`](messages.md) | message identity, accepted bodies and rendering |
| [`replies.md`](replies.md) | reply references and the reply-chain view (planned) |
| [`setup-and-signup.md`](../architecture/setup-and-signup.md) | first-run setup and sign-up |

## Glossary

| Term | Meaning |
|---|---|
| **account** | A person who can sign in. Global, not tied to one organisation. |
| **session** | A signed-in browser of an account. |
| **organization** | A workspace. Owns everything its members create. |
| **member** | An account's membership in one organisation. All organisation-owned data refers to members, never directly to accounts. |
| **handle** | A member's organisation-scoped name for people to tell members apart, shown as `Display name @handle`. |
| **section** *(planned)* | A group of channels in the sidebar; only its label is reserved so far. |
| **channel** | A named conversation inside an organisation. |
| **read state** | A member's read ranges in a channel: which of its messages they have read ([`unread.md`](unread.md)). |
| **message** | A post in a channel, written by a member. |
| **reply** *(planned)* | A message linked to an earlier message it answers in the same channel. |
| **reply chain** *(planned)* | A selected message's ancestor path and all its descendants, in `event_seq` order. |
| **topic** | A named conversation inside a channel; every channel message is in exactly one. |
| **default topic** | The one topic every channel has, where a message goes when no topic is chosen. Not the default channel. |
| **feed** | A channel's messages from all its topics, interleaved by time and labelled with their topic. |
| **branching** | Moving selected messages to another topic of the same channel, keeping their ids and sequences. |
| **event** | A durable change that live clients must see (a message posted, a member joined), recorded in `event_log`. |
| **event sequence** (`event_seq`) | The organisation's single, gap-free, increasing counter. Every durable event gets the next value. |
| **cursor** | The last event sequence a client has seen. |

## MVP scope

One organisation, created by a first-run setup that needs a setup token and
succeeds only once. `RIBBITTO_SIGNUP=on` opens sign-up after setup; new
accounts join the setup row's organisation as members. `off`, empty or unset disables sign-up
(GET and POST `/signup` return 404); any other value prevents startup.
Two roles: `owner` and `member`. Public channels only: every member can read and write them.

Later, in roughly this order: private channels, direct messages,
invitations, reactions, editing and deleting, row-level security, several
organisations (see [`docs/roadmap.md`](../roadmap.md)).
