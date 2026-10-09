# Roadmap

Where ribbitto is heading, in coarse steps. No dates: this is a hobby
project. What is happening right now is in the pinned **Status** issue
(#1); individual tasks are issues grouped by GitHub milestone.

Update this file when a milestone is finished or the plan changes, not for
day-to-day progress. What the steps aim at is in the [vision](vision.md).

## MVP

| Milestone | Goal | Done when |
|---|---|---|
| **M0 Foundation** ✓ done | Skeleton, lint with enforced layering, PostgreSQL with migrations and sqlc, templ + htmx + Tailwind hello page, architecture and domain docs, i18n (English and Japanese), UI mock and design tokens — plus the AI workflow: templates, cross-review, closure verification, Grok adversarial review | `make check` and CI pass; design tokens chosen |
| **M1 Accounts** ✓ done | First-run setup, sign-up, sign-in, sign-out, server-side sessions | Security checklist met: CSRF, cookie attributes, hashed session tokens, rate limits, one-time setup |
| **M2 Channels and messages** ✓ done | Public channels, posting, history with paging | Two people can talk (after a reload) |
| **M3 Real time** ✓ done | Server-Sent Events hub with per-connection authorization and replay | Messages arrive instantly; nothing is lost on reconnect; nothing reaches a connection that may not read it |
| **Topics** ✓ done | Topics inside channels: the default topic, the feed and topic view, branching ([decision 21](decisions/21-topics-inside-channels-a-default-topic-and-branching-instead-of-threads.md)) | A conversation started in the default topic can be branched into its own topic, and both views show it |
| **M4 Awareness** ← next | Unread counts, presence, typing indicator | The four outcomes under [M4](#m4) below |
| **M5 Polish** | Dark mode, mobile layout, motion | |

The modular-monolith migration ([decision 26](decisions/26-modules-by-feature-layout-seams-and-order.md),
[decision 27](decisions/27-channels-topics-and-messages-are-one-conversation-module.md),
[`modules.md`](architecture/modules.md)) is done: `identity`, `realtime`,
`org` and `conversation` are modules, and the old layers are gone. The
load tests before M4 are done (#216, #219; [results](architecture/load-results.md)).

## M4

M4 is done when a member of an organisation sees:

1. **Unread counts, per topic.** The sidebar shows how many messages the
   member has not read in each channel and in each topic it links. The
   counts follow new messages and reading without a reload, in all of the
   member's tabs. Reading one topic never marks another topic's messages
   read, and branching never changes whether a message is read
   ([`unread.md`](domain/unread.md#topics)).
2. **Where they left off.** Opening a channel or a topic with unread
   messages shows a divider above the first one when it is on the loaded
   page, and it stays put while the member reads.
3. **Who is online.** The channel's members panel marks the members who
   have ribbitto open, and updates live.
4. **Who is typing.** The channel or topic the member is reading shows who
   is typing there; the indicator goes away soon after they stop or send.

Each signal reaches only members who may see it, and a reconnect restores
the current state. How they reach the browser is in decisions
[30](decisions/30-one-organisation-wide-stream-per-tab-filtered-by-its-interests.md) and
[31](decisions/31-presence-and-typing-are-current-state-with-a-generation.md).

Left for later: notifications, sounds and a count in the tab title;
jumping to the first unread message; away, idle and "last seen"; typing in
reply chains; new channels appearing live in the sidebar (#162); the "for
me" view (#433); presence across several server processes (#236).

## After the MVP

Roughly in this order: private channels → direct messages → invitations →
reactions → editing and deleting → PostgreSQL row-level security →
multiple organisations → file uploads → search.

The "for me" view (#433) follows once mentions (#117) and replies exist.

Deleting messages, and moving them to another channel, first need the
stream's rule for [vanished messages](architecture/replay.md#vanished-messages)
(#352).

Email delivery and email verification are not part of M1 to M3 (accounts
still sign in with an email address). Delivery arrives with self-hosting after M3
or when invitations become concrete, whichever comes first, starting with a
Mailer over SMTP ([decision 16](decisions/16-email-goes-through-an-external-smtp-server.md)). The default order is Mailer →
password reset → email verification → invitations, adjusted to when
invitations are scheduled (#128).

Bulk onboarding of an organisation's people goes through invitations and
provisioning, never through the public `/signup` form.

Self-hosting (container image on GHCR, Compose file with PostgreSQL and
Caddy) becomes a release goal once M3 works.

## Not planned

Native mobile apps, federation, voice and video. These may be revisited,
but nothing in the design should be bent for them now.
