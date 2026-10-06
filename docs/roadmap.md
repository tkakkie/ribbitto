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
| **M4 Awareness** ← next, after the load tests below | Unread counts, presence, typing indicator | |
| **M5 Polish** | Dark mode, mobile layout, motion | |

The modular-monolith migration ([decision 26](decisions/26-modules-by-feature-layout-seams-and-order.md),
[decision 27](decisions/27-channels-topics-and-messages-are-one-conversation-module.md),
[`modules.md`](architecture/modules.md)) is done: `identity`, `realtime`,
`org` and `conversation` are modules, and the old layers are gone. Before
M4 come the load tests (#216, #219).

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
