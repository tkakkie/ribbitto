# Roadmap

A coarse plan without dates: ribbitto is a hobby project. Current work is
in the pinned **Status** issue (#1); tasks are issues grouped by GitHub
milestone.

The steps' aims are in the [vision](vision.md).

## MVP

| Milestone | Goal | Done when |
|---|---|---|
| **M0 Foundation** ✓ done | Skeleton, lint with enforced layering, PostgreSQL with migrations and sqlc, templ + htmx + Tailwind hello page, architecture and domain docs, i18n (English and Japanese), UI mock and design tokens — plus the AI workflow: templates, cross-review, closure verification, Grok adversarial review | `make check` and CI pass; design tokens chosen |
| **M1 Accounts** ✓ done | First-run setup, sign-up, sign-in, sign-out, server-side sessions | Security checklist met: CSRF, cookie attributes, hashed session tokens, rate limits, one-time setup |
| **M2 Channels and messages** ✓ done | Public channels, posting, history with paging | Two people can talk (after a reload) |
| **M3 Real time** ✓ done | Server-Sent Events hub with per-connection authorization and replay | Messages arrive instantly; nothing is lost on reconnect; nothing reaches a connection that may not read it |
| **Topics** ✓ done | Topics inside channels: the default topic, the feed and topic view, branching ([decision 21](decisions/21-topics-inside-channels-a-default-topic-and-branching-instead-of-threads.md)) | A conversation started in the default topic can be branched into its own topic, and both views show it |
| **M4 Awareness** ✓ done | Unread counts, presence, typing indicator | The four outcomes under [M4](roadmap-mvp.md#m4) |
| **M5 UI pass** ✓ done | The existing pages in a new visual direction, with icons, channel images and motion | The five outcomes under [M5](roadmap-mvp.md#m5) |
| **M6 Dark mode and mobile** ✓ done | Dark colours and a mobile layout for M5's design | The three outcomes under [M6](roadmap-mvp.md#m6) |

The modular-monolith migration ([decision 26](decisions/26-modules-by-feature-layout-seams-and-order.md),
[decision 27](decisions/27-channels-topics-and-messages-are-one-conversation-module.md),
[`modules.md`](architecture/modules.md)) is done: `identity`, `realtime`,
`org` and `conversation` are modules, and the old layers are gone. Pre-M4
load tests are done (#216, #219; [results](measurements/load-results.md)).

## After the MVP

| Milestone | Goal | Done when |
|---|---|---|
| **M9 Front end** (#1404) | A front end that stays safe to change as it grows, and conversations that open at a bounded cost | The five outcomes in [M9](roadmap-m9.md) |
| **M10 Front-end architecture** (#2060) | Front-end complexity that stays bounded as reactions, editing, uploads and search arrive | The five outcomes in [M10](roadmap-m10.md) |

The repository is public, but a release — a
container image and a Compose file with PostgreSQL and Caddy
([decision 7](decisions/07-rental-vps-and-containers.md)) — comes later,
when the maintainer chooses, once more is built. Until then code, security and docs stay
release-ready.

The [vision](vision.md)'s principles stay. Technical constraints set for the MVP may
change after M6 where a need is shown, each through a new decision recording
why and what changes, never by quietly breaking an old one. Roughly in this order:

1. **Revisit the constraints (short).** Measure in the browser: input
   latency, INP, switching, initial JavaScript, memory. Then
   decide, each in its own issue and on measurements or a prototype: the
   front end's roles, set by
   [decision 40](decisions/40-plain-javascript-by-default-library-islands-by-measured-exception.md); composer
   state and browser storage, set by
   [decision 37](decisions/37-composer-javascript-pending-posts-keys-and-drafts.md);
   a body format and safe rendering, set by
   [decision 39](decisions/39-message-bodies-use-a-small-frozen-markup.md);
   the URL of one message, set by
   [decision 36](decisions/36-one-message-has-a-stable-url.md). The rest
   waits for its decision.
2. **Two tracks in parallel.** The composer track (M7) never blocks the basics.
   - *Basics:* private channels → direct messages → email (Mailer →
     password reset → email verification) → invitations (#128).
   - *Composer:* M7 is done (review #1163): reliable sending (no duplicate posts, the text kept when
     sending fails, the sending state and the post shown before the
     server answers) → an input that grows and a send-key setting →
     mentions (#117, #1310–#1313) → links to one message → quoting, including a
     selection → device drafts (#1162; #88 cross-device).
   - *[M9](roadmap-m9.md) (alongside Basics):* follows M7, starting with its
     foundation (phases 1–5); neither M9 as a whole nor its UX
     blocks *Basics* ([position](roadmap-m9.md#position-in-the-roadmap)).
   - *[M10](roadmap-m10.md):* follows M9 and comes before step 3's reactions,
     editing and uploads ([position](roadmap-m10.md#position-in-the-roadmap)).
3. **Messages:** reactions, editing and deleting, [pins](domain/pins.md)
   (#880; rules agreed, implementation after M6), reply chains
   ([decision 22](decisions/22-replies-stay-in-the-stream-with-a-reply-chain-panel.md)),
   file uploads.
4. **Topics:** resolving a topic with its conclusion, following and muting
   topics, an inbox of unread topics with keyboard navigation and jumping to
   the first unread message, the "for me" view (#433, once mentions and
   replies exist), starting a topic from a quoted selection (unlike
   branching, it moves no message), search that handles Japanese, a quick
   switcher.
5. **Integrations and operations,** mostly for a release: webhooks and bots
   that post into topics, Web Push, mention notifications (#1423), import and
   export, deactivating members, PostgreSQL row-level security, multiple
   organisations.

The [vanished messages](architecture/replay.md#vanished-messages) rule is
settled (#352); the first feature deleting or moving a message across
channels implements it.

Email delivery and verification are not in the MVP (accounts still sign in
with an email address). Delivery arrives with step 2's basics track,
starting with a Mailer over SMTP
([decision 16](decisions/16-email-goes-through-an-external-smtp-server.md));
#128 holds the plan and requirements.

Bulk onboarding goes through invitations and provisioning, never the public
`/signup` form.

## Not planned

Native mobile apps, federation, voice and video. These may be revisited,
but nothing in the design should be bent for them now.

Not planned for now either: turning the front end into a Svelte
single-page app (the message list, sidebar and counts stay server-rendered),
offline message caching ([decision 41, class 3](decisions/41-client-storage-indexeddb-by-data-class.md#3-message-content)), read receipts per
message, and tasks.
